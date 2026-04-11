package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
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

	if baseVM.DiskConverted {
		err := fmt.Errorf("el disco de esta VM base ya fue preparado")
		log.LogOperationError("CreateMultiAttachDisk", "already-converted", err)
		http.Error(w, "Esta VM base ya tiene un disco multiconexión preparado", http.StatusConflict)
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

	vmState := strings.ToLower(info["VMState"])
	if vmState != "poweroff" {
		err := fmt.Errorf("estado actual de VM base: %s", vmState)
		log.LogOperationError("CreateMultiAttachDisk", "validate-vm-poweroff", err)
		http.Error(w, "La VM base debe estar completamente apagada (poweroff) para clonar su disco. Si está en estado saved/restoring/running, apágala desde VirtualBox e intenta de nuevo.", http.StatusBadRequest)
		return
	}

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

	cloneSource := originalDisk
	if strings.Contains(strings.ToLower(diskType), "differencing") {
		stepStart = time.Now()
		baseDiskPath, resolveErr := vboxmanage.ResolveBaseDiskPath(originalDisk)
		if resolveErr != nil {
			log.LogOperationError("CreateMultiAttachDisk", "resolve-base-disk", resolveErr)
			http.Error(w, fmt.Sprintf("Error resolviendo disco base desde snapshot: %v", resolveErr), http.StatusInternalServerError)
			return
		}
		cloneSource = baseDiskPath
		log.LogOperationStep("Resolver disco base desde cadena de snapshots", time.Since(stepStart))
		log.Info("Disco fuente para clonación: %s", cloneSource)
	}

	// Req 6: Verificar si ya es multiattach
	if strings.Contains(strings.ToLower(diskType), "multiattach") {
		err := fmt.Errorf("el disco ya es de tipo multiconexión")
		log.LogOperationError("CreateMultiAttachDisk", "disk-already-multiattach", err)
		http.Error(w, "El disco ya fue convertido a multiconexión", http.StatusConflict)
		return
	}

	// Clonar el disco original para no modificar el disco base de la VM.
	stepStart = time.Now()
	if err := os.MkdirAll("disks", 0755); err != nil {
		log.LogOperationError("CreateMultiAttachDisk", "mkdir-disks", err)
		http.Error(w, fmt.Sprintf("Error preparando carpeta de discos: %v", err), http.StatusInternalServerError)
		return
	}

	clonedDiskName := fmt.Sprintf("%s_multiattach.vdi", vmName)
	clonedDiskPath := filepath.Join("disks", clonedDiskName)
	if absPath, absErr := filepath.Abs(clonedDiskPath); absErr == nil {
		clonedDiskPath = absPath
	}
	cloneReused := false

	if _, err := os.Stat(clonedDiskPath); err == nil {
		cloneReused = true
		log.Info("Disco clonado ya existe, se reutilizará: %s", clonedDiskPath)
	} else {
		if !os.IsNotExist(err) {
			log.LogOperationError("CreateMultiAttachDisk", "check-clone-file", err)
			http.Error(w, fmt.Sprintf("Error verificando disco clonado: %v", err), http.StatusInternalServerError)
			return
		}

		if err := vboxmanage.CloneDisk(cloneSource, clonedDiskPath); err != nil {
			log.LogOperationError("CreateMultiAttachDisk", "clone-disk", err)
			http.Error(w, fmt.Sprintf("Error clonando disco: %v", err), http.StatusInternalServerError)
			return
		}
	}
	if cloneReused {
		log.LogOperationStep("Reutilizar disco clonado existente", time.Since(stepStart))
	} else {
		log.LogOperationStep("Clonar disco a multiattach", time.Since(stepStart))
	}

	// Validar tipo de disco clonado y forzar multiattach si la variante no quedó correcta.
	stepStart = time.Now()
	clonedType, err := vboxmanage.GetDiskType(clonedDiskPath)
	if err != nil {
		log.LogOperationError("CreateMultiAttachDisk", "get-cloned-disk-type", err)
		http.Error(w, fmt.Sprintf("Error validando tipo del disco clonado: %v", err), http.StatusInternalServerError)
		return
	}
	if !strings.Contains(strings.ToLower(clonedType), "multiattach") {
		if err := vboxmanage.ConvertDiskToMultiAttach(clonedDiskPath); err != nil {
			errText := strings.ToLower(err.Error())
			if strings.Contains(errText, "can only be used on media registered with a machine that was created with virtualbox 4.0 or later") ||
				strings.Contains(errText, "vbox_e_invalid_object_state") {
				log.Warn("VBox no permitió cambiar tipo del medio a multiattach; se continuará con adjunto en modo multiattach por VM. Error: %v", err)
			} else {
				log.LogOperationError("CreateMultiAttachDisk", "convert-cloned-disk", err)
				http.Error(w, fmt.Sprintf("Error convirtiendo disco clonado a multiattach: %v", err), http.StatusInternalServerError)
				return
			}
		}
	}
	log.LogOperationStep("Validar/convertir tipo de disco clonado", time.Since(stepStart))

	// Obtener UUID del disco clonado
	stepStart = time.Now()
	diskUUID, err := vboxmanage.GetDiskUUID(clonedDiskPath)
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
	baseVM.DiskPath = clonedDiskPath
	baseVM.DiskUUID = diskUUID

	diskName := fmt.Sprintf("%s_multiattach", vmName)
	state.Disks = append(state.Disks, Disk{
		Name:      diskName,
		SourceVM:  vmName,
		Path:      clonedDiskPath,
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
		"diskPath": clonedDiskPath,
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
