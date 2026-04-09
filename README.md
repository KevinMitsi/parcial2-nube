# Plataforma de Gestión de VMs VirtualBox

Plataforma web en Golang para gestionar máquinas virtuales de VirtualBox automáticamente en Windows.

## Requisitos

- Windows con PowerShell
- VirtualBox instalado con VBoxManage en el PATH
- Go 1.21 o superior
- SSH disponible en el sistema (incluido en Windows 10+)
- VMs base (VM1, VM2) configuradas con Debian 13 y SSH server

## Instalación

1. Compilar el proyecto:
```bash
go build -o plataforma.exe main.go
```

2. Ejecutar:
```bash
./plataforma.exe
```

3. Abrir navegador en: http://localhost:8080

## Estructura del Proyecto

```
/
├── main.go              # Servidor HTTP principal
├── handlers/
│   ├── basevms.go      # Gestión de VMs base
│   ├── disks.go        # Gestión de discos multiconexión
│   ├── uservms.go      # Gestión de VMs de usuario
│   └── ssh.go          # Gestión de llaves SSH
├── vboxmanage/
│   └── vbox.go         # Wrapper para VBoxManage
├── templates/
│   └── index.html      # Dashboard web
├── static/
│   └── style.css       # Estilos
├── keys/               # Llaves SSH generadas
└── state.json          # Estado persistente
```

## Flujo de Uso

1. **Agregar VM Base**: Registrar VM1 o VM2 en el sistema
2. **Crear llaves de root**: Generar par de llaves RSA 1024 para acceso root
3. **Crear disco multiconexión**: Clonar disco de la VM base
4. **Crear VM de usuario**: Crear nueva VM usando el disco multiconexión
5. **Crear usuario**: Generar usuario con llaves SSH en la VM
6. **Descargar llaves**: Obtener llaves para conectarse por SSH

## API Endpoints

- `GET /` - Dashboard
- `GET /api/state` - Estado completo
- `POST /api/basevms` - Agregar VM base
- `POST /api/basevms/:name/rootkeys` - Crear llaves root
- `GET /api/basevms/:name/rootkeys/download` - Descargar llaves root
- `POST /api/basevms/:name/disk` - Crear disco multiconexión
- `POST /api/disks/:name/uservms` - Crear VM de usuario
- `POST /api/disks/:name/connect` - Conectar disco
- `POST /api/disks/:name/disconnect` - Desconectar disco
- `DELETE /api/disks/:name` - Eliminar disco
- `POST /api/uservms/:name/user` - Crear usuario
- `GET /api/uservms/:name/keys/download` - Descargar llaves usuario
- `DELETE /api/uservms/:name` - Eliminar VM usuario

## Configuración

El adaptador de red puente se configura en `state.json`:

```json
{
  "bridgeAdapter": "Ethernet"
}
```

## Notas Importantes

- Las VMs base deben estar apagadas antes de clonar discos
- Las llaves RSA usan 1024 bits como especificado
- El sistema hace polling automático cada 5 segundos
- Las operaciones largas devuelven respuesta inmediata
