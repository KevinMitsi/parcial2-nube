package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"vbox-platform/vboxmanage"
)

type Disk struct {
	Name      string `json:"name"`
	SourceVM  string `json:"sourceVM"`
	Path      string `json:"path"`
	Connected bool   `json:"connected"`
}

func CreateMultiAttachDisk(w http.ResponseWriter, r *http.Request, vmName string) {
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

	if !baseVM.HasRootKeys {
		http.Error(w, "Debe crear llaves de root primero", http.StatusBadRequest)
		return
	}

	// Obtener info de la VM para encontrar el disco
	info, err := vboxmanage.GetVMInfo(vmName)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error obteniendo info de VM: %v", err), http.StatusInternalServerError)
		return
	}

	// Buscar el disco principal (SATA-0-0)
	originalDisk := info["SATA-0-0"]
	if originalDisk == "" {
		http.Error(w, "No se encontró disco en la VM", http.StatusNotFound)
		return
	}

	// Crear nombre para el nuevo disco
	diskName := fmt.Sprintf("%s_multiattach", vmName)
	diskDir := filepath.Dir(originalDisk)
	newDiskPath := filepath.Join(diskDir, diskName+".vdi")

	// Clonar disco
	if err := vboxmanage.CloneDisk(originalDisk, newDiskPath); err != nil {
		http.Error(w, fmt.Sprintf("Error clonando disco: %v", err), http.StatusInternalServerError)
		return
	}

	// Actualizar estado
	baseVM.DiskCreated = true
	baseVM.DiskPath = newDiskPath

	state.Disks = append(state.Disks, Disk{
		Name:      diskName,
		SourceVM:  vmName,
		Path:      newDiskPath,
		Connected: false,
	})

	saveStateFn()

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "diskPath": newDiskPath})
}

func HandleDiskActions(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/disks/")
	parts := strings.Split(path, "/")

	if len(parts) < 2 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	diskName := parts[0]
	action := parts[1]

	switch action {
	case "uservms":
		if r.Method == "POST" {
			CreateUserVM(w, r, diskName)
		}
	case "connect":
		if r.Method == "POST" {
			ConnectDisk(w, r, diskName)
		}
	case "disconnect":
		if r.Method == "POST" {
			DisconnectDisk(w, r, diskName)
		}
	default:
		if r.Method == "DELETE" && action == "" {
			DeleteDisk(w, r, diskName)
		} else {
			http.Error(w, "Unknown action", http.StatusNotFound)
		}
	}
}

func ConnectDisk(w http.ResponseWriter, r *http.Request, diskName string) {
	state.Mu.Lock()
	defer state.Mu.Unlock()

	for i := range state.Disks {
		if state.Disks[i].Name == diskName {
			state.Disks[i].Connected = true
			saveStateFn()
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}
	}

	http.Error(w, "Disco no encontrado", http.StatusNotFound)
}

func DisconnectDisk(w http.ResponseWriter, r *http.Request, diskName string) {
	state.Mu.Lock()
	defer state.Mu.Unlock()

	for i := range state.Disks {
		if state.Disks[i].Name == diskName {
			state.Disks[i].Connected = false
			saveStateFn()
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}
	}

	http.Error(w, "Disco no encontrado", http.StatusNotFound)
}

func DeleteDisk(w http.ResponseWriter, r *http.Request, diskName string) {
	state.Mu.Lock()
	defer state.Mu.Unlock()

	for i, disk := range state.Disks {
		if disk.Name == diskName {
			// Eliminar disco de VirtualBox
			if err := vboxmanage.DeleteDisk(disk.Path); err != nil {
				http.Error(w, fmt.Sprintf("Error eliminando disco: %v", err), http.StatusInternalServerError)
				return
			}

			// Remover del estado
			state.Disks = append(state.Disks[:i], state.Disks[i+1:]...)
			saveStateFn()

			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}
	}

	http.Error(w, "Disco no encontrado", http.StatusNotFound)
}
