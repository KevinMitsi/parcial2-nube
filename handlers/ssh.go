package handlers

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
	"vbox-platform/logger"
)

func CreateRootKeys(w http.ResponseWriter, r *http.Request, vmName string) {
	opID := fmt.Sprintf("create-rootkeys-%s-%d", vmName, time.Now().Unix())
	log := logger.Get()
	log.SetOperationID(opID)

	start := time.Now()
	log.LogOperation("CreateRootKeys", vmName, "system")

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

	// Si las llaves ya existen, tratar la operación como idempotente.
	if _, errPriv := os.Stat(keyPath); errPriv == nil {
		if _, errPub := os.Stat(pubKeyPath); errPub == nil {
			baseVM.HasRootKeys = true
			saveStateFn()

			log.Info("Llaves root ya existentes en: %s", keyDir)
			log.LogOperationComplete("CreateRootKeys", time.Since(start),
				fmt.Sprintf("VM: %s, Path: %s, Status: already_exists", vmName, keyDir))
			log.SetOperationID("")

			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "already_exists"})
			return
		}
	}

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

	// Req 3: Actualizar estado hasRootKeys
	baseVM.HasRootKeys = true
	saveStateFn()

	log.LogOperationComplete("CreateRootKeys", time.Since(start),
		fmt.Sprintf("VM: %s, Path: %s", vmName, keyDir))
	log.SetOperationID("")

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
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

	for i := 0; i < maxRetries; i++ {
		log.Debug("Intento SSH %d/%d", i+1, maxRetries)

		// Req 15: Timeout de 3 segundos
		cmd := exec.Command("ssh", "-i", keyPath, "-o", "StrictHostKeyChecking=no", "-o", "ConnectTimeout=3", fmt.Sprintf("root@%s", vmIP), "echo ok")
		if err := cmd.Run(); err == nil {
			log.Info("Conexión SSH establecida exitosamente")
			return nil
		} else {
			log.Warn("Intento SSH %d falló: %v", i+1, err)
		}

		// Req 15: Espera de 3 segundos entre intentos
		if i < maxRetries-1 {
			time.Sleep(3 * time.Second)
		}
	}

	err := fmt.Errorf("timeout esperando SSH después de %d intentos", maxRetries)
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

	cmd := exec.Command("ssh", "-i", rootKeyPath, "-o", "StrictHostKeyChecking=no", "-o", "ConnectTimeout=3",
		fmt.Sprintf("root@%s", vmIP), createUserCmd)

	if err := cmd.Run(); err != nil {
		log.Error("Error creando usuario: %v", err)
		return fmt.Errorf("error creando usuario: %v", err)
	}
	log.Info("Usuario creado en %v", time.Since(stepStart))

	// Req 9: Instalar llave pública en authorized_keys con permisos 600
	stepStart = time.Now()
	installKeyCmd := fmt.Sprintf("echo '%s' >> /home/%s/.ssh/authorized_keys && chmod 600 /home/%s/.ssh/authorized_keys && chown %s:%s /home/%s/.ssh/authorized_keys",
		userPubKey, username, username, username, username, username)

	cmd = exec.Command("ssh", "-i", rootKeyPath, "-o", "StrictHostKeyChecking=no", "-o", "ConnectTimeout=3",
		fmt.Sprintf("root@%s", vmIP), installKeyCmd)

	if err := cmd.Run(); err != nil {
		log.Error("Error instalando llave pública: %v", err)
		return fmt.Errorf("error instalando llave: %v", err)
	}
	log.Info("Llave pública instalada en %v", time.Since(stepStart))

	log.Info("Usuario '%s' creado exitosamente con acceso SSH", username)
	return nil
}
