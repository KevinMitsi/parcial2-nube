package handlers

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"vbox-platform/logger"
	"vbox-platform/vboxmanage"
)

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func InstallRootPublicKeyInGuest(targetVMName, keyVMName, guestUsername, guestPassword string) error {
	log := logger.Get()

	if strings.TrimSpace(guestUsername) == "" || strings.TrimSpace(guestPassword) == "" {
		return fmt.Errorf("credenciales guest inválidas")
	}

	pubKeyPath := filepath.Join("keys", keyVMName, "root", "id_rsa.pub")
	pubKeyPathAbs := pubKeyPath
	if absPath, absErr := filepath.Abs(pubKeyPath); absErr == nil {
		pubKeyPathAbs = absPath
	}

	if _, err := os.Stat(pubKeyPathAbs); err != nil {
		return fmt.Errorf("no se encontró llave root pública para %s en %s: %v", keyVMName, pubKeyPathAbs, err)
	}

	// Leer el contenido de la llave pública
	pubKeyData, err := os.ReadFile(pubKeyPathAbs)
	if err != nil {
		return fmt.Errorf("no se pudo leer llave pública: %v", err)
	}
	pubKeyContent := strings.TrimSpace(string(pubKeyData))

	// Método 1: Intentar con VBoxManage guestcontrol (método original)
	guestPubKeyPath := "/tmp/platform_root_id_rsa.pub"
	if err := vboxmanage.GuestCopyTo(targetVMName, guestUsername, guestPassword, pubKeyPathAbs, guestPubKeyPath); err != nil {
		log.Debug("VBoxManage copyto falló: %v, intentando método alternativo...", err)

		// Método 2: Crear el archivo directamente con el contenido usando echo
		// Esto evita la necesidad de copiar archivos
		createFileCmd := fmt.Sprintf("mkdir -p /tmp && echo '%s' > /tmp/platform_root_id_rsa.pub", pubKeyContent)
		runCmdCreate := createFileCmd
		if guestUsername != "root" {
			runCmdCreate = fmt.Sprintf("printf %%s %s | sudo -S -p '' /bin/bash -lc %s", shQuote(guestPassword+"\n"), shQuote(createFileCmd))
		}

		if err := vboxmanage.GuestRunBash(targetVMName, guestUsername, guestPassword, runCmdCreate); err != nil {
			return fmt.Errorf("no se pudo crear archivo de llave en guest: %v", err)
		}
	}

	// Instalar la llave en authorized_keys
	installCmd := "mkdir -p /root/.ssh && cat /tmp/platform_root_id_rsa.pub >> /root/.ssh/authorized_keys && chmod 700 /root/.ssh && chmod 600 /root/.ssh/authorized_keys && chown -R root:root /root/.ssh"
	runCmd := installCmd
	if guestUsername != "root" {
		runCmd = fmt.Sprintf("printf %%s %s | sudo -S -p '' /bin/bash -lc %s", shQuote(guestPassword+"\n"), shQuote(installCmd))
	}

	if err := vboxmanage.GuestRunBash(targetVMName, guestUsername, guestPassword, runCmd); err != nil {
		return fmt.Errorf("no se pudo instalar authorized_keys de root en guest: %v", err)
	}

	return nil
}

func InstallRootPublicKeyInGuestWithRetries(targetVMName, keyVMName, guestUsername, guestPassword string, maxRetries int, retryDelay time.Duration) error {
	log := logger.Get()
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		if err := InstallRootPublicKeyInGuest(targetVMName, keyVMName, guestUsername, guestPassword); err == nil {
			return nil
		} else {
			lastErr = err
			log.Warn("Intento %d/%d instalando llave root en %s falló: %v", i+1, maxRetries, targetVMName, err)
		}

		if i < maxRetries-1 {
			time.Sleep(retryDelay)
		}
	}

	return fmt.Errorf("no se pudo instalar llave root en guest después de %d intentos: %v", maxRetries, lastErr)
}

func CreateRootKeys(w http.ResponseWriter, r *http.Request, vmName string) {
	opID := fmt.Sprintf("create-rootkeys-%s-%d", vmName, time.Now().Unix())
	log := logger.Get()
	log.SetOperationID(opID)

	start := time.Now()
	log.LogOperation("CreateRootKeys", vmName, "system")

	var req struct {
		RootPassword  string `json:"rootPassword"`
		GuestUsername string `json:"guestUsername"`
		GuestPassword string `json:"guestPassword"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			log.LogOperationError("CreateRootKeys", "decode-request", err)
			http.Error(w, "Body inválido", http.StatusBadRequest)
			return
		}
	}
	guestUsername := strings.TrimSpace(req.GuestUsername)
	guestPassword := strings.TrimSpace(req.GuestPassword)

	// Compatibilidad con clientes viejos que envían rootPassword.
	if guestPassword == "" && strings.TrimSpace(req.RootPassword) != "" {
		guestUsername = "root"
		guestPassword = strings.TrimSpace(req.RootPassword)
	}

	if guestUsername == "" {
		guestUsername = "root"
	}

	if guestPassword == "" {
		err := fmt.Errorf("password de root no proporcionado")
		log.LogOperationError("CreateRootKeys", "validate-guest-password", err)
		http.Error(w, "Debes proporcionar credenciales del sistema invitado para instalar la llave en la VM base", http.StatusBadRequest)
		return
	}

	state.Mu.Lock()
	defer state.Mu.Unlock()

	// Req 3: Buscar VM base y verificar que existe
	var baseVM *BaseVM
	for i := range state.BaseVMs {
		if state.BaseVMs[i].Name == vmName {
			baseVM = &state.BaseVMs[i]
			break
		}
	}

	if baseVM == nil {
		err := fmt.Errorf("VM no encontrada en el estado")
		log.LogOperationError("CreateRootKeys", "find-vm", err)
		http.Error(w, "VM no encontrada", http.StatusNotFound)
		return
	}

	// Verificar si la VM está encendida
	stepStart := time.Now()
	running, err := vboxmanage.IsVMRunning(vmName)
	if err != nil {
		log.LogOperationError("CreateRootKeys", "check-vm-running", err)
		http.Error(w, fmt.Sprintf("Error verificando estado de VM base: %v", err), http.StatusInternalServerError)
		return
	}

	// Si no está encendida, encenderla
	if !running {
		log.Info("VM %s no está encendida, iniciándola...", vmName)
		if err := vboxmanage.StartVM(vmName); err != nil {
			log.LogOperationError("CreateRootKeys", "start-vm", err)
			http.Error(w, fmt.Sprintf("Error iniciando VM: %v", err), http.StatusInternalServerError)
			return
		}
		log.Info("VM iniciada, esperando a que obtenga IP...")
	}
	log.LogOperationStep("Verificar/iniciar VM", time.Since(stepStart))

	// Esperar a que la VM obtenga IP con ciclo inteligente
	stepStart = time.Now()
	var vmIP string
	maxRetries := 60              // 60 intentos
	retryDelay := 5 * time.Second // 5 segundos entre intentos (total: 5 minutos máximo)

	log.Info("Esperando a que la VM obtenga una IP (máximo %d intentos de %v)...", maxRetries, retryDelay)

	for i := 0; i < maxRetries; i++ {
		vmIP, err = vboxmanage.GetVMIP(vmName)
		if err == nil && vmIP != "" {
			log.Info("VM obtuvo IP: %s (intento %d/%d)", vmIP, i+1, maxRetries)
			break
		}

		if i < maxRetries-1 {
			log.Debug("Intento %d/%d: IP no disponible aún, esperando %v...", i+1, maxRetries, retryDelay)
			time.Sleep(retryDelay)
		}
	}

	if vmIP == "" {
		err := fmt.Errorf("no se pudo obtener IP después de %d intentos (%v)", maxRetries, time.Since(stepStart))
		log.LogOperationError("CreateRootKeys", "get-vm-ip", err)
		http.Error(w, "No se pudo obtener IP de la VM. Asegúrate de que la VM tenga VirtualBox Guest Additions instalado y que la red esté configurada correctamente.", http.StatusBadRequest)
		return
	}
	log.LogOperationStep("Obtener IP de VM", time.Since(stepStart))

	// Crear directorio para llaves en el host
	stepStart = time.Now()
	keyDir := filepath.Join("keys", vmName, "root")
	os.MkdirAll(keyDir, 0755)
	log.LogOperationStep("Crear directorio de llaves en host", time.Since(stepStart))

	keyPath := filepath.Join(keyDir, "id_rsa")
	pubKeyPath := keyPath + ".pub"

	keysAlreadyExist := false
	if _, errPriv := os.Stat(keyPath); errPriv == nil {
		if _, errPub := os.Stat(pubKeyPath); errPub == nil {
			keysAlreadyExist = true
			log.Info("Llaves root ya existentes en host: %s", keyDir)
		}
	}

	if !keysAlreadyExist {
		// NUEVO ENFOQUE: Generar llaves SSH en Windows y luego copiarlas a la VM
		stepStart = time.Now()
		log.Info("Generando llaves SSH en Windows...")

		// Generar llaves SSH en Windows usando ssh-keygen
		keyPathAbs, _ := filepath.Abs(keyPath)
		cmd := exec.Command("ssh-keygen", "-t", "rsa", "-b", "1024", "-f", keyPathAbs, "-N", "", "-C", fmt.Sprintf("root@%s", vmName))
		if out, err := cmd.CombinedOutput(); err != nil {
			log.LogOperationError("CreateRootKeys", "generate-keys-windows", err)
			http.Error(w, fmt.Sprintf("Error generando llaves SSH en Windows: %v | output: %s", err, string(out)), http.StatusInternalServerError)
			return
		}
		log.Info("Llaves SSH generadas en Windows: %s", keyDir)
		log.LogOperationStep("Generar llaves SSH en Windows", time.Since(stepStart))

		// Leer la llave pública generada
		pubKeyData, err := os.ReadFile(pubKeyPath)
		if err != nil {
			log.LogOperationError("CreateRootKeys", "read-public-key", err)
			http.Error(w, fmt.Sprintf("Error leyendo llave pública: %v", err), http.StatusInternalServerError)
			return
		}
		pubKeyContent := strings.TrimSpace(string(pubKeyData))

		// Instalar la llave pública en la VM
		stepStart = time.Now()
		log.Info("Instalando llave pública en VM...")

		// Crear directorio .ssh y agregar llave a authorized_keys
		installCmd := fmt.Sprintf("mkdir -p /root/.ssh && chmod 700 /root/.ssh && echo '%s' >> /root/.ssh/authorized_keys && chmod 600 /root/.ssh/authorized_keys && chown -R root:root /root/.ssh", pubKeyContent)

		var runCmd string
		if guestUsername == "root" {
			runCmd = installCmd
		} else {
			// Para usuarios no-root, usar sudo
			runCmd = fmt.Sprintf("echo %s | sudo -S bash -c %s", shQuote(guestPassword), shQuote(installCmd))
		}

		if err := vboxmanage.GuestRunBash(vmName, guestUsername, guestPassword, runCmd); err != nil {
			log.Error("Error instalando llave en VM: %v", err)
			log.LogOperationError("CreateRootKeys", "install-key-in-vm", err)
			http.Error(w, fmt.Sprintf("Error instalando llave en VM: %v", err), http.StatusInternalServerError)
			return
		}

		log.LogOperationStep("Instalar llave pública en VM", time.Since(stepStart))
		log.Info("Llaves generadas en Windows e instaladas en VM: %s", keyDir)
	} else {
		log.LogOperationStep("Reutilizar llaves existentes", time.Since(stepStart))
	}

	// Verificar que las llaves existen en el host
	if _, err := os.Stat(keyPath); err != nil {
		log.LogOperationError("CreateRootKeys", "validate-private-key", err)
		http.Error(w, fmt.Sprintf("No se encontró la llave privada en: %s", keyPath), http.StatusInternalServerError)
		return
	}
	if _, err := os.Stat(pubKeyPath); err != nil {
		log.LogOperationError("CreateRootKeys", "validate-public-key", err)
		http.Error(w, fmt.Sprintf("No se encontró la llave pública en: %s", pubKeyPath), http.StatusInternalServerError)
		return
	}

	// Req 3: Actualizar estado hasRootKeys y guardar credenciales
	baseVM.HasRootKeys = true
	baseVM.GuestUsername = guestUsername
	baseVM.GuestPassword = guestPassword
	saveStateFn()

	log.LogOperationComplete("CreateRootKeys", time.Since(start),
		fmt.Sprintf("VM: %s, Path: %s, IP: %s", vmName, keyDir, vmIP))
	log.SetOperationID("")

	status := "ok"
	httpStatus := http.StatusCreated
	if keysAlreadyExist {
		status = "already_exists"
		httpStatus = http.StatusOK
	}
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(map[string]string{"status": status, "ip": vmIP})
}

func DownloadRootKeys(w http.ResponseWriter, r *http.Request, vmName string) {
	log := logger.Get()
	log.Info("Descargando llaves de root para VM: %s", vmName)

	keyDir := filepath.Join("keys", vmName, "root")
	privateKey := filepath.Join(keyDir, "id_rsa")
	publicKey := filepath.Join(keyDir, "id_rsa.pub")

	// Req 4: Verificar que existan las llaves
	if _, err := os.Stat(privateKey); os.IsNotExist(err) {
		log.Error("Llaves no encontradas en: %s", keyDir)
		http.Error(w, "Llaves no encontradas", http.StatusNotFound)
		return
	}

	// Req 4: Crear ZIP conteniendo id_rsa e id_rsa.pub
	buf := new(bytes.Buffer)
	zipWriter := zip.NewWriter(buf)

	// Agregar llave privada
	privData, _ := os.ReadFile(privateKey)
	privFile, _ := zipWriter.Create("id_rsa")
	privFile.Write(privData)

	// Agregar llave pública
	pubData, _ := os.ReadFile(publicKey)
	pubFile, _ := zipWriter.Create("id_rsa.pub")
	pubFile.Write(pubData)

	zipWriter.Close()

	// Req 4: Enviar ZIP con nombre {vm_name}_root_keys.zip
	zipName := fmt.Sprintf("%s_root_keys.zip", vmName)
	log.Info("Enviando archivo: %s", zipName)

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", zipName))
	w.Write(buf.Bytes())
}

func InstallRootKey(vmName, vmIP, rootPassword string) error {
	log := logger.Get()
	log.Info("Instalando llave root en VM: %s (IP: %s)", vmName, vmIP)

	keyPath := filepath.Join("keys", vmName, "root", "id_rsa.pub")
	pubKeyData, err := os.ReadFile(keyPath)
	if err != nil {
		log.Error("Error leyendo llave pública: %v", err)
		return err
	}

	pubKey := string(pubKeyData)

	// Comando para instalar la llave
	sshCmd := fmt.Sprintf("mkdir -p ~/.ssh && echo '%s' >> ~/.ssh/authorized_keys && chmod 700 ~/.ssh && chmod 600 ~/.ssh/authorized_keys", pubKey)

	cmd := exec.Command("ssh", "-o", "StrictHostKeyChecking=no", fmt.Sprintf("root@%s", vmIP), sshCmd)
	if err := cmd.Run(); err != nil {
		log.Error("Error instalando llave: %v", err)
		return err
	}

	log.Info("Llave root instalada exitosamente")
	return nil
}

// Req 15: Manejo de errores SSH con timeout y reintentos
func WaitForSSH(vmIP, keyPath string, maxRetries int) error {
	log := logger.Get()
	log.Info("Esperando conexión SSH a %s (máximo %d intentos)", vmIP, maxRetries)
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		log.Debug("Intento SSH %d/%d", i+1, maxRetries)

		// Modo no interactivo: solo llave. Evita quedarse bloqueado pidiendo password.
		cmd := exec.Command("ssh", "-i", keyPath,
			"-o", "StrictHostKeyChecking=no",
			"-o", "ConnectTimeout=3",
			"-o", "BatchMode=yes",
			"-o", "PasswordAuthentication=no",
			fmt.Sprintf("root@%s", vmIP), "echo ok")

		out, err := cmd.CombinedOutput()
		if err == nil {
			log.Info("Conexión SSH establecida exitosamente")
			return nil
		} else {
			lastErr = fmt.Errorf("%v | output: %s", err, string(out))
			log.Warn("Intento SSH %d falló: %v", i+1, lastErr)
		}

		// Req 15: Espera de 3 segundos entre intentos
		if i < maxRetries-1 {
			time.Sleep(3 * time.Second)
		}
	}

	err := fmt.Errorf("timeout esperando SSH después de %d intentos", maxRetries)
	if lastErr != nil {
		errText := strings.ToLower(lastErr.Error())
		if strings.Contains(errText, "permission denied") || strings.Contains(errText, "password") || strings.Contains(errText, "publickey") {
			err = fmt.Errorf("autenticación SSH con llave falló para root@%s. La VM no acepta la llave root generada por la plataforma. Debes configurar acceso root por clave en la VM base y deshabilitar login por password para este flujo. Detalle: %v", vmIP, lastErr)
		} else {
			err = fmt.Errorf("%v | último error: %v", err, lastErr)
		}
	}
	log.Error("Error SSH: host=%s, keyPath=%s, error=%v", vmIP, keyPath, err)
	return err
}

// Req 9: Crear usuario en VM con SSH
func CreateUserInVM(vmIP, rootKeyPath, username, password, userPubKey string) error {
	log := logger.Get()
	log.Info("Creando usuario '%s' en VM (IP: %s)", username, vmIP)

	// Req 15: Esperar conexión SSH con reintentos
	if err := WaitForSSH(vmIP, rootKeyPath, 3); err != nil {
		return err
	}

	// Req 9: Crear usuario con directorio home, shell bash y contraseña
	stepStart := time.Now()
	// Usar chpasswd para establecer la contraseña de forma no interactiva
	createUserCmd := fmt.Sprintf("useradd -m -s /bin/bash %s && echo '%s:%s' | chpasswd && mkdir -p /home/%s/.ssh && chmod 700 /home/%s/.ssh && chown %s:%s /home/%s/.ssh",
		username, username, password, username, username, username, username, username)

	cmd := exec.Command("ssh", "-i", rootKeyPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "ConnectTimeout=3",
		"-o", "BatchMode=yes",
		"-o", "PasswordAuthentication=no",
		fmt.Sprintf("root@%s", vmIP), createUserCmd)

	if out, err := cmd.CombinedOutput(); err != nil {
		log.Error("Error creando usuario: %v", err)
		return fmt.Errorf("error creando usuario: %v | output: %s", err, string(out))
	}
	log.Info("Usuario creado con contraseña en %v", time.Since(stepStart))

	// Req 9: Instalar llave pública en authorized_keys con permisos 600
	stepStart = time.Now()
	installKeyCmd := fmt.Sprintf("echo '%s' >> /home/%s/.ssh/authorized_keys && chmod 600 /home/%s/.ssh/authorized_keys && chown %s:%s /home/%s/.ssh/authorized_keys",
		userPubKey, username, username, username, username, username)

	cmd = exec.Command("ssh", "-i", rootKeyPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "ConnectTimeout=3",
		"-o", "BatchMode=yes",
		"-o", "PasswordAuthentication=no",
		fmt.Sprintf("root@%s", vmIP), installKeyCmd)

	if out, err := cmd.CombinedOutput(); err != nil {
		log.Error("Error instalando llave pública: %v", err)
		return fmt.Errorf("error instalando llave: %v | output: %s", err, string(out))
	}
	log.Info("Llave pública instalada en %v", time.Since(stepStart))

	log.Info("Usuario '%s' creado exitosamente con contraseña y acceso SSH", username)
	return nil
}
