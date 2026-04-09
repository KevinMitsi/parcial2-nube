package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"vbox-platform/handlers"
	"vbox-platform/vboxmanage"
)

var appState *handlers.State

func main() {
	appState = &handlers.State{
		BaseVMs:       []handlers.BaseVM{},
		Disks:         []handlers.Disk{},
		UserVMs:       []handlers.UserVM{},
		BridgeAdapter: "Ethernet", // Default, puede cambiarse
	}

	// Cargar estado desde archivo
	loadState()

	// Crear carpeta para llaves
	os.MkdirAll("keys", 0755)

	// Inicializar handlers
	handlers.InitHandlers(appState, saveState)
	vboxmanage.Init()

	// Rutas
	http.HandleFunc("/", serveIndex)
	http.HandleFunc("/api/state", getState)
	http.HandleFunc("/api/basevms", handleBaseVMs)
	http.HandleFunc("/api/basevms/", handleBaseVMActions)
	http.HandleFunc("/api/disks/", handleDiskActions)
	http.HandleFunc("/api/uservms/", handleUserVMActions)

	// Archivos estáticos
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	log.Println("Servidor iniciado en http://0.0.0.0:8080")
	log.Fatal(http.ListenAndServe("0.0.0.0:8080", nil))
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

func loadState() {
	data, err := os.ReadFile("state.json")
	if err != nil {
		log.Println("No se encontró state.json, iniciando con estado vacío")
		return
	}

	if err := json.Unmarshal(data, appState); err != nil {
		log.Printf("Error al cargar state.json: %v\n", err)
	}
}

func saveState() {
	appState.Mu.RLock()
	defer appState.Mu.RUnlock()

	data, err := json.Marshal(appState)
	if err != nil {
		log.Printf("Error al serializar estado: %v\n", err)
		return
	}

	if err := os.WriteFile("state.json", data, 0644); err != nil {
		log.Printf("Error al guardar state.json: %v\n", err)
	}
}
