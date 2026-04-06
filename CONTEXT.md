# csvshipper-win — Contexto del Repositorio

## ¿Qué es?

Servicio **Windows** (ejecutable nativo `.exe`) que actúa como el **transportista** del pipeline de datos.  
Su única responsabilidad es vigilar un directorio de archivos `.sql`, leerlos y enviarlos a RabbitMQ para que el `csvconsumer` los ejecute en TimescaleDB.

Es el puente entre el mundo **Windows** (donde se generan los SQL) y el mundo **Linux/Cloud** (donde se ejecutan en la base de datos).

## Posición en el sistema

```
[csvprocessor (Win)] ──.sql─→ [sqllog/] ──scan─→ [csvshipper-win] ──RPC─→ [RabbitMQ] ──→ [csvconsumer (Linux)] ──→ [TimescaleDB]
                                                        ↕
                                               heartbeat cada 5s → status_queue
```

## Stack técnico

| Componente | Detalle |
|------------|---------|
| Lenguaje | Go (Windows-only: requiere `GOOS=windows`) |
| Mensajería | RabbitMQ vía `amqp091-go`, patrón RPC con reply queue |
| Windows Service | `golang.org/x/sys/windows/svc` |
| Instalación | Script `instalar.bat` + flags CLI del ejecutable |

## Estructura del código

```
csvshipper-win/
├── main.go       # Punto de entrada: flags CLI (install/uninstall/start/stop) y modo interactivo
├── service.go    # Implementación del ciclo de vida de Windows Service (Execute, install, start, stop)
├── rabbit.go     # Cliente RabbitMQ: conexión, RPC síncrono, envío de heartbeats
├── worker.go     # Scanner de directorio + Worker Pool + lógica de bloqueo de archivos (.processing)
├── config.go     # Carga y validación de config.json
├── config.json   # Configuración de producción
├── instalar.bat  # Instalador plug-and-play (doble clic)
└── sqlshipper.exe # Binario compilado listo para despliegue
```

## Flujo de trabajo

1. **Scanner** (~cada 5s): escanea `source_dir` buscando archivos `.sql` y `.sql.processing`
2. **Bloqueo de archivo**: renombra `.sql` → `.sql.processing` (evita doble procesamiento)
3. **Workers** (configurable): leen el contenido del archivo y llaman a `ExecuteSQLRPC`
4. **RPC síncrono**: publica el SQL en `sql_execution_tasks` con `ReplyTo` y `CorrelationId`
5. **Espera respuesta**: bloquea hasta recibir `"SUCCESS"` o `"ERROR:..."` (con timeout configurable)
6. **Éxito** → mueve el archivo a `done_dir`
7. **Error/Timeout** → reintenta indefinidamente cada 5s (o hasta que se cancele el contexto)
8. **Heartbeat**: goroutine separada publica `{"os":"windows","status":"UP"}` en `status_queue` cada 5s

## Repositorios relacionados

| Repositorio | Relación |
|-------------|----------|
| `csvprocessor` | Genera los `.sql` que este servicio envía |
| `csvconsumer` | Consume la cola RabbitMQ que este servicio alimenta |
| `db-infra` | Destino final de los datos |
