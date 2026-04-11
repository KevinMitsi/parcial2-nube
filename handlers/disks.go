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

	if baseVM.DiskConverted {
		diskPathLower := strings.ToLower(baseVM.DiskPath)
		if !strings.Contains(diskPathLower, "\\disks\\") && !strings.Contains(diskPathLower, "/disks/") {
			log.Info("La VM base ya tiene disco preparado directamente desde origen: %s", baseVM.DiskPath)
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":   "already_exists",
				"diskPath": baseVM.DiskPath,
				"diskUUID": baseVM.DiskUUID,
			})
			return
		}

		log.Warn("Se detectó estado antiguo con disco clonado, se migrará a disco padre directo")
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
		log.Info("VM %s está en estado '%s', apagándola antes de convertir disco...", vmName, vmState)

		// Intentar apagar la VM
		stepStart = time.Now()
		if err := vboxmanage.PowerOffVM(vmName); err != nil {
			log.LogOperationError("CreateMultiAttachDisk", "poweroff-vm", err)
			http.Error(w, fmt.Sprintf("No se pudo apagar la VM automáticamente (estado: %s): %v. Apágala manualmente desde VirtualBox e intenta de nuevo.", vmState, err), http.StatusBadRequest)
			return
		}
		log.LogOperationStep("Apagar VM antes de conversión", time.Since(stepStart))

		// Esperar un momento para asegurar que la VM se apagó completamente
		log.Info("Esperando a que la VM se apague completamente...")
		time.Sleep(3 * time.Second)

		// Verificar que efectivamente se apagó
		stepStart = time.Now()
		info, err = vboxmanage.GetVMInfo(vmName)
		if err != nil {
			log.LogOperationError("CreateMultiAttachDisk", "verify-vm-poweroff", err)
			http.Error(w, fmt.Sprintf("Error verificando estado de VM después de apagar: %v", err), http.StatusInternalServerError)
			return
		}
		vmState = strings.ToLower(info["VMState"])
		if vmState != "poweroff" {
			err := fmt.Errorf("VM no se apagó correctamente, estado actual: %s", vmState)
			log.LogOperationError("CreateMultiAttachDisk", "validate-vm-poweroff-after", err)
			http.Error(w, fmt.Sprintf("La VM no se apagó correctamente (estado: %s). Apágala manualmente desde VirtualBox e intenta de nuevo.", vmState), http.StatusBadRequest)
			return
		}
		log.LogOperationStep("Verificar VM apagada", time.Since(stepStart))
		log.Info("VM apagada exitosamente, continuando con conversión de disco...")
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

	targetDiskPath := originalDisk
	if strings.Contains(strings.ToLower(diskType), "differencing") {
		stepStart = time.Now()
		baseDiskPath, resolveErr := vboxmanage.ResolveBaseDiskPath(originalDisk)
		if resolveErr != nil {
			log.LogOperationError("CreateMultiAttachDisk", "resolve-base-disk", resolveErr)
			http.Error(w, fmt.Sprintf("Error resolviendo disco base desde snapshot: %v", resolveErr), http.StatusInternalServerError)
			return
		}
		targetDiskPath = baseDiskPath
		log.LogOperationStep("Resolver disco base desde cadena de snapshots", time.Since(stepStart))
		log.Info("Disco padre para conversión: %s", targetDiskPath)
	}

	// Verificar tipo del disco objetivo (padre) antes de convertir.
	stepStart = time.Now()
	targetDiskType, err := vboxmanage.GetDiskType(targetDiskPath)
	if err != nil {
		log.LogOperationError("CreateMultiAttachDisk", "get-target-disk-type", err)
		http.Error(w, fmt.Sprintf("Error obteniendo tipo del disco padre: %v", err), http.StatusInternalServerError)
		return
	}
	log.LogOperationStep("Verificar tipo de disco padre", time.Since(stepStart))
	log.Info("Tipo de disco padre: %s", targetDiskType)

	targetTypeLower := strings.ToLower(targetDiskType)
	if strings.Contains(targetTypeLower, "immutable") {
		stepStart = time.Now()
		if err := vboxmanage.ConvertDiskType(targetDiskPath, "normal"); err != nil {
			log.LogOperationError("CreateMultiAttachDisk", "convert-disk-to-normal", err)
			http.Error(w, fmt.Sprintf("Error cambiando disco padre a tipo normal: %v", err), http.StatusInternalServerError)
			return
		}
		log.LogOperationStep("Convertir disco padre a normal", time.Since(stepStart))
		targetTypeLower = "normal"
	}

	if !strings.Contains(targetTypeLower, "multiattach") {
		stepStart = time.Now()
		if err := vboxmanage.ConvertDiskToMultiAttach(targetDiskPath); err != nil {
			errText := strings.ToLower(err.Error())
			if strings.Contains(errText, "can only be used on media registered with a machine that was created with virtualbox 4.0 or later") ||
				strings.Contains(errText, "vbox_e_invalid_object_state") {
				log.Warn("VBox no permitió cambiar tipo del disco padre a multiattach; se continuará con adjunto en modo multiattach por VM. Error: %v", err)
			} else {
				log.LogOperationError("CreateMultiAttachDisk", "convert-parent-disk-multiattach", err)
				http.Error(w, fmt.Sprintf("Error convirtiendo disco padre a multiattach: %v", err), http.StatusInternalServerError)
				return
			}
		}
		log.LogOperationStep("Convertir disco padre a multiattach", time.Since(stepStart))
	}

	// Obtener UUID del disco padre (el que compartirán las VMs hijas)
	stepStart = time.Now()
	diskUUID, err := vboxmanage.GetDiskUUID(targetDiskPath)
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
	baseVM.DiskPath = targetDiskPath
	baseVM.DiskUUID = diskUUID

	diskName := fmt.Sprintf("%s_multiattach", vmName)
	for i := range state.Disks {
		if state.Disks[i].Name == diskName {
			state.Disks[i].Path = targetDiskPath
			state.Disks[i].UUID = diskUUID
			state.Disks[i].SourceVM = vmName
			saveStateFn()

			log.LogOperationComplete("CreateMultiAttachDisk", time.Since(start),
				fmt.Sprintf("Disco: %s, UUID: %s, Path: %s", diskName, diskUUID, targetDiskPath))
			log.SetOperationID("")

			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":   "ok",
				"diskPath": targetDiskPath,
				"diskUUID": diskUUID,
			})
			return
		}
	}

	state.Disks = append(state.Disks, Disk{
		Name:      diskName,
		SourceVM:  vmName,
		Path:      targetDiskPath,
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
		"diskPath": targetDiskPath,
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
