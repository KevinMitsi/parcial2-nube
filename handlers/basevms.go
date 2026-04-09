package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
)

type BaseVM struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	HasRootKeys bool   `json:"hasRootKeys"`
	DiskCreated bool   `json:"diskCreated"`
	DiskPath    string `json:"diskPath"`
}

func AddBaseVM(w http.ResponseWriter, r *http.Request) {
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

	// Verificar que no exista
	for _, vm := range state.BaseVMs {
		if vm.Name == req.Name {
			http.Error(w, "VM ya existe", http.StatusConflict)
			return
		}
	}

	state.BaseVMs = append(state.BaseVMs, BaseVM{
		Name:        req.Name,
		Description: req.Description,
		HasRootKeys: false,
		DiskCreated: false,
	})

	saveStateFn()

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func HandleBaseVMActions(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/basevms/")
	parts := strings.Split(path, "/")

	if len(parts) < 2 {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	vmName := parts[0]
	action := parts[1]

	switch action {
	case "rootkeys":
		if r.Method == "POST" {
			CreateRootKeys(w, r, vmName)
		} else if r.Method == "GET" && len(parts) > 2 && parts[2] == "download" {
			DownloadRootKeys(w, r, vmName)
		}
	case "disk":
		if r.Method == "POST" {
			CreateMultiAttachDisk(w, r, vmName)
		}
	default:
		http.Error(w, "Unknown action", http.StatusNotFound)
	}
}
