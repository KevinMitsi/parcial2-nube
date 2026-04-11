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

	guestPubKeyPath := "/tmp/platform_root_id_rsa.pub"
	if err := vboxmanage.GuestCopyTo(targetVMName, guestUsername, guestPassword, pubKeyPathAbs, guestPubKeyPath); err != nil {
		return fmt.Errorf("no se pudo copiar llave root al guest: %v", err)
	}

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

	// Req 3: Crear directorio para llaves en keys/{vm_name}/root/
	stepStart := time.Now()
	keyDir := filepath.Join("keys", vmName, "root")
	os.MkdirAll(keyDir, 0755)
	log.LogOperationStep("Crear directorio de llaves", time.Since(stepStart))

	keyPath := filepath.Join(keyDir, "id_rsa")
	pubKeyPath := keyPath + ".pub"
	pubKeyPathAbs := pubKeyPath
	if absPath, absErr := filepath.Abs(pubKeyPath); absErr == nil {
		pubKeyPathAbs = absPath
	}

	keysAlreadyExist := false
	if _, errPriv := os.Stat(keyPath); errPriv == nil {
		if _, errPub := os.Stat(pubKeyPath); errPub == nil {
			keysAlreadyExist = true
			log.Info("Llaves root ya existentes en: %s", keyDir)
		}
	}

	if !keysAlreadyExist {
		// Req 3: Generar llaves RSA 1024
		stepStart = time.Now()
		cmd := exec.Command("ssh-keygen", "-t", "rsa", "-b", "1024", "-f", keyPath, "-N", "", "-C", fmt.Sprintf("root@%s", vmName))
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.LogOperationError("CreateRootKeys", "generate-keys", err)
			http.Error(w, fmt.Sprintf("Error generando llaves: %v | output: %s", err, string(out)), http.StatusInternalServerError)
			return
		}
		log.LogOperationStep("Generar llaves RSA 1024", time.Since(stepStart))
		log.Info("Llaves generadas en: %s", keyDir)
	} else {
		log.LogOperationStep("Reutilizar llaves RSA 1024", time.Since(stepStart))
	}

	if _, err := os.Stat(pubKeyPathAbs); err != nil {
		log.LogOperationError("CreateRootKeys", "validate-pubkey-path", err)
		http.Error(w, fmt.Sprintf("No se encontró la llave pública en ruta esperada: %s", pubKeyPathAbs), http.StatusInternalServerError)
		return
	}

	// Instalar llave pública root dentro de la VM padre.
	stepStart = time.Now()
	running, err := vboxmanage.IsVMRunning(vmName)
	if err != nil {
		log.LogOperationError("CreateRootKeys", "check-vm-running", err)
		http.Error(w, fmt.Sprintf("Error verificando estado de VM base: %v", err), http.StatusInternalServerError)
		return
	}
	if !running {
		err := fmt.Errorf("la VM base no está encendida")
		log.LogOperationError("CreateRootKeys", "vm-not-running", err)
		http.Error(w, "Enciende la VM base antes de crear/instalar llaves root", http.StatusBadRequest)
		return
	}

	if err := InstallRootPublicKeyInGuest(vmName, vmName, guestUsername, guestPassword); err != nil {
		log.LogOperationError("CreateRootKeys", "install-root-pubkey", err)
		http.Error(w, fmt.Sprintf("No se pudo instalar la llave root en la VM base. Si usas un usuario no-root, debe tener sudo configurado: %v", err), http.StatusBadRequest)
		return
	}
	log.LogOperationStep("Instalar llave root en VM base", time.Since(stepStart))

	// Req 3: Actualizar estado hasRootKeys
	baseVM.HasRootKeys = true
	saveStateFn()

	log.LogOperationComplete("CreateRootKeys", time.Since(start),
		fmt.Sprintf("VM: %s, Path: %s", vmName, keyDir))
	log.SetOperationID("")

	status := "ok"
	httpStatus := http.StatusCreated
	if keysAlreadyExist {
		status = "already_exists"
		httpStatus = http.StatusOK
	}
	w.WriteHeader(httpStatus)
	json.NewEncoder(w).Encode(map[string]string{"status": status})
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
func CreateUserInVM(vmIP, rootKeyPath, username, userPubKey string) error {
	log := logger.Get()
	log.Info("Creando usuario '%s' en VM (IP: %s)", username, vmIP)

	// Req 15: Esperar conexión SSH con reintentos
	if err := WaitForSSH(vmIP, rootKeyPath, 3); err != nil {
		return err
	}

	// Req 9: Crear usuario con directorio home y shell bash
	stepStart := time.Now()
	createUserCmd := fmt.Sprintf("useradd -m -s /bin/bash %s && mkdir -p /home/%s/.ssh && chmod 700 /home/%s/.ssh && chown %s:%s /home/%s/.ssh",
		username, username, username, username, username, username)

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
	log.Info("Usuario creado en %v", time.Since(stepStart))

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

	log.Info("Usuario '%s' creado exitosamente con acceso SSH", username)
	return nil
}
