# csvshipper-win

> **Transportista SQL para Windows** — Servicio nativo que envía archivos SQL a RabbitMQ para su ejecución remota en TimescaleDB.

---

## ¿Qué hace?

`csvshipper-win` es un **Windows Service** escrito en Go que vigila un directorio de archivos `.sql` generados por `csvprocessor` y los envía de forma confiable a una cola de RabbitMQ mediante el patrón **RPC síncrono** (espera confirmación de ejecución antes de marcar el archivo como completado).

### Funcionalidades clave

- 📁 **Escaneo automático** de directorio cada 5 segundos
- 🔒 **Bloqueo de archivos a nivel OS** (rename a `.processing`) para evitar doble procesamiento
- 🔁 **Reintentos infinitos** con backoff de 5 segundos ante fallos de red o RabbitMQ
- 📡 **Heartbeat** cada 5 segundos a la cola `status_queue` para que el consumer Linux sepa que el Windows sender está vivo
- ✅ **Confirmación de ejecución** — el archivo solo se mueve a "terminados" cuando el consumer confirma éxito con `"SUCCESS"`
- 🏁 **Apagado graceful** — respeta las señales Stop/Shutdown del SCM de Windows

---

## Arquitectura

```
[source_dir/*.sql]
       │
       ▼
  ┌──────────┐       ┌─────────────────────┐       ┌─────────────┐
  │  Scanner │──────▶│  Worker Pool        │──RPC──▶  RabbitMQ   │
  │ (5s tick)│       │  (N goroutines)     │       │ sql_tasks   │
  └──────────┘       └─────────────────────┘       └──────┬──────┘
                                                          │
                              heartbeat cada 5s           ▼
                      ┌────────────────┐         ┌──────────────────┐
                      │  status_queue  │◀────────│  csvconsumer     │
                      └────────────────┘         │  (Linux/Docker)  │
                                                 └──────────────────┘
```

---

## Instalación rápida (Plug & Play)

> ⚠️ **Requiere ejecutar como Administrador**

### Opción 1: Script automático (recomendado)

1. Editar `config.json` con las rutas y credenciales correctas
2. Hacer doble clic en **`instalar.bat`** (como Administrador)

El script instala y arranca el servicio automáticamente.

### Opción 2: Manual con CLI

```powershell
# 1. Instalar el servicio en services.msc
.\sqlshipper.exe -install

# 2. Iniciar el servicio
.\sqlshipper.exe -start

# 3. Ver estado en el Administrador de Servicios
services.msc
```

---

## Comandos CLI

| Comando | Descripción |
|---------|-------------|
| `-install` | Registra el servicio en Windows SCM con arranque automático |
| `-uninstall` | Elimina el registro del servicio |
| `-start` | Inicia el servicio |
| `-stop` | Detiene el servicio |
| `-config <ruta>` | Especifica una ruta alternativa al archivo `config.json` |
| *(sin flags)* | Ejecuta en modo interactivo (debug, muestra logs en consola) |

---

## Configuración (`config.json`)

```json
{
  "source_dir":          "C:\\ruta\\a\\los\\archivos\\sql",
  "done_dir":            "C:\\ruta\\terminados",
  "error_dir":           "C:\\ruta\\errores",
  "worker_count":        5,
  "rabbitmq_url":        "amqp://usuario:password@IP_RABBIT:5672/",
  "task_queue":          "sql_execution_tasks",
  "rpc_timeout_seconds": 30
}
```

### Descripción de cada campo

| Campo | Tipo | Descripción |
|-------|------|-------------|
| `source_dir` | string | Directorio donde el `csvprocessor` deposita los `.sql` para enviar |
| `done_dir` | string | Directorio destino para archivos enviados exitosamente |
| `error_dir` | string | Directorio destino para archivos que fallaron (actualmente no usado — se reintenta siempre) |
| `worker_count` | int | Número de goroutines procesando en paralelo. Default: 1 |
| `rabbitmq_url` | string | URL de conexión AMQP al broker RabbitMQ |
| `task_queue` | string | Nombre de la cola donde se publican los SQL. Debe coincidir con lo que escucha el `csvconsumer` |
| `rpc_timeout_seconds` | int | Tiempo máximo de espera por respuesta del consumer. Default: 30 |

> 💡 **Tip**: Cuando el servicio corre como Windows Service, carga `config.json` desde el mismo directorio que el `.exe`. Al usar el flag `-config`, se puede especificar cualquier ruta.

---

## Flujo detallado de un archivo

```
archivo.sql detectado en source_dir
       │
       ▼
   Espera 200ms (para que el archivo termine de escribirse)
       │
       ▼
   Renombra a archivo.sql.processing  ← bloqueo OS
       │
       ▼
   Lee el contenido completo del archivo
       │
       ▼
   Publica en RabbitMQ con CorrelationId + ReplyTo (reply queue anónima)
       │
       ├── Éxito (recibe "SUCCESS") ──────▶ Mueve a done_dir
       │
       ├── Error del consumer ("ERROR:...") ──▶ Reintenta en 5s
       │
       ├── Timeout ──────────────────────────▶ Reintenta en 5s
       │
       └── Context cancelado (Stop/Shutdown) ─▶ Revierte a .sql y termina
```

---

## Logs

Cuando corre como servicio Windows (no interactivo), los logs se escriben en:

```
C:\Windows\Temp\sqlshipper.log
```

Abrir con cualquier editor de texto o con PowerShell:

```powershell
Get-Content C:\Windows\Temp\sqlshipper.log -Tail 50
```

---

## Colas RabbitMQ

| Cola | Durable | Descripción |
|------|---------|-------------|
| `sql_execution_tasks` | ✅ Sí | Cola principal donde se publican los SQL para ejecución |
| `status_queue` | ❌ No | Cola de heartbeats (publicados cada 5s con `{"os":"windows","status":"UP"}`) |
| `amq.gen-*` (auto) | ❌ No | Cola de reply exclusiva y anónima por sesión para recibir confirmaciones RPC |

---

## Arquitectura del código

### `main.go`
Punto de entrada. Detecta si corre como servicio Windows o en modo interactivo. Procesa los flags CLI para gestión del servicio (`-install`, `-uninstall`, `-start`, `-stop`).

### `service.go`
Implementa la interfaz `svc.Handler` de Windows. Gestiona el ciclo de vida completo:
- `StartPending` → carga config → conecta RabbitMQ → lanza workers y heartbeat → `Running`
- Al recibir `Stop`/`Shutdown`: cancela el contexto → espera 3s para drenar tareas → cierra conexión → `StopPending`

### `rabbit.go`
Cliente RabbitMQ con tres responsabilidades:
- `ConnectRabbit()`: conecta y declara la reply queue exclusiva
- `ExecuteSQLRPC()`: publica el SQL con `CorrelationId`/`ReplyTo` y bloquea esperando respuesta
- `SendHeartbeat()`: publica `{"os":"windows","status":"UP"}` en `status_queue` cada 5 segundos

### `worker.go`
Sistema de escaneo y procesamiento:
- `RunScannerAndWorkers()`: lanza N workers y escanea el directorio cada 5s
- `worker()`: goroutine que toma jobs del channel y llama a `processFile()`
- `processFile()`: bloquea el archivo, lee, envía por RPC, mueve a done/error
- `moveFile()`: mueve archivos con fallback a copia+borrado (para particiones distintas)

### `config.go`
Deserializa `config.json`. Aplica defaults: `WorkerCount=1` y `RPCTimeoutSeconds=30` si no se especifican.

---

## Compilación (para desarrollo)

Requiere Go 1.21+ y Windows (o cross-compile con `GOOS=windows`):

```bash
# En Windows
go build -o sqlshipper.exe .

# Cross-compile desde Linux/Mac
GOOS=windows GOARCH=amd64 go build -o sqlshipper.exe .
```

---

## Repositorios relacionados

| Repositorio | Relación |
|-------------|----------|
| [`csvprocessor`](../csvprocessor/) | Genera los archivos `.sql` que este servicio envía |
| [`csvconsumer`](../csvconsumer/) | Consume la cola `sql_execution_tasks` y ejecuta los SQL en TimescaleDB |
| [`db-infra`](../db-infra/) | Provee la instancia de TimescaleDB destino |
