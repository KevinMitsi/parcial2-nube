# Plataforma de Gestión de VMs VirtualBox

Sistema completo de gestión de máquinas virtuales VirtualBox con soporte para discos multiconexión, gestión de usuarios SSH y logging detallado.

## Características Principales

### ✅ Sistema de Logging Robusto
- Logging estructurado con niveles (DEBUG, INFO, WARN, ERROR)
- Registro de todas las operaciones con timestamps y duración
- Logs tanto en consola como en archivos rotativos
- Identificadores únicos de operación para rastreo completo
- Stack traces detallados en caso de errores

### ✅ Gestión de VMs Base
- Validación de existencia de VMs en VirtualBox
- Generación de llaves SSH RSA 1024 para root
- Descarga de llaves en formato ZIP
- Conversión de discos a tipo multiconexión (multiattach)

### ✅ Discos Multiconexión
- Conversión de discos existentes a tipo multiattach
- Múltiples VMs pueden compartir el mismo disco simultáneamente
- Visualización de UUID y rutas de discos
- Gestión de estado de conexión

### ✅ VMs de Usuario
- Creación de VMs desde discos multiconexión
- Configuración automática (1024 MB RAM, adaptador bridge)
- Inicio automático en modo headless
- Gestión de usuarios con llaves SSH

### ✅ Gestión de Usuarios SSH
- Creación de usuarios en VMs con llaves SSH
- Generación automática de llaves RSA 1024
- Instalación automática de llaves públicas
- Descarga de llaves en formato ZIP
- Reintentos automáticos con timeout de 3 segundos

### ✅ Interfaz Web Dinámica
- Controles habilitados/deshabilitados según estado
- Notificaciones toast (éxito, error, info)
- Actualización automática cada 5 segundos
- Indicadores de progreso en operaciones

### ✅ Persistencia de Estado
- Guardado automático en state.json
- Carga automática al iniciar
- Formato JSON legible

## Requisitos

- Go 1.21 o superior
- VirtualBox instalado
- ssh-keygen disponible en PATH
- Sistema operativo: Windows/Linux/macOS

## Instalación

1. Clonar el repositorio
2. Compilar la aplicación:
```bash
go build -o plataforma.exe
```

3. Ejecutar:
```bash
./plataforma.exe
```

4. Abrir navegador en: http://localhost:8000

## Flujo de Trabajo

### 1. Agregar VM Base
1. Seleccionar una VM existente de VirtualBox
2. Agregar descripción (opcional)
3. Click en "Agregar VM Base"

**Validaciones:**
- La VM debe existir en VirtualBox
- Se registra advertencia si la VM está encendida

### 2. Crear Llaves SSH de Root
1. Click en "Crear llaves de root"
2. Se generan llaves RSA 1024 en `keys/{vm_name}/root/`
3. El botón de descarga se habilita automáticamente

**Requisitos:**
- La VM base debe estar agregada

### 3. Convertir Disco a Multiconexión
1. Click en "Convertir disco a multiconexión"
2. El disco existente de la VM se convierte a tipo multiattach
3. El disco aparece en la sección "Discos Multiconexión"

**Requisitos:**
- Las llaves de root deben estar creadas
- El disco no debe estar ya convertido

**Validaciones:**
- Verifica que el disco existe
- Verifica que no es ya de tipo multiattach
- Registra UUID y ruta del disco

### 4. Crear VM de Usuario
1. En la sección "Discos Multiconexión", ingresar nombre y descripción
2. Click en "Crear VM de usuario"
3. La VM se crea, configura e inicia automáticamente

**Proceso automático:**
- Crea VM con nombre `UserVM_{nombre}`
- Configura 1024 MB de memoria
- Configura adaptador de red en modo bridge
- Agrega controlador SATA
- Conecta disco multiconexión usando UUID
- Inicia VM en modo headless

**Validaciones:**
- El disco debe ser de tipo multiattach
- Múltiples VMs pueden usar el mismo disco

### 5. Crear Usuario en VM
1. En la sección "VMs de Usuario", ingresar nombre de usuario
2. Click en "Crear usuario"
3. El usuario se crea con acceso SSH

**Proceso automático:**
- Verifica que la VM está encendida
- Obtiene IP de la VM
- Genera llaves RSA 1024 para el usuario
- Se conecta por SSH usando llaves de root
- Crea usuario con directorio home y shell bash
- Crea directorio .ssh con permisos 700
- Instala llave pública en authorized_keys con permisos 600

**Requisitos:**
- La VM debe estar encendida
- Las llaves de root de la VM base deben existir

**Reintentos:**
- 3 intentos de conexión SSH
- Timeout de 3 segundos por intento
- Espera de 3 segundos entre intentos

### 6. Descargar Llaves
- **Llaves de root**: Click en "Descargar llaves de root"
  - Archivo: `{vm_name}_root_keys.zip`
- **Llaves de usuario**: Click en "Descargar llaves de usuario"
  - Archivo: `{vm_name}_{username}_keys.zip`

Ambos archivos contienen:
- `id_rsa` (llave privada)
- `id_rsa.pub` (llave pública)

## Estructura de Archivos

```
.
├── main.go                 # Punto de entrada, inicialización
├── go.mod                  # Dependencias
├── state.json             # Estado persistente (generado)
├── plataforma.exe         # Ejecutable compilado
├── logger/
│   └── logger.go          # Sistema de logging
├── handlers/
│   ├── basevms.go         # Gestión de VMs base
│   ├── disks.go           # Gestión de discos multiconexión
│   ├── uservms.go         # Gestión de VMs de usuario
│   └── ssh.go             # Operaciones SSH y llaves
├── vboxmanage/
│   └── vbox.go            # Wrapper de VBoxManage
├── templates/
│   └── index.html         # Interfaz web
├── static/
│   └── style.css          # Estilos
├── keys/                  # Llaves SSH (generado)
│   ├── {vm_base}/
│   │   └── root/
│   │       ├── id_rsa
│   │       └── id_rsa.pub
│   └── {vm_usuario}/
│       └── {username}/
│           ├── id_rsa
│           └── id_rsa.pub
└── logs/                  # Logs de aplicación (generado)
    └── app_YYYY-MM-DD.log
```

## Logs

Los logs se guardan en el directorio `logs/` con el formato:
```
[TIMESTAMP] [LEVEL] [OPERATION_ID] file:line - mensaje
```

**Niveles de log:**
- `DEBUG`: Comandos VBoxManage, detalles técnicos
- `INFO`: Operaciones normales, pasos completados
- `WARN`: Advertencias, situaciones no ideales
- `ERROR`: Errores, fallos de operaciones

**Rotación:**
- Tamaño máximo: 50 MB
- Los archivos antiguos se renombran con timestamp

## API REST

### GET /api/state
Obtiene el estado completo de la aplicación

### GET /api/vboxvms
Lista todas las VMs de VirtualBox

### POST /api/basevms
Agrega una VM base
```json
{
  "name": "nombre_vm",
  "description": "descripción"
}
```

### POST /api/basevms/{vm_name}/rootkeys
Crea llaves SSH de root

### GET /api/basevms/{vm_name}/rootkeys/download
Descarga llaves de root en ZIP

### POST /api/basevms/{vm_name}/disk
Convierte disco a multiconexión

### POST /api/disks/{disk_name}/uservms
Crea VM de usuario desde disco
```json
{
  "name": "nombre",
  "description": "descripción"
}
```

### POST /api/uservms/{vm_name}/user
Crea usuario en VM
```json
{
  "username": "nombre_usuario"
}
```

### GET /api/uservms/{vm_name}/keys/download
Descarga llaves de usuario en ZIP

### DELETE /api/uservms/{vm_name}
Elimina VM de usuario

## Validaciones Implementadas

1. **VM Base debe existir en VirtualBox** antes de agregarla
2. **Llaves de root deben existir** antes de convertir disco
3. **Disco debe ser multiattach** antes de crear VMs de usuario
4. **VM debe estar encendida** para crear usuarios
5. **Disco no puede ser convertido dos veces**
6. **Llaves deben existir** antes de descargarlas

## Controles Dinámicos de UI

- ✅ Botón "Crear llaves de root" se deshabilita después de crear
- ✅ Botón "Descargar llaves de root" se habilita solo con llaves creadas
- ✅ Botón "Convertir disco" se habilita solo con llaves de root
- ✅ Botón "Convertir disco" se deshabilita después de convertir
- ✅ Botón "Descargar llaves de usuario" se habilita solo con usuario creado
- ✅ Botones muestran "⏳ Procesando..." durante operaciones

## Notificaciones

- 🟢 **Verde**: Operación exitosa
- 🔴 **Rojo**: Error
- 🔵 **Azul**: Información
- Duración: 4 segundos
- Posición: Esquina superior derecha

## Solución de Problemas

### Error: "VM no existe en VirtualBox"
- Verificar que la VM está registrada en VirtualBox
- Ejecutar `VBoxManage list vms` para ver VMs disponibles

### Error: "Debe crear llaves de root primero"
- Crear llaves de root antes de convertir el disco

### Error: "VM debe estar encendida"
- Iniciar la VM antes de crear usuarios
- Esperar a que la VM obtenga una IP

### Error: "timeout esperando SSH"
- Verificar que la VM tiene red configurada
- Verificar que el servicio SSH está activo en la VM
- Verificar que las llaves de root están instaladas

### Error: "El disco no es de tipo multiattach"
- Convertir el disco a multiconexión primero
- Verificar que la conversión fue exitosa

## Configuración

### Adaptador de Red
Por defecto usa "Ethernet". Para cambiar, editar en `main.go`:
```go
BridgeAdapter: "Ethernet", // Cambiar según tu adaptador
```

### Tamaño de Logs
Por defecto 50 MB. Para cambiar, editar en `main.go`:
```go
MaxLogSizeMB = 50 // Cambiar según necesidad
```

## Seguridad

- Las llaves SSH se generan con 1024 bits (configurable)
- Las llaves privadas se almacenan localmente
- Se usan permisos apropiados (700 para .ssh, 600 para authorized_keys)
- Las conexiones SSH usan StrictHostKeyChecking=no (solo para desarrollo)

## Licencia

Este proyecto es de código abierto.

## Soporte

Para reportar problemas o solicitar características, revisar los logs en el directorio `logs/`.
