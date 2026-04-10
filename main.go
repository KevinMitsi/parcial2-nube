package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
	"vbox-platform/handlers"
	"vbox-platform/logger"
	"vbox-platform/vboxmanage"
)

const (
	AppVersion   = "1.0.0"
	LogDir       = "logs"
	MaxLogSizeMB = 50
)

var appState *handlers.State

func main() {
	// Req 1: Inicializar sistema de logging
	if err := logger.Init(LogDir, MaxLogSizeMB); err != nil {
		log.Fatalf("Error inicializando logger: %v", err)
	}
	defer logger.Get().Close()

	log := logger.Get()

	// Req 1: Registrar inicio de aplicación con versión y timestamp
	log.Info("=== Iniciando Plataforma VirtualBox ===")
	log.Info("Versión: %s", AppVersion)
	log.Info("Timestamp: %s", time.Now().Format("2006-01-02 15:04:05"))

	appState = &handlers.State{
		BaseVMs:       []handlers.BaseVM{},
		Disks:         []handlers.Disk{},
		UserVMs:       []handlers.UserVM{},
		BridgeAdapter: "Ethernet", // Default, puede cambiarse
	}

	// Req 13: Cargar estado desde archivo
	loadState()

	// Crear carpeta para llaves
	if err := os.MkdirAll("keys", 0755); err != nil {
		log.Error("Error creando directorio de llaves: %v", err)
	}

	// Inicializar handlers
	handlers.InitHandlers(appState, saveState)
	vboxmanage.Init()

	// Rutas
	http.HandleFunc("/", serveIndex)
	http.HandleFunc("/api/state", getState)
	http.HandleFunc("/api/vboxvms", getVBoxVMs)
	http.HandleFunc("/api/basevms", handleBaseVMs)
	http.HandleFunc("/api/basevms/", handleBaseVMActions)
	http.HandleFunc("/api/disks/", handleDiskActions)
	http.HandleFunc("/api/uservms/", handleUserVMActions)

	// Archivos estáticos
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	log.Info("Servidor iniciado en http://0.0.0.0:8000")
	fmt.Println("Servidor iniciado en http://0.0.0.0:8000")

	if err := http.ListenAndServe("0.0.0.0:8000", nil); err != nil {
		log.Error("Error iniciando servidor: %v", err)
		log.Error(err.Error())
	}
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "templates/index.html")
}

func getState(w http.ResponseWriter, r *http.Request) {
	appState.Mu.RLock()
	defer appState.Mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(appState)
}

func handleBaseVMs(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		handlers.AddBaseVM(w, r)
	} else {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleBaseVMActions(w http.ResponseWriter, r *http.Request) {
	handlers.HandleBaseVMActions(w, r)
}

func handleDiskActions(w http.ResponseWriter, r *http.Request) {
	handlers.HandleDiskActions(w, r)
}

func handleUserVMActions(w http.ResponseWriter, r *http.Request) {
	handlers.HandleUserVMActions(w, r)
}

func getVBoxVMs(w http.ResponseWriter, r *http.Request) {
	log := logger.Get()
	log.Debug("Listando VMs de VirtualBox")

	vms, err := vboxmanage.ListVMs()
	if err != nil {
		log.Error("Error listando VMs: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	log.Debug("VMs encontradas: %d", len(vms))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(vms)
}

// Req 13: Cargar estado desde archivo
func loadState() {
	log := logger.Get()
	log.Info("Cargando estado desde state.json")

	data, err := os.ReadFile("state.json")
	if err != nil {
		if os.IsNotExist(err) {
			log.Info("state.json no existe, iniciando con estado vacío")
		} else {
			log.Error("Error leyendo state.json: %v", err)
		}
		return
	}

	if err := json.Unmarshal(data, appState); err != nil {
		log.Error("Error deserializando state.json: %v", err)
		log.Warn("Continuando con estado vacío")
		return
	}

	log.Info("Estado cargado exitosamente: %d VMs base, %d discos, %d VMs de usuario",
		len(appState.BaseVMs), len(appState.Disks), len(appState.UserVMs))
}

// Req 13: Guardar estado en archivo
// IMPORTANTE: Esta función debe ser llamada DENTRO de un lock (Lock o RLock)
func saveState() {
	log := logger.Get()

	// NO usar locks aquí porque ya estamos dentro de un lock cuando se llama
	data, err := json.MarshalIndent(appState, "", "  ")
	if err != nil {
		log.Error("Error serializando estado: %v", err)
		return
	}

	if err := os.WriteFile("state.json", data, 0644); err != nil {
		log.Error("Error guardando state.json: %v", err)
		return
	}

	log.Debug("Estado guardado exitosamente en state.json")
}
