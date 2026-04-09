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
	"strings"
	"sync"
	"vbox-platform/vboxmanage"
)

type UserVM struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	SourceVM    string `json:"sourceVM"`
	DiskPath    string `json:"diskPath"`
	Username    string `json:"username"`
	HasUserKeys bool   `json:"hasUserKeys"`
	IP          string `json:"ip"`
	State       string `json:"state"`
}

type State struct {
	BaseVMs       []BaseVM `json:"baseVMs"`
	Disks         []Disk   `json:"disks"`
	UserVMs       []UserVM `json:"userVMs"`
	BridgeAdapter string   `json:"bridgeAdapter"`
	Mu            sync.RWMutex
}

var state *State
var saveStateFn func()

func InitHandlers(s *State, saveFn func()) {
	state = s
	saveStateFn = saveFn
}

func CreateUserVM(w http.ResponseWriter, r *http.Request, diskName string) {
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	state.Mu.Lock()
	defer state.Mu.Unlock()

	// Buscar disco
	var disk *Disk
	for i := range state.Disks {
		if state.Disks[i].Name == diskName {
			disk = &state.Disks[i]
			break
		}
	}

	if disk == nil {
		http.Error(w, "Disco no encontrado", http.StatusNotFound)
		return
	}

	vmName := fmt.Sprintf("UserVM_%s", req.Name)

	// Crear VM
	if err := vboxmanage.CreateVM(vmName, "Debian_64"); err != nil {
		http.Error(w, fmt.Sprintf("Error creando VM: %v", err), http.StatusInternalServerError)
		return
	}

	// Configurar VM
	if err := vboxmanage.ModifyVM(vmName, "--memory", "1024", "--nic1", "bridged", "--bridgeadapter1", state.BridgeAdapter); err != nil {
		http.Error(w, fmt.Sprintf("Error configurando VM: %v", err), http.StatusInternalServerError)
		return
	}

	// Agregar controlador SATA
	if err := vboxmanage.AddStorageController(vmName, "SATA"); err != nil {
		http.Error(w, fmt.Sprintf("Error agregando controlador: %v", err), http.StatusInternalServerError)
		return
	}

	// Conectar disco
	if err := vboxmanage.AttachDisk(vmName, "SATA", disk.Path); err != nil {
		http.Error(w, fmt.Sprintf("Error conectando disco: %v", err), http.StatusInternalServerError)
		return
	}

	// Iniciar VM
	if err := vboxmanage.StartVM(vmName); err != nil {
		http.Error(w, fmt.Sprintf("Error iniciando VM: %v", err), http.StatusInternalServerError)
		return
	}

	disk.Connected = true

	state.UserVMs = append(state.UserVMs, UserVM{
		Name:        vmName,
		Description: req.Description,
		SourceVM:    disk.SourceVM,
		DiskPath:    disk.Path,
		State:       "running",
	})

	saveStateFn()

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "vmName": vmName})
}

func HandleUserVMActions(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/uservms/")
	parts := strings.Split(path, "/")

	if len(parts) < 1 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	vmName := parts[0]

	if len(parts) == 1 && r.Method == "DELETE" {
		DeleteUserVM(w, r, vmName)
		return
	}

	if len(parts) < 2 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	action := parts[1]

	switch action {
	case "user":
		if r.Method == "POST" {
			CreateUser(w, r, vmName)
		}
	case "keys":
		if r.Method == "GET" && len(parts) > 2 && parts[2] == "download" {
			DownloadUserKeys(w, r, vmName)
		}
	default:
		http.Error(w, "Unknown action", http.StatusNotFound)
	}
}

func CreateUser(w http.ResponseWriter, r *http.Request, vmName string) {
	var req struct {
		Username string `json:"username"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	state.Mu.Lock()
	defer state.Mu.Unlock()

	// Buscar UserVM
	var userVM *UserVM
	for i := range state.UserVMs {
		if state.UserVMs[i].Name == vmName {
			userVM = &state.UserVMs[i]
			break
		}
	}

	if userVM == nil {
		http.Error(w, "VM no encontrada", http.StatusNotFound)
		return
	}

	// Obtener IP de la VM
	ip, err := vboxmanage.GetVMIP(vmName)
	if err != nil {
		http.Error(w, fmt.Sprintf("VM debe estar encendida: %v", err), http.StatusBadRequest)
		return
	}

	userVM.IP = ip

	// Generar llaves para el usuario
	keyDir := filepath.Join("keys", vmName, req.Username)
	os.MkdirAll(keyDir, 0755)

	keyPath := filepath.Join(keyDir, "id_rsa")

	cmd := exec.Command("ssh-keygen", "-t", "rsa", "-b", "1024", "-f", keyPath, "-N", "", "-C", fmt.Sprintf("%s@%s", req.Username, vmName))
	if err := cmd.Run(); err != nil {
		http.Error(w, fmt.Sprintf("Error generando llaves: %v", err), http.StatusInternalServerError)
		return
	}

	// Leer llave pública
	pubKeyData, _ := os.ReadFile(keyPath + ".pub")
	pubKey := string(pubKeyData)

	// Obtener llave root de la VM base
	rootKeyPath := filepath.Join("keys", userVM.SourceVM, "root", "id_rsa")

	// Crear usuario en la VM
	if err := CreateUserInVM(ip, rootKeyPath, req.Username, pubKey); err != nil {
		http.Error(w, fmt.Sprintf("Error creando usuario en VM: %v", err), http.StatusInternalServerError)
		return
	}

	userVM.Username = req.Username
	userVM.HasUserKeys = true
	saveStateFn()

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func DownloadUserKeys(w http.ResponseWriter, r *http.Request, vmName string) {
	state.Mu.RLock()
	defer state.Mu.RUnlock()

	var userVM *UserVM
	for i := range state.UserVMs {
		if state.UserVMs[i].Name == vmName {
			userVM = &state.UserVMs[i]
			break
		}
	}

	if userVM == nil || userVM.Username == "" {
		http.Error(w, "Usuario no encontrado", http.StatusNotFound)
		return
	}

	keyDir := filepath.Join("keys", vmName, userVM.Username)
	privateKey := filepath.Join(keyDir, "id_rsa")
	publicKey := filepath.Join(keyDir, "id_rsa.pub")

	// Crear ZIP
	buf := new(bytes.Buffer)
	zipWriter := zip.NewWriter(buf)

	privData, _ := os.ReadFile(privateKey)
	privFile, _ := zipWriter.Create("id_rsa")
	privFile.Write(privData)

	pubData, _ := os.ReadFile(publicKey)
	pubFile, _ := zipWriter.Create("id_rsa.pub")
	pubFile.Write(pubData)

	zipWriter.Close()

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s_%s_keys.zip", vmName, userVM.Username))
	w.Write(buf.Bytes())
}

func DeleteUserVM(w http.ResponseWriter, r *http.Request, vmName string) {
	state.Mu.Lock()
	defer state.Mu.Unlock()

	for i, vm := range state.UserVMs {
		if vm.Name == vmName {
			// Apagar VM si está encendida
			vboxmanage.PowerOffVM(vmName)

			// Desregistrar VM
			if err := vboxmanage.UnregisterVM(vmName); err != nil {
				http.Error(w, fmt.Sprintf("Error eliminando VM: %v", err), http.StatusInternalServerError)
				return
			}

			// Actualizar estado del disco
			for j := range state.Disks {
				if state.Disks[j].Path == vm.DiskPath {
					state.Disks[j].Connected = false
					break
				}
			}

			// Remover del estado
			state.UserVMs = append(state.UserVMs[:i], state.UserVMs[i+1:]...)
			saveStateFn()

			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}
	}

	http.Error(w, "VM no encontrada", http.StatusNotFound)
}
