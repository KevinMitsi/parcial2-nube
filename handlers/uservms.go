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
	"time"
	"vbox-platform/logger"
	"vbox-platform/vboxmanage"
)

type UserVM struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	SourceVM    string `json:"sourceVM"`
	DiskPath    string `json:"diskPath"`
	DiskUUID    string `json:"diskUUID"`
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
	opID := fmt.Sprintf("create-uservm-%s-%d", diskName, time.Now().Unix())
	log := logger.Get()
	log.SetOperationID(opID)

	start := time.Now()
	log.LogOperation("CreateUserVM", diskName, "system")

	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.LogOperationError("CreateUserVM", "decode-request", err)
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
		err := fmt.Errorf("disco no encontrado")
		log.LogOperationError("CreateUserVM", "find-disk", err)
		http.Error(w, "Disco no encontrado", http.StatusNotFound)
		return
	}

	// Req 14: Validar que el disco es usable para VMs hijas
	stepStart := time.Now()
	diskType, err := vboxmanage.GetDiskType(disk.Path)
	if err != nil {
		log.LogOperationError("CreateUserVM", "get-disk-type", err)
		http.Error(w, fmt.Sprintf("Error verificando tipo de disco: %v", err), http.StatusInternalServerError)
		return
	}

	diskTypeLower := strings.ToLower(diskType)
	if !strings.Contains(diskTypeLower, "multiattach") && !strings.Contains(diskTypeLower, "immutable") && !strings.Contains(diskTypeLower, "normal") {
		err := fmt.Errorf("el disco debe ser de tipo multiattach, immutable o normal compatible, actual: %s", diskType)
		log.LogOperationError("CreateUserVM", "validate-disk-type", err)
		http.Error(w, "El disco no tiene un tipo compatible para crear VMs hijas", http.StatusBadRequest)
		return
	}
	log.LogOperationStep("Validar tipo de disco", time.Since(stepStart))

	vmName := fmt.Sprintf("UserVM_%s", req.Name)
	log.Info("Creando VM de usuario: %s", vmName)

	// Verificar si la VM ya existe en VirtualBox
	stepStart = time.Now()
	vmExists, err := vboxmanage.VMExists(vmName)
	if err != nil {
		log.LogOperationError("CreateUserVM", "check-vm-exists", err)
		http.Error(w, fmt.Sprintf("Error verificando VM: %v", err), http.StatusInternalServerError)
		return
	}

	if vmExists {
		err := fmt.Errorf("ya existe una VM con el nombre %s", vmName)
		log.LogOperationError("CreateUserVM", "vm-already-exists", err)
		http.Error(w, fmt.Sprintf("Ya existe una VM con el nombre '%s'. Usa otro nombre o elimina la VM existente.", req.Name), http.StatusConflict)
		return
	}
	log.LogOperationStep("Verificar VM no existe", time.Since(stepStart))

	// Req 8: Crear VM
	stepStart = time.Now()
	if err := vboxmanage.CreateVM(vmName, "Debian_64"); err != nil {
		log.LogOperationError("CreateUserVM", "create-vm", err)
		http.Error(w, fmt.Sprintf("Error creando VM: %v", err), http.StatusInternalServerError)
		return
	}
	log.LogOperationStep("Crear VM", time.Since(stepStart))

	// Req 8: Configurar VM con 1024 MB y adaptador bridge
	stepStart = time.Now()

	// Obtener el adaptador de red de la VM base
	var baseVM *BaseVM
	for i := range state.BaseVMs {
		if state.BaseVMs[i].Name == disk.SourceVM {
			baseVM = &state.BaseVMs[i]
			break
		}
	}

	// Obtener info de la VM base para extraer el adaptador de red
	bridgeAdapter := state.BridgeAdapter // fallback al default
	if baseVM != nil {
		baseInfo, err := vboxmanage.GetVMInfo(baseVM.Name)
		if err == nil {
			if adapter := baseInfo["bridgeadapter1"]; adapter != "" {
				bridgeAdapter = adapter
				log.Info("Usando adaptador de red de VM base: %s", bridgeAdapter)
			}
		}
	}

	if err := vboxmanage.ModifyVM(vmName, "--memory", "1024", "--nic1", "bridged", "--bridgeadapter1", bridgeAdapter); err != nil {
		log.LogOperationError("CreateUserVM", "configure-vm", err)
		http.Error(w, fmt.Sprintf("Error configurando VM: %v", err), http.StatusInternalServerError)
		return
	}
	log.LogOperationStep("Configurar VM", time.Since(stepStart))

	// Req 8: Agregar controlador SATA
	stepStart = time.Now()
	if err := vboxmanage.AddStorageController(vmName, "SATA"); err != nil {
		log.LogOperationError("CreateUserVM", "add-storage-controller", err)
		http.Error(w, fmt.Sprintf("Error agregando controlador: %v", err), http.StatusInternalServerError)
		return
	}
	log.LogOperationStep("Agregar controlador SATA", time.Since(stepStart))

	// Req 8: Conectar disco usando UUID
	stepStart = time.Now()
	if err := vboxmanage.AttachDiskByUUID(vmName, "SATA", disk.UUID); err != nil {
		log.LogOperationError("CreateUserVM", "attach-disk", err)
		http.Error(w, fmt.Sprintf("Error conectando disco: %v", err), http.StatusInternalServerError)
		return
	}
	log.LogOperationStep("Conectar disco multiattach", time.Since(stepStart))

	// Req 8: Iniciar VM en modo headless
	stepStart = time.Now()
	if err := vboxmanage.StartVM(vmName); err != nil {
		log.LogOperationError("CreateUserVM", "start-vm", err)
		http.Error(w, fmt.Sprintf("Error iniciando VM: %v", err), http.StatusInternalServerError)
		return
	}
	log.LogOperationStep("Iniciar VM", time.Since(stepStart))

	// Esperar a que la VM obtenga IP con ciclo inteligente
	stepStart = time.Now()
	var vmIP string
	maxIPRetries := 60              // 60 intentos
	ipRetryDelay := 5 * time.Second // 5 segundos entre intentos (total: 5 minutos máximo)

	log.Info("Esperando a que la VM %s obtenga una IP (máximo %d intentos de %v)...", vmName, maxIPRetries, ipRetryDelay)

	for i := 0; i < maxIPRetries; i++ {
		vmIP, err = vboxmanage.GetVMIP(vmName)
		if err == nil && vmIP != "" {
			log.Info("VM obtuvo IP: %s (intento %d/%d)", vmIP, i+1, maxIPRetries)
			break
		}

		if i < maxIPRetries-1 {
			log.Debug("Intento %d/%d: IP no disponible aún, esperando %v...", i+1, maxIPRetries, ipRetryDelay)
			time.Sleep(ipRetryDelay)
		}
	}

	if vmIP == "" {
		log.Warn("No se pudo obtener IP después de %d intentos (%v), continuando de todos modos...", maxIPRetries, time.Since(stepStart))
	} else {
		log.LogOperationStep("Obtener IP de VM", time.Since(stepStart))
	}

	// Bootstrap en VM hija: instalar llave root de la VM base para habilitar SSH por clave.
	stepStart = time.Now()

	// Reutilizar baseVM ya buscada anteriormente para obtener las credenciales guardadas
	// (baseVM ya fue declarada arriba para obtener el adaptador de red)

	// Usar credenciales guardadas de la VM base, o fallback a valores por defecto
	bootstrapUser := "mary"
	bootstrapPass := "mary"
	if baseVM != nil && baseVM.GuestUsername != "" && baseVM.GuestPassword != "" {
		bootstrapUser = baseVM.GuestUsername
		bootstrapPass = baseVM.GuestPassword
		log.Info("Usando credenciales guardadas de VM base: usuario=%s", bootstrapUser)
	} else {
		log.Warn("No se encontraron credenciales guardadas para VM base %s, usando valores por defecto (mary/mary)", disk.SourceVM)
	}

	if err := InstallRootPublicKeyInGuestWithRetries(vmName, disk.SourceVM, bootstrapUser, bootstrapPass, 40, 2*time.Second); err != nil {
		log.LogOperationError("CreateUserVM", "bootstrap-root-key", err)
		// No fallar aquí - la VM está creada, solo le falta la llave root
		log.Warn("Bootstrap falló pero continuando: %v. Intenta instalar root keys manualmente.", err)
	}
	log.LogOperationStep("Bootstrap llave root en VM hija", time.Since(stepStart))

	disk.Connected = true

	state.UserVMs = append(state.UserVMs, UserVM{
		Name:        vmName,
		Description: req.Description,
		SourceVM:    disk.SourceVM,
		DiskPath:    disk.Path,
		DiskUUID:    disk.UUID,
		State:       "running",
	})

	saveStateFn()

	log.LogOperationComplete("CreateUserVM", time.Since(start),
		fmt.Sprintf("VM: %s, Disco: %s", vmName, diskName))
	log.SetOperationID("")

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
	opID := fmt.Sprintf("create-user-%s-%d", vmName, time.Now().Unix())
	log := logger.Get()
	log.SetOperationID(opID)

	start := time.Now()
	log.LogOperation("CreateUser", vmName, "system")

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.LogOperationError("CreateUser", "decode-request", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.Password == "" {
		err := fmt.Errorf("password es requerido")
		log.LogOperationError("CreateUser", "validate-password", err)
		http.Error(w, "Debes proporcionar una contraseña para el usuario", http.StatusBadRequest)
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
		err := fmt.Errorf("VM no encontrada")
		log.LogOperationError("CreateUser", "find-vm", err)
		http.Error(w, "VM no encontrada", http.StatusNotFound)
		return
	}

	// Req 9: Verificar que la VM está encendida
	stepStart := time.Now()
	running, err := vboxmanage.IsVMRunning(vmName)
	if err != nil || !running {
		err := fmt.Errorf("la VM debe estar encendida")
		log.LogOperationError("CreateUser", "check-vm-running", err)
		http.Error(w, "VM debe estar encendida", http.StatusBadRequest)
		return
	}
	log.LogOperationStep("Verificar VM encendida", time.Since(stepStart))

	// Req 9: Obtener IP de la VM (con reintentos)
	stepStart = time.Now()
	var ip string
	maxRetries := 30              // 30 intentos
	retryDelay := 5 * time.Second // 5 segundos entre intentos

	log.Info("Esperando a que la VM obtenga una IP (esto puede tomar 1-2 minutos)...")

	for i := 0; i < maxRetries; i++ {
		ip, err = vboxmanage.GetVMIP(vmName)
		if err == nil && ip != "" {
			break
		}

		if i < maxRetries-1 {
			log.Debug("Intento %d/%d: IP no disponible aún, esperando %v...", i+1, maxRetries, retryDelay)
			time.Sleep(retryDelay)
		}
	}

	if ip == "" {
		err := fmt.Errorf("no se pudo obtener IP después de %d intentos (%v)", maxRetries, time.Since(stepStart))
		log.LogOperationError("CreateUser", "get-vm-ip", err)
		http.Error(w, "No se pudo obtener IP de la VM. Asegúrate de que la VM tenga VirtualBox Guest Additions instalado y que la red esté configurada correctamente.", http.StatusBadRequest)
		return
	}
	log.LogOperationStep("Obtener IP de VM", time.Since(stepStart))
	log.Info("IP de la VM: %s", ip)

	userVM.IP = ip

	// Req 9: Generar llaves RSA 1024 para el usuario
	stepStart = time.Now()
	keyDir := filepath.Join("keys", vmName, req.Username)
	if err := os.MkdirAll(keyDir, 0755); err != nil {
		log.LogOperationError("CreateUser", "create-key-dir", err)
		http.Error(w, fmt.Sprintf("Error creando directorio de llaves: %v", err), http.StatusInternalServerError)
		return
	}

	keyPath := filepath.Join(keyDir, "id_rsa")
	pubKeyPath := keyPath + ".pub"

	// Si las llaves ya existen de un intento previo, reutilizarlas.
	if _, errPriv := os.Stat(keyPath); errPriv == nil {
		if _, errPub := os.Stat(pubKeyPath); errPub == nil {
			log.Info("Llaves de usuario ya existentes en: %s", keyDir)
			log.LogOperationStep("Reutilizar llaves SSH existentes", time.Since(stepStart))
		} else {
			cmd := exec.Command("ssh-keygen", "-t", "rsa", "-b", "1024", "-f", keyPath, "-N", "", "-C", fmt.Sprintf("%s@%s", req.Username, vmName))
			out, err := cmd.CombinedOutput()
			if err != nil {
				log.LogOperationError("CreateUser", "generate-keys", err)
				http.Error(w, fmt.Sprintf("Error generando llaves: %v | output: %s", err, string(out)), http.StatusInternalServerError)
				return
			}
			log.LogOperationStep("Generar llaves SSH", time.Since(stepStart))
		}
	} else {
		cmd := exec.Command("ssh-keygen", "-t", "rsa", "-b", "1024", "-f", keyPath, "-N", "", "-C", fmt.Sprintf("%s@%s", req.Username, vmName))
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.LogOperationError("CreateUser", "generate-keys", err)
			http.Error(w, fmt.Sprintf("Error generando llaves: %v | output: %s", err, string(out)), http.StatusInternalServerError)
			return
		}
		log.LogOperationStep("Generar llaves SSH", time.Since(stepStart))
	}

	// Leer llave pública
	pubKeyData, _ := os.ReadFile(keyPath + ".pub")
	pubKey := string(pubKeyData)

	// Obtener llave root de la VM base
	rootKeyPath := filepath.Join("keys", userVM.SourceVM, "root", "id_rsa")

	// Req 9: Crear usuario en la VM con SSH
	stepStart = time.Now()
	if err := CreateUserInVM(ip, rootKeyPath, req.Username, req.Password, pubKey); err != nil {
		log.LogOperationError("CreateUser", "create-user-in-vm", err)
		http.Error(w, fmt.Sprintf("Error creando usuario en VM: %v", err), http.StatusInternalServerError)
		return
	}
	log.LogOperationStep("Crear usuario en VM", time.Since(stepStart))

	userVM.Username = req.Username
	userVM.HasUserKeys = true
	saveStateFn()

	log.LogOperationComplete("CreateUser", time.Since(start),
		fmt.Sprintf("Usuario: %s, VM: %s", req.Username, vmName))
	log.SetOperationID("")

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func DownloadUserKeys(w http.ResponseWriter, r *http.Request, vmName string) {
	log := logger.Get()
	log.Info("Descargando llaves de usuario para VM: %s", vmName)

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
		log.Error("Usuario no encontrado para VM: %s", vmName)
		http.Error(w, "Usuario no encontrado", http.StatusNotFound)
		return
	}

	keyDir := filepath.Join("keys", vmName, userVM.Username)
	privateKey := filepath.Join(keyDir, "id_rsa")
	publicKey := filepath.Join(keyDir, "id_rsa.pub")

	// Req 4: Verificar que las llaves existen
	if _, err := os.Stat(privateKey); os.IsNotExist(err) {
		log.Error("Llaves no encontradas en: %s", keyDir)
		http.Error(w, "Llaves no encontradas", http.StatusNotFound)
		return
	}

	// Crear ZIP
	buf := new(bytes.Buffer)
	zipWriter := zip.NewWriter(buf)

	privData, _ := os.ReadFile(privateKey)
	privFile, _ := zipWriter.Create("id_rsa")
	privFile.Write(privData)

	pubData, _ := os.ReadFile(publicKey)
	pubFile, _ := zipWriter.Create("id_rsa.pub")
	pubFile.Write(pubData)

	instructionsContent := fmt.Sprintf("=== Instrucciones de acceso SSH ===\n\nUsuario: %s\nVM: %s\nIP: %s\n\nPasos para probar la llave en Windows (PowerShell):\n\n1. Extrae este ZIP en una carpeta, por ejemplo:\n   C:\\Users\\Mary\\Downloads\\%s_%s_keys\n\n2. Abre PowerShell dentro de ESA carpeta (donde están id_rsa e id_rsa.pub).\n\n3. Verifica que el archivo privado exista:\n   dir .\\id_rsa\n\n4. Conéctate por SSH usando la llave:\n   ssh -o StrictHostKeyChecking=accept-new -i .\\id_rsa %s@%s\n\n5. Si te pide password, la llave no se usó. Ejecuta modo verbose para diagnóstico:\n   ssh -vvv -i .\\id_rsa %s@%s\n\nResultado esperado:\n- Debe abrir sesión sin pedir password y ver un prompt como: %s@...:~$\n\nComandos útiles una vez dentro de la VM por SSH:\n- whoami                     (debe mostrar %s)\n- hostname -I                (muestra IP asignada)\n- pwd                        (directorio actual)\n- ls -la ~/.ssh              (valida que existe .ssh del usuario)\n- cat ~/.ssh/authorized_keys (verifica llave pública instalada)\n- exit                       (cerrar sesión SSH)",
		userVM.Username,
		userVM.Name,
		userVM.IP,
		userVM.Name,
		userVM.Username,
		userVM.Username,
		userVM.IP,
		userVM.Username,
		userVM.IP,
		userVM.Username,
		userVM.Username,
	)
	instructionsFile, _ := zipWriter.Create("instrucciones.txt")
	instructionsFile.Write([]byte(instructionsContent))

	zipWriter.Close()

	// Req 4: Enviar ZIP con nombre específico
	zipName := fmt.Sprintf("%s_%s_keys.zip", vmName, userVM.Username)
	log.Info("Enviando archivo: %s", zipName)

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", zipName))
	w.Write(buf.Bytes())
}

func DeleteUserVM(w http.ResponseWriter, r *http.Request, vmName string) {
	opID := fmt.Sprintf("delete-uservm-%s-%d", vmName, time.Now().Unix())
	log := logger.Get()
	log.SetOperationID(opID)

	start := time.Now()
	log.LogOperation("DeleteUserVM", vmName, "system")

	state.Mu.Lock()
	defer state.Mu.Unlock()

	for i, vm := range state.UserVMs {
		if vm.Name == vmName {
			// Apagar VM si está encendida
			stepStart := time.Now()
			vboxmanage.PowerOffVM(vmName)
			log.LogOperationStep("Apagar VM", time.Since(stepStart))

			// Desregistrar VM
			stepStart = time.Now()
			if err := vboxmanage.UnregisterVM(vmName); err != nil {
				log.LogOperationError("DeleteUserVM", "unregister-vm", err)
				http.Error(w, fmt.Sprintf("Error eliminando VM: %v", err), http.StatusInternalServerError)
				return
			}
			log.LogOperationStep("Desregistrar VM", time.Since(stepStart))

			// Req 14: Actualizar estado del disco (mantener disponible)
			for j := range state.Disks {
				if state.Disks[j].UUID == vm.DiskUUID {
					state.Disks[j].Connected = false
					log.Info("Disco %s marcado como disponible", state.Disks[j].Name)
					break
				}
			}

			// Remover del estado
			state.UserVMs = append(state.UserVMs[:i], state.UserVMs[i+1:]...)
			saveStateFn()

			log.LogOperationComplete("DeleteUserVM", time.Since(start), fmt.Sprintf("VM: %s", vmName))
			log.SetOperationID("")

			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}
	}

	err := fmt.Errorf("VM no encontrada")
	log.LogOperationError("DeleteUserVM", "find-vm", err)
	log.SetOperationID("")
	http.Error(w, "VM no encontrada", http.StatusNotFound)
}
