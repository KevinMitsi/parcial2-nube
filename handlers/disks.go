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

type Disk struct {
	Name      string `json:"name"`
	SourceVM  string `json:"sourceVM"`
	Path      string `json:"path"`
	UUID      string `json:"uuid"`
	Connected bool   `json:"connected"`
}

func CreateMultiAttachDisk(w http.ResponseWriter, r *http.Request, vmName string) {
	opID := fmt.Sprintf("create-disk-%s-%d", vmName, time.Now().Unix())
	log := logger.Get()
	log.SetOperationID(opID)

	start := time.Now()
	log.LogOperation("CreateMultiAttachDisk", vmName, "system")

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
		err := fmt.Errorf("VM no encontrada")
		log.LogOperationError("CreateMultiAttachDisk", "find-vm", err)
		http.Error(w, "VM no encontrada", http.StatusNotFound)
		return
	}

	// Req 5: Validar que existan llaves de root
	if !baseVM.HasRootKeys {
		err := fmt.Errorf("debe crear llaves de root primero")
		log.LogOperationError("CreateMultiAttachDisk", "validate-root-keys", err)
		http.Error(w, "Debe crear llaves de root primero", http.StatusBadRequest)
		return
	}

	// Req 6: Obtener info de la VM para encontrar el disco
	stepStart := time.Now()
	info, err := vboxmanage.GetVMInfo(vmName)
	if err != nil {
		log.LogOperationError("CreateMultiAttachDisk", "get-vm-info", err)
		http.Error(w, fmt.Sprintf("Error obteniendo info de VM: %v", err), http.StatusInternalServerError)
		return
	}
	log.LogOperationStep("Obtener info de VM", time.Since(stepStart))

	// Buscar el disco principal (SATA-0-0)
	originalDisk := info["SATA-0-0"]
	if originalDisk == "" {
		err := fmt.Errorf("no se encontró disco en la VM")
		log.LogOperationError("CreateMultiAttachDisk", "find-disk", err)
		http.Error(w, "No se encontró disco en la VM", http.StatusNotFound)
		return
	}

	log.Info("Disco original encontrado: %s", originalDisk)

	// Verificar tipo de disco actual
	stepStart = time.Now()
	diskType, err := vboxmanage.GetDiskType(originalDisk)
	if err != nil {
		log.LogOperationError("CreateMultiAttachDisk", "get-disk-type", err)
		http.Error(w, fmt.Sprintf("Error obteniendo tipo de disco: %v", err), http.StatusInternalServerError)
		return
	}
	log.LogOperationStep("Verificar tipo de disco", time.Since(stepStart))
	log.Info("Tipo de disco actual: %s", diskType)

	// Req 6: Verificar si ya es multiattach
	if strings.Contains(strings.ToLower(diskType), "multiattach") {
		err := fmt.Errorf("el disco ya es de tipo multiconexión")
		log.LogOperationError("CreateMultiAttachDisk", "disk-already-multiattach", err)
		http.Error(w, "El disco ya fue convertido a multiconexión", http.StatusConflict)
		return
	}

	// Obtener el nombre del controlador SATA
	storageCtrl := info["storagecontrollername1"]
	if storageCtrl == "" {
		storageCtrl = "SATA" // fallback al nombre por defecto
	}
	log.Info("Controlador de almacenamiento: %s", storageCtrl)

	// Desconectar el disco de la VM antes de convertirlo
	stepStart = time.Now()
	if err := vboxmanage.DetachDisk(vmName, storageCtrl); err != nil {
		log.LogOperationError("CreateMultiAttachDisk", "detach-disk", err)
		http.Error(w, fmt.Sprintf("Error desconectando disco: %v", err), http.StatusInternalServerError)
		return
	}
	log.LogOperationStep("Desconectar disco de VM", time.Since(stepStart))

	// Convertir disco a multiattach
	stepStart = time.Now()
	if err := vboxmanage.ConvertDiskToMultiAttach(originalDisk); err != nil {
		// Si falla, intentar reconectar el disco
		vboxmanage.AttachDisk(vmName, storageCtrl, originalDisk)
		log.LogOperationError("CreateMultiAttachDisk", "convert-disk", err)
		http.Error(w, fmt.Sprintf("Error convirtiendo disco: %v", err), http.StatusInternalServerError)
		return
	}
	log.LogOperationStep("Convertir disco a multiattach", time.Since(stepStart))

	// Reconectar el disco a la VM
	stepStart = time.Now()
	if err := vboxmanage.AttachDisk(vmName, storageCtrl, originalDisk); err != nil {
		log.LogOperationError("CreateMultiAttachDisk", "reattach-disk", err)
		http.Error(w, fmt.Sprintf("Error reconectando disco: %v", err), http.StatusInternalServerError)
		return
	}
	log.LogOperationStep("Reconectar disco a VM", time.Since(stepStart))

	// Obtener UUID del disco
	stepStart = time.Now()
	diskUUID, err := vboxmanage.GetDiskUUID(originalDisk)
	if err != nil {
		log.LogOperationError("CreateMultiAttachDisk", "get-disk-uuid", err)
		http.Error(w, fmt.Sprintf("Error obteniendo UUID: %v", err), http.StatusInternalServerError)
		return
	}
	log.LogOperationStep("Obtener UUID del disco", time.Since(stepStart))
	log.Info("UUID del disco: %s", diskUUID)

	// Actualizar estado
	baseVM.DiskCreated = true
	baseVM.DiskConverted = true
	baseVM.DiskPath = originalDisk
	baseVM.DiskUUID = diskUUID

	diskName := fmt.Sprintf("%s_multiattach", vmName)
	state.Disks = append(state.Disks, Disk{
		Name:      diskName,
		SourceVM:  vmName,
		Path:      originalDisk,
		UUID:      diskUUID,
		Connected: false,
	})

	saveStateFn()

	log.LogOperationComplete("CreateMultiAttachDisk", time.Since(start),
		fmt.Sprintf("Disco: %s, UUID: %s, Path: %s", diskName, diskUUID, originalDisk))
	log.SetOperationID("")

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":   "ok",
		"diskPath": originalDisk,
		"diskUUID": diskUUID,
	})
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
	log := logger.Get()
	log.Info("Conectando disco: %s", diskName)

	state.Mu.Lock()
	defer state.Mu.Unlock()

	for i := range state.Disks {
		if state.Disks[i].Name == diskName {
			state.Disks[i].Connected = true
			saveStateFn()
			log.Info("Disco conectado exitosamente: %s", diskName)
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}
	}

	log.Error("Disco no encontrado: %s", diskName)
	http.Error(w, "Disco no encontrado", http.StatusNotFound)
}

func DisconnectDisk(w http.ResponseWriter, r *http.Request, diskName string) {
	log := logger.Get()
	log.Info("Desconectando disco: %s", diskName)

	state.Mu.Lock()
	defer state.Mu.Unlock()

	for i := range state.Disks {
		if state.Disks[i].Name == diskName {
			state.Disks[i].Connected = false
			saveStateFn()
			log.Info("Disco desconectado exitosamente: %s", diskName)
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}
	}

	log.Error("Disco no encontrado: %s", diskName)
	http.Error(w, "Disco no encontrado", http.StatusNotFound)
}

func DeleteDisk(w http.ResponseWriter, r *http.Request, diskName string) {
	opID := fmt.Sprintf("delete-disk-%s-%d", diskName, time.Now().Unix())
	log := logger.Get()
	log.SetOperationID(opID)

	start := time.Now()
	log.LogOperation("DeleteDisk", diskName, "system")

	state.Mu.Lock()
	defer state.Mu.Unlock()

	for i, disk := range state.Disks {
		if disk.Name == diskName {
			// Eliminar disco de VirtualBox
			if err := vboxmanage.DeleteDisk(disk.Path); err != nil {
				log.LogOperationError("DeleteDisk", "delete-from-vbox", err)
				http.Error(w, fmt.Sprintf("Error eliminando disco: %v", err), http.StatusInternalServerError)
				return
			}

			// Remover del estado
			state.Disks = append(state.Disks[:i], state.Disks[i+1:]...)
			saveStateFn()

			log.LogOperationComplete("DeleteDisk", time.Since(start), fmt.Sprintf("Disco: %s", diskName))
			log.SetOperationID("")

			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}
	}

	err := fmt.Errorf("disco no encontrado")
	log.LogOperationError("DeleteDisk", "find-disk", err)
	log.SetOperationID("")
	http.Error(w, "Disco no encontrado", http.StatusNotFound)
}
