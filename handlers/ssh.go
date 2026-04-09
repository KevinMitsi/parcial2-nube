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
)

func CreateRootKeys(w http.ResponseWriter, r *http.Request, vmName string) {
	state.Mu.Lock()
	defer state.Mu.Unlock()

	// Buscar VM base
	var baseVM *BaseVM
	for i := range state.BaseVMs {
		if state.BaseVMs[i].Name == vmName {
			baseVM = &state.BaseVMs[i]
			break
		}
	}

	if baseVM == nil {
		http.Error(w, "VM no encontrada", http.StatusNotFound)
		return
	}

	// Crear directorio para llaves
	keyDir := filepath.Join("keys", vmName, "root")
	os.MkdirAll(keyDir, 0755)

	keyPath := filepath.Join(keyDir, "id_rsa")

	// Generar llaves RSA 1024
	cmd := exec.Command("ssh-keygen", "-t", "rsa", "-b", "1024", "-f", keyPath, "-N", "", "-C", fmt.Sprintf("root@%s", vmName))
	if err := cmd.Run(); err != nil {
		http.Error(w, fmt.Sprintf("Error generando llaves: %v", err), http.StatusInternalServerError)
		return
	}

	baseVM.HasRootKeys = true
	saveStateFn()

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func DownloadRootKeys(w http.ResponseWriter, r *http.Request, vmName string) {
	keyDir := filepath.Join("keys", vmName, "root")
	privateKey := filepath.Join(keyDir, "id_rsa")
	publicKey := filepath.Join(keyDir, "id_rsa.pub")

	// Verificar que existan
	if _, err := os.Stat(privateKey); os.IsNotExist(err) {
		http.Error(w, "Llaves no encontradas", http.StatusNotFound)
		return
	}

	// Crear ZIP en memoria
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

	// Enviar ZIP
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s_root_keys.zip", vmName))
	w.Write(buf.Bytes())
}

func InstallRootKey(vmName, vmIP, rootPassword string) error {
	keyPath := filepath.Join("keys", vmName, "root", "id_rsa.pub")
	pubKeyData, err := os.ReadFile(keyPath)
	if err != nil {
		return err
	}

	pubKey := string(pubKeyData)

	// Comando para instalar la llave
	sshCmd := fmt.Sprintf("mkdir -p ~/.ssh && echo '%s' >> ~/.ssh/authorized_keys && chmod 700 ~/.ssh && chmod 600 ~/.ssh/authorized_keys", pubKey)

	cmd := exec.Command("ssh", "-o", "StrictHostKeyChecking=no", fmt.Sprintf("root@%s", vmIP), sshCmd)
	return cmd.Run()
}

func WaitForSSH(vmIP, keyPath string, maxRetries int) error {
	for i := 0; i < maxRetries; i++ {
		cmd := exec.Command("ssh", "-i", keyPath, "-o", "StrictHostKeyChecking=no", "-o", "ConnectTimeout=3", fmt.Sprintf("root@%s", vmIP), "echo ok")
		if err := cmd.Run(); err == nil {
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("timeout esperando SSH")
}

func CreateUserInVM(vmIP, rootKeyPath, username, userPubKey string) error {
	// Crear usuario
	createUserCmd := fmt.Sprintf("useradd -m -s /bin/bash %s && mkdir -p /home/%s/.ssh && chmod 700 /home/%s/.ssh && chown %s:%s /home/%s/.ssh", username, username, username, username, username, username)

	cmd := exec.Command("ssh", "-i", rootKeyPath, "-o", "StrictHostKeyChecking=no", fmt.Sprintf("root@%s", vmIP), createUserCmd)
	if err := cmd.Run(); err != nil {
		return err
	}

	// Instalar llave pública
	installKeyCmd := fmt.Sprintf("echo '%s' >> /home/%s/.ssh/authorized_keys && chmod 600 /home/%s/.ssh/authorized_keys && chown %s:%s /home/%s/.ssh/authorized_keys", userPubKey, username, username, username, username, username)

	cmd = exec.Command("ssh", "-i", rootKeyPath, "-o", "StrictHostKeyChecking=no", fmt.Sprintf("root@%s", vmIP), installKeyCmd)
	return cmd.Run()
}
