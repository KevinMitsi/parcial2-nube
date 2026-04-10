# Documento de Requisitos: Mejoras de Logging y Flujo de Trabajo para Plataforma de VMs

## Introducción

Este documento especifica las mejoras críticas para la aplicación web de gestión de máquinas virtuales VirtualBox desarrollada en Go. El sistema actual permite gestionar VMs base, discos multiconexión y VMs de usuario, pero carece de un sistema de logging robusto y tiene deficiencias en el flujo de trabajo y validaciones. Las mejoras incluyen logging detallado de operaciones, validaciones de flujo de trabajo, gestión correcta de rutas de discos, y controles dinámicos de interfaz de usuario.

## Glosario

- **Sistema**: La aplicación web completa de gestión de VMs VirtualBox
- **Logger**: Componente del sistema responsable de registrar eventos y operaciones
- **VM_Base**: Máquina virtual de VirtualBox existente que sirve como plantilla
- **Disco_Multiconexión**: Disco virtual de la VM Base convertido a tipo multiconexión que puede ser conectado a múltiples VMs simultáneamente
- **VM_Usuario**: Máquina virtual creada a partir de un disco multiconexión para uso de usuarios finales
- **Handler**: Componente del sistema que procesa solicitudes HTTP
- **Estado_Operación**: Indicador del progreso de una operación (iniciada, en_progreso, completada, fallida)
- **Validador_Flujo**: Componente que verifica que las operaciones se ejecuten en el orden correcto
- **Tipo_Multiconexión**: Tipo de disco VirtualBox que permite que múltiples VMs compartan el mismo disco simultáneamente (multiattach)
- **Llave_SSH**: Par de llaves criptográficas (pública y privada) para autenticación SSH
- **UI_Controller**: Componente que gestiona el estado de habilitación/deshabilitación de controles de interfaz

## Requisitos

### Requisito 1: Sistema de Logging Estructurado

**User Story:** Como desarrollador, quiero un sistema de logging completo, para poder rastrear todas las operaciones del sistema y diagnosticar problemas rápidamente.

#### Acceptance Criteria

1. WHEN THE Sistema inicia, THE Logger SHALL registrar la versión de la aplicación, configuración inicial y timestamp de inicio
2. WHEN una operación de VM comienza, THE Logger SHALL registrar el tipo de operación, nombre de VM, usuario solicitante y timestamp
3. WHILE una operación de VM está en progreso, THE Logger SHALL registrar cada paso intermedio con su estado y duración
4. WHEN una operación de VM se completa exitosamente, THE Logger SHALL registrar el resultado final, duración total y recursos creados
5. IF una operación de VM falla, THEN THE Logger SHALL registrar el error detallado, stack trace, estado del sistema y paso donde falló
6. THE Logger SHALL incluir niveles de log (DEBUG, INFO, WARN, ERROR) para cada mensaje
7. THE Logger SHALL escribir logs tanto a consola como a archivo rotativo con límite de tamaño
8. THE Logger SHALL incluir identificadores únicos de operación para rastrear flujos completos

### Requisito 2: Validación de Existencia de VM Base

**User Story:** Como usuario, quiero que el sistema valide que la VM base existe en VirtualBox, para evitar errores al intentar crear recursos desde VMs inexistentes.

#### Acceptance Criteria

1. WHEN un usuario intenta agregar una VM base, THE Validador_Flujo SHALL verificar que la VM existe en VirtualBox
2. IF la VM base no existe en VirtualBox, THEN THE Sistema SHALL retornar un error descriptivo y registrar el intento fallido
3. WHEN una VM base es agregada exitosamente, THE Sistema SHALL verificar que la VM está apagada
4. IF la VM base está encendida, THEN THE Sistema SHALL mostrar una advertencia pero permitir agregarla

### Requisito 3: Flujo de Generación de Llaves SSH para Root

**User Story:** Como usuario, quiero generar llaves SSH para el usuario root de una VM base, para poder acceder y configurar la VM de forma segura.

#### Acceptance Criteria

1. WHEN un usuario solicita crear llaves SSH de root, THE Sistema SHALL verificar que la VM base existe en el estado
2. THE Sistema SHALL generar un par de llaves RSA de 1024 bits en el directorio `keys/{vm_name}/root/`
3. WHEN las llaves son generadas exitosamente, THE Sistema SHALL actualizar el estado `hasRootKeys` a verdadero
4. THE Sistema SHALL habilitar el botón de descarga de llaves inmediatamente después de la generación
5. IF la generación de llaves falla, THEN THE Sistema SHALL registrar el error y mantener `hasRootKeys` en falso

### Requisito 4: Descarga de Llaves SSH

**User Story:** Como usuario, quiero descargar las llaves SSH generadas en formato ZIP, para poder usarlas en mi cliente SSH.

#### Acceptance Criteria

1. WHEN un usuario solicita descargar llaves de root, THE Sistema SHALL verificar que las llaves existen en el sistema de archivos
2. THE Sistema SHALL crear un archivo ZIP conteniendo `id_rsa` e `id_rsa.pub`
3. THE Sistema SHALL enviar el ZIP con el nombre `{vm_name}_root_keys.zip`
4. WHEN un usuario solicita descargar llaves de usuario, THE Sistema SHALL crear un ZIP con nombre `{vm_name}_{username}_keys.zip`
5. IF las llaves no existen, THEN THE Sistema SHALL retornar un error HTTP 404 con mensaje descriptivo

### Requisito 5: Validación de Flujo para Conversión de Discos

**User Story:** Como usuario, quiero que el sistema valide que las llaves SSH de root existen antes de convertir el disco a tipo multiconexión, para asegurar que podré configurar las VMs creadas desde ese disco.

#### Acceptance Criteria

1. WHEN un usuario intenta convertir un disco a multiconexión, THE Validador_Flujo SHALL verificar que `hasRootKeys` es verdadero
2. IF `hasRootKeys` es falso, THEN THE Sistema SHALL retornar un error HTTP 400 indicando que debe crear llaves primero
3. THE UI_Controller SHALL mantener el botón "Convertir disco a multiconexión" deshabilitado mientras `hasRootKeys` sea falso
4. WHEN `hasRootKeys` cambia a verdadero, THE UI_Controller SHALL habilitar el botón "Convertir disco a multiconexión"

### Requisito 6: Conversión de Disco a Tipo Multiconexión

**User Story:** Como usuario, quiero convertir el disco existente de una VM Base a tipo multiconexión, para poder crear múltiples VMs de usuario que compartan el mismo disco base.

#### Acceptance Criteria

1. WHEN un usuario solicita crear un disco multiconexión, THE Sistema SHALL obtener la ruta del disco de la VM Base usando VBoxManage showvminfo
2. THE Sistema SHALL verificar que el disco existe y es de tipo normal
3. THE Sistema SHALL ejecutar VBoxManage modifymedium para cambiar el tipo del disco a multiattach
4. WHEN la conversión es exitosa, THE Sistema SHALL almacenar la información del disco (UUID, ruta, VM origen) en el estado
5. THE Sistema SHALL registrar la ruta del disco original, el UUID y el resultado de la conversión en los logs
6. IF el disco ya es de tipo multiconexión, THEN THE Sistema SHALL retornar un error indicando que el disco ya fue convertido

### Requisito 7: Visualización de Discos Multiconexión

**User Story:** Como usuario, quiero ver todos los discos convertidos a tipo multiconexión en la segunda sección del dashboard, para gestionar los discos disponibles y crear VMs desde ellos.

#### Acceptance Criteria

1. THE Sistema SHALL mostrar todos los discos multiconexión en la sección "Discos Multiconexión" del dashboard
2. WHEN un disco es convertido a multiconexión, THE Sistema SHALL agregarlo inmediatamente a la lista de discos
3. FOR EACH disco, THE Sistema SHALL mostrar nombre del disco, VM Base origen, UUID del disco y ruta completa
4. FOR EACH disco, THE Sistema SHALL mostrar un botón "Crear máquina virtual"
5. THE Sistema SHALL actualizar la visualización automáticamente cada 5 segundos

### Requisito 8: Creación de VM de Usuario desde Disco Multiconexión

**User Story:** Como usuario, quiero crear una VM de usuario desde un disco multiconexión, para que múltiples usuarios puedan trabajar con la misma imagen base sin duplicar el disco.

#### Acceptance Criteria

1. WHEN un usuario solicita crear una VM de usuario, THE Sistema SHALL validar que el disco multiconexión existe y es de tipo multiattach
2. THE Sistema SHALL crear una nueva VM con el nombre proporcionado por el usuario
3. THE Sistema SHALL configurar la VM con 1024 MB de memoria y adaptador de red en modo bridge
4. THE Sistema SHALL agregar un controlador SATA a la VM
5. THE Sistema SHALL conectar el disco multiconexión existente a la VM usando su UUID
6. THE Sistema SHALL iniciar la VM en modo headless
7. WHEN la VM es creada exitosamente, THE Sistema SHALL agregarla a la sección "VMs de Usuario" con referencia al disco multiconexión usado
8. THE Sistema SHALL permitir crear múltiples VMs desde el mismo disco multiconexión simultáneamente

### Requisito 9: Gestión de Usuarios en VMs

**User Story:** Como administrador, quiero crear usuarios con llaves SSH en las VMs de usuario, para que los usuarios finales puedan acceder de forma segura a sus VMs.

#### Acceptance Criteria

1. WHEN un usuario solicita crear un usuario en una VM, THE Sistema SHALL verificar que la VM está encendida
2. THE Sistema SHALL obtener la dirección IP de la VM usando VBoxManage
3. THE Sistema SHALL generar un par de llaves RSA de 1024 bits para el usuario en `keys/{vm_name}/{username}/`
4. THE Sistema SHALL conectarse a la VM usando SSH con las llaves de root
5. THE Sistema SHALL ejecutar comandos para crear el usuario con directorio home y shell bash
6. THE Sistema SHALL crear el directorio `.ssh` con permisos 700 para el usuario
7. THE Sistema SHALL instalar la llave pública en `authorized_keys` con permisos 600
8. WHEN el usuario es creado exitosamente, THE Sistema SHALL actualizar `hasUserKeys` a verdadero
9. IF la VM no está encendida, THEN THE Sistema SHALL retornar un error HTTP 400

### Requisito 10: Controles Dinámicos de Interfaz

**User Story:** Como usuario, quiero que los botones de la interfaz se habiliten y deshabiliten según el estado de las operaciones, para evitar ejecutar acciones en orden incorrecto.

#### Acceptance Criteria

1. THE UI_Controller SHALL deshabilitar el botón "Crear llaves de root" después de que las llaves son creadas
2. THE UI_Controller SHALL habilitar el botón "Descargar llaves de root" solo cuando `hasRootKeys` es verdadero
3. THE UI_Controller SHALL habilitar el botón "Convertir disco a multiconexión" solo cuando `hasRootKeys` es verdadero y `diskConverted` es falso
4. THE UI_Controller SHALL deshabilitar el botón "Convertir disco a multiconexión" después de que el disco es convertido
5. THE UI_Controller SHALL habilitar el botón "Crear VM de usuario" para cada disco multiconexión disponible
6. THE UI_Controller SHALL habilitar el botón "Descargar llaves de usuario" solo cuando `hasUserKeys` es verdadero
7. WHILE una operación está en progreso, THE UI_Controller SHALL mostrar el botón con texto "⏳ Procesando..." y deshabilitarlo
8. WHEN una operación se completa, THE UI_Controller SHALL restaurar el texto original del botón y actualizar su estado de habilitación

### Requisito 11: Notificaciones de Usuario

**User Story:** Como usuario, quiero recibir notificaciones visuales sobre el resultado de las operaciones, para saber si mis acciones fueron exitosas o fallaron.

#### Acceptance Criteria

1. WHEN una operación se completa exitosamente, THE Sistema SHALL mostrar una notificación toast verde con mensaje descriptivo
2. WHEN una operación falla, THE Sistema SHALL mostrar una notificación toast roja con el mensaje de error
3. WHEN una operación informativa ocurre, THE Sistema SHALL mostrar una notificación toast azul
4. THE Sistema SHALL mostrar las notificaciones en la esquina superior derecha durante 4 segundos
5. THE Sistema SHALL permitir múltiples notificaciones simultáneas apiladas verticalmente

### Requisito 12: Logging de Operaciones VBoxManage

**User Story:** Como desarrollador, quiero que todas las invocaciones a VBoxManage sean registradas, para poder diagnosticar problemas con VirtualBox.

#### Acceptance Criteria

1. WHEN THE Sistema invoca VBoxManage, THE Logger SHALL registrar el comando completo con todos los argumentos
2. WHEN VBoxManage retorna exitosamente, THE Logger SHALL registrar la salida estándar y el código de retorno
3. IF VBoxManage falla, THEN THE Logger SHALL registrar la salida de error estándar y el código de retorno
4. THE Logger SHALL registrar la duración de cada invocación a VBoxManage
5. THE Logger SHALL incluir el contexto de la operación (crear VM, clonar disco, etc.) en cada log

### Requisito 13: Persistencia de Estado

**User Story:** Como usuario, quiero que el estado de la aplicación se guarde automáticamente, para no perder información si la aplicación se reinicia.

#### Acceptance Criteria

1. WHEN el estado de la aplicación cambia, THE Sistema SHALL serializar el estado a JSON
2. THE Sistema SHALL escribir el estado en el archivo `state.json`
3. WHEN THE Sistema inicia, THE Sistema SHALL cargar el estado desde `state.json` si existe
4. IF `state.json` no existe, THEN THE Sistema SHALL iniciar con estado vacío
5. IF la carga de `state.json` falla, THEN THE Logger SHALL registrar el error y continuar con estado vacío

### Requisito 14: Validación de Operaciones de Disco

**User Story:** Como usuario, quiero que el sistema valide el estado de los discos antes de realizar operaciones, para evitar errores de tipo de disco incorrecto.

#### Acceptance Criteria

1. WHEN un usuario intenta crear una VM desde un disco, THE Validador_Flujo SHALL verificar que el disco es de tipo multiattach
2. IF el disco no es de tipo multiattach, THEN THE Sistema SHALL retornar un error indicando que debe convertir el disco primero
3. WHEN un disco multiconexión es usado para crear una VM, THE Sistema SHALL registrar la VM asociada al disco
4. WHEN una VM es eliminada, THE Sistema SHALL mantener el registro del disco multiconexión disponible para otras VMs
5. THE Sistema SHALL permitir crear múltiples VMs desde el mismo disco multiconexión simultáneamente

### Requisito 15: Manejo de Errores de SSH

**User Story:** Como desarrollador, quiero que los errores de conexión SSH sean manejados apropiadamente, para proporcionar retroalimentación útil al usuario.

#### Acceptance Criteria

1. WHEN THE Sistema intenta conectarse por SSH, THE Sistema SHALL usar un timeout de 3 segundos
2. IF la conexión SSH falla, THEN THE Logger SHALL registrar el error con detalles de host, puerto y usuario
3. WHEN se instalan llaves SSH en una VM, THE Sistema SHALL verificar que el comando se ejecutó exitosamente
4. IF la instalación de llaves falla, THEN THE Sistema SHALL retornar un error descriptivo al usuario
5. THE Sistema SHALL reintentar operaciones SSH hasta 3 veces con espera de 3 segundos entre intentos

