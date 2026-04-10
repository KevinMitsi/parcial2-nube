package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"vbox-platform/logger"
	"vbox-platform/vboxmanage"
)

type BaseVM struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	HasRootKeys   bool   `json:"hasRootKeys"`
	DiskCreated   bool   `json:"diskCreated"`
	DiskPath      string `json:"diskPath"`
	DiskUUID      string `json:"diskUUID"`
	DiskConverted bool   `json:"diskConverted"`
}

func AddBaseVM(w http.ResponseWriter, r *http.Request) {
	opID := fmt.Sprintf("add-basevm-%d", time.Now().Unix())
	log := logger.Get()
	log.SetOperationID(opID)

	start := time.Now()
	log.LogOperation("AddBaseVM", "", "system")

	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.LogOperationError("AddBaseVM", "decode-request", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	state.Mu.Lock()
	defer state.Mu.Unlock()

	// Req 2: Validar que la VM existe en VirtualBox
	stepStart := time.Now()
	exists, err := vboxmanage.VMExists(req.Name)
	if err != nil {
		log.LogOperationError("AddBaseVM", "check-vm-exists", err)
		http.Error(w, fmt.Sprintf("Error verificando VM: %v", err), http.StatusInternalServerError)
		return
	}

	if !exists {
		err := fmt.Errorf("la VM '%s' no existe en VirtualBox", req.Name)
		log.LogOperationError("AddBaseVM", "vm-not-found", err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	log.LogOperationStep("VM existe en VirtualBox", time.Since(stepStart))

	// Verificar si está encendida
	stepStart = time.Now()
	running, _ := vboxmanage.IsVMRunning(req.Name)
	if running {
		log.Warn("VM '%s' está encendida. Se recomienda apagarla antes de agregarla", req.Name)
	}
	log.LogOperationStep("Verificar estado de VM", time.Since(stepStart))

	// Verificar que no exista en el estado
	for _, vm := range state.BaseVMs {
		if vm.Name == req.Name {
			err := fmt.Errorf("VM ya existe en el estado")
			log.LogOperationError("AddBaseVM", "duplicate-vm", err)
			http.Error(w, "VM ya existe", http.StatusConflict)
			return
		}
	}

	state.BaseVMs = append(state.BaseVMs, BaseVM{
		Name:          req.Name,
		Description:   req.Description,
		HasRootKeys:   false,
		DiskCreated:   false,
		DiskConverted: false,
	})

	saveStateFn()

	log.LogOperationComplete("AddBaseVM", time.Since(start), fmt.Sprintf("VM: %s", req.Name))
	log.SetOperationID("")

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
