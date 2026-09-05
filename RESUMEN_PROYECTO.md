# 🚀 TelegramDL — Especificación Técnica y Resumen Integral del Sistema

> **Propósito del documento**: Este archivo describe en detalle la arquitectura, componentes, flujos de trabajo paso a paso, estructura de base de datos y reglas clave de **TelegramDL** para que cualquier agente de IA o desarrollador comprenda el funcionamiento exacto del proyecto.

---

## 📌 1. Visión General del Proyecto

**TelegramDL** es un **gestor de descargas de Telegram de alto rendimiento para escritorio**, desarrollado en **Go + Wails v2 + Vue 3**.

### 🎯 Qué ES y qué NO ES:
- ✅ **ES**: Un gestor especializado en descargar archivos, videos, audios, fotos y stickers de Telegram a máxima velocidad con descargas por fragmentos paralelos, soporte de rangos de mensajes, escucha automática en segundo plano y panel de control en tiempo real.
- ❌ **NO ES**: Un cliente de mensajería (no es para chatear, enviar mensajes, llamadas ni gestionar contactos). Su único propósito es **recibir, vigilar y descargar contenido**.

---

## 🛠️ 2. Pila Tecnológica y Arquitectura

```
┌─────────────────────────────────────────────────────────────┐
│                      FRONTEND (Vue 3)                       │
│    Vite + Composition API + Lucide Icons + Vanilla CSS      │
│  Vistas: Descargas (Monitor/Historial), Escucha, Ajustes    │
└──────────────────────────────┬──────────────────────────────┘
                               │ IPC / WebSocket / REST
┌──────────────────────────────▼──────────────────────────────┐
│                    WAILS v2 DESKTOP BRIDGE                  │
│   Eventos nativos ('tgdl:state') + Diálogos del Sistema     │
└──────────────────────────────┬──────────────────────────────┘
                               │
┌──────────────────────────────▼──────────────────────────────┐
│                      BACKEND (Go 1.22+)                     │
│                                                             │
│  ├─ pkg/telegram   ── Conexión MTProto con Telegram (gotd)  │
│  ├─ pkg/downloader ── Motor de Descargas Multihilo y Parser │
│  ├─ pkg/listener   ── Vigilancia y Detección Multimedia     │
│  ├─ pkg/storage    ── Persistencia SQLite (tgdown.sqlite3)  │
│  ├─ pkg/server     ── Servidor HTTP REST (8000) & WebSocket │
│  └─ pkg/updater    ── Autoactualizaciones vía GitHub API    │
└─────────────────────────────────────────────────────────────┘
```

- **Lenguaje Backend**: Go 1.22+
- **Librería MTProto**: `github.com/gotd/td` (Cliente nativo de Telegram en Go puro, sin CGO).
- **Framework de Escritorio**: `wailsapp/wails/v2` (Usa WebView2 en Windows).
- **Frontend**: Vue 3 (Vite, `@vitejs/plugin-vue`, `lucide-vue-next`).
- **Base de Datos**: SQLite nativo (`modernc.org/sqlite`) en `~/.tgdown/tgdown.sqlite3`.
- **Servidor Web Interno**: HTTP REST + WebSocket en `127.0.0.1:8000`.

---

## 📁 3. Estructura de Paquetes y Responsabilidades

### 🔹 `pkg/telegram` — Conexión y Gestión de MTProto
- **`ClientManager`**: Gestiona el ciclo de vida de la conexión Telegram MTProto.
- **Autenticación**: Soporta código SMS, código por app de Telegram y contraseña 2FA (`SRP`).
- **Migración de Sesión**: Importa automáticamente sesiones existentes de Pyrogram (`downloader_session.session` o `tg_session.json`).
- **Gestión de Entidades y `AccessHash`**: Mantiene en memoria y resuelve los `AccessHash` necesarios para interactuar con canales privados, supergrupos y usuarios.
- **`WaitReady(ctx)`**: Sincronización bloqueante segura para asegurar que ninguna descarga inicie antes de completar el apretón de manos (*handshake*) con el DC de Telegram.

### 🔹 `pkg/downloader` — Motor de Descargas
- **`Parser` (`parser.go`)**:
  - Parsea URLs como `https://t.me/c/2121902112/31449`, `https://t.me/c/2121902112/31449-31455`, `https://t.me/canal/100`, `https://t.me/b/botname/50`.
  - Extrae `ChatID` (con formato MTProto `-100...`), `StartMsgID` y `EndMsgID`.
- **`ExtractMediaInfo` (`media.go`)**:
  - Extrae metadata de `MessageMediaDocument`, `MessageMediaPhoto`, `MessageMediaWebPage`.
  - Obtiene nombre de archivo real (vía atributos de Telegram o nombres generados limpios), tipo (`file`, `video`, `song`, `photo`, `sticker`), tamaño en bytes y `InputFileLocation`.
- **`Engine` (`engine.go`)**:
  - Control de concurrencia mediante un canal semáforo (`activeSem`) basado en `MaxConcurrentDownloads` (default: 3).
  - Descarga en paralelo por fragmentos (*chunks* de 512 KB) usando `tdDownloader.NewDownloader().Download(...)`.
  - Cálculo de progreso (0%–100%) y velocidad en vivo (`MB/s`).
  - Límite de velocidad configurable (*throttling*).
  - Prevención de duplicados (`reservations.go`): si el archivo ya existe con el mismo tamaño, pasa a `skipped` (100%).

### 🔹 `pkg/listener` — Escucha en Tiempo Real
- **`ListenerEngine` (`listener.go`)**:
  - Escucha actualizaciones de mensajes entrantes mediante el dispatcher de `gotd/td`.
  - Filtra por chats vigilados configurados en SQLite (`listener_chats`).
  - Aplica filtros por tipo de archivo (`fotos`, `videos`, `audios`, `documentos`, `stickers`).
  - Si el chat tiene `AutoDownload = true`, lo encola directamente en el motor de descarga.
  - Si `AutoDownload = false`, lo agrega a la lista **"Multimedia Detectada"** con estado `available` para descarga manual con un clic.

### 🔹 `pkg/storage` — Persistencia SQLite
- Base de datos ubicada en `~/.tgdown/tgdown.sqlite3`.
- Tablas principales:
  1. `app_config`: Claves y valores JSON de configuración general.
  2. `listener_chats`: Lista de chats vigilados con sus filtros individuales de contenido.
  3. `downloads`: Registro completo de descargas (activas, completadas, omitidas, fallidas, canceladas).
  4. `download_chunks`: Registro de fragmentos descargados para reanudación de archivos.

### 🔹 `pkg/server` — Servidor API y WebSocket
- Expone endpoints REST en `/api/*` y WebSocket en `/api/ws`.
- Comunica en tiempo real el snapshot del sistema a través de `wsClient` con mutex sincronizado para evitar condiciones de carrera (*concurrent websocket writes*).
- Expone el método `BuildStateSnapshot()` para sincronización nativa con Wails.

---

## 🔄 4. Flujos Paso a Paso Detallados

### 📥 Flujo A: Descarga Manual por Enlace
```
1. Usuario pega URL: https://t.me/c/2121902112/31449
2. Vue emite POST /api/download { url }
3. server.go llama a downloader.ParseURL(url):
   -> ChatID = -1002121902112
   -> StartMsgID = 31449, EndMsgID = 31449
4. server.go genera DownloadItem (Status: "queued") y llama a downloader.QueueItem(item)
5. QueueItem guarda en SQLite, emite por WebSocket/Wails y lanza go startDownloadJob(id)
6. startDownloadJob adquiere semáforo de concurrencia y cambia Status a "downloading"
7. executeDownload:
   a. Espera a clientMgr.WaitReady()
   b. Llama a fetchMessage(ctx, ChatID, MsgID) -> ChannelsGetMessages
   c. ExtractMediaInfo() extrae nombre, tamaño y ubicación MTProto
   d. Crea archivo temporal <nombre>.temp
   e. tdDownloader descarga fragmentos de 512KB en paralelo
   f. progressWriterAt actualiza bytes, velocidad y emite progreso a la UI
   g. Al finalizar: renombra <nombre>.temp a <nombre>
   h. Marca Status como "completed" (100%) y actualiza SQLite y la UI
```

### 🎧 Flujo B: Escucha de Canales / Grupos (Listener)
```
1. Llega actualización de Telegram vía MTProto Updates Handler
2. listener.go evalúa:
   - ¿ListenerEnabled está activo? (si no, ignora)
   - ¿Es mensaje saliente propio? (si sí, ignora)
   - ¿El ChatID coincide con un chat en listener_chats? (si no, ignora)
   - ¿Contiene multimedia válida según filtros de tipo (f_photos, f_videos, etc.)?
3. Si coincide y pasa filtros:
   - Si AutoDownload = true: Se envía a QueueItem() y comienza a descargarse automáticamente.
   - Si AutoDownload = false: Se registra en SQLite y en memoria en la lista "Multimedia Detectada".
4. La UI de Escucha (ListenerView.vue) muestra el ítem con botón "Descargar".
```

### ⚡ Flujo C: Control de Tareas (Pausar / Reanudar / Cancelar / Reintentar / Borrar)
- **Pausar (`POST /api/pause`)**: Cancela el contexto activo de descarga y cambia estado a `paused`.
- **Reanudar (`POST /api/resume`)**: Cambia estado a `queued` y relanza `startDownloadJob(id)`.
- **Cancelar (`POST /api/cancel`)**: Cancela el contexto activo y cambia estado a `cancelled`.
- **Reintentar (`POST /api/retry`)**: Disponible para ítems `failed` o `cancelled`; los reinicia en cola.
- **Borrar (`DELETE /api/downloads/{id}`)**: Elimina el registro de SQLite y opcionalmente el archivo físico en disco.
- **Limpiar Historial (`DELETE /api/downloads/history`)**: Elimina todos los registros completados, fallidos o cancelados manteniendo los archivos en disco.

### 🌐 Flujo D: Sincronización en Tiempo Real con la UI
La aplicación utiliza un modelo de **Sincronización Dual**:
1. **Canal Primario (Push Instantáneo)**:
   - En Wails Desktop: `wailsRuntime.EventsEmit(ctx, "tgdl:state", snap)` capturado por `window.runtime.EventsOn`.
   - En Navegador Web: WebSocket en `/api/ws`.
2. **Canal Secundario (Pull de Respaldo)**:
   - Polling continuo cada 1 segundo llamando a `GET /api/downloads`.
   - Si `/api/downloads` responde exitosamente, la UI garantiza que el estado sea *"Servicio conectado"* y refresca la lista reactiva en Vue (`downloads.value = [...data]`).

---

## 📊 5. Estructura de la Base de Datos SQLite (`tgdown.sqlite3`)

### Tabla: `downloads`
| Columna | Tipo | Descripción |
| :--- | :--- | :--- |
| `id` | `TEXT PRIMARY KEY` | UUID de la descarga |
| `job_id` | `TEXT` | ID de la tarea agrupada (ej. rangos) |
| `message_id` | `INTEGER` | ID del mensaje en Telegram |
| `chat_id` | `INTEGER` | ID del chat/canal en formato MTProto |
| `file_name` | `TEXT` | Nombre final del archivo |
| `status` | `TEXT` | `queued`, `downloading`, `paused`, `completed`, `skipped`, `failed`, `cancelled` |
| `progress` | `REAL` | Porcentaje de 0.0 a 100.0 |
| `total_str` | `TEXT` | Tamaño formateado (ej. "1.45 GB") |
| `current_str` | `TEXT` | Bytes descargados formateados |
| `speed` | `TEXT` | Velocidad actual (ej. "8.50 MB/s") |
| `kind` | `TEXT` | `file`, `video`, `song`, `photo`, `sticker` |
| `file_path` | `TEXT` | Ruta absoluta en el disco |
| `source` | `TEXT` | `manual` o `listener` |
| `total_bytes` | `INTEGER` | Tamaño total en bytes |
| `current_bytes`| `INTEGER` | Bytes descargados |
| `created_at` | `REAL` | Timestamp Unix de creación |
| `updated_at` | `REAL` | Timestamp Unix de última actualización |

### Tabla: `listener_chats`
| Columna | Tipo | Descripción |
| :--- | :--- | :--- |
| `chat_id` | `INTEGER PRIMARY KEY` | ID del chat vigilado |
| `name` | `TEXT` | Nombre o título del chat |
| `auto_download` | `INTEGER` | `1` para descarga automática, `0` solo detectar |
| `f_photos` | `INTEGER` | Filtro de fotos (`1` activo, `0` ignorar) |
| `f_videos` | `INTEGER` | Filtro de videos (`1` activo, `0` ignorar) |
| `f_audios` | `INTEGER` | Filtro de audios/música (`1` activo, `0` ignorar) |
| `f_docs` | `INTEGER` | Filtro de documentos/archivos (`1` activo, `0` ignorar)|
| `f_stickers` | `INTEGER` | Filtro de stickers (`1` activo, `0` ignorar) |

---

## ⚠️ 6. Reglas Críticas para Agentes

1. **Trabajar ÚNICAMENTE en la rama `go`**: NUNCA modificar la rama `main` (que contiene el código legacy en Python).
2. **Las descargas manuales NUNCA pasan por filtros de chat**: No comprobar si el chat está en `listener_chats` cuando se introduce una URL en el formulario de descargas.
3. **No realizar escaneos completos de historial**: Para URLs de mensaje directo (ej. `https://t.me/c/2121902112/31449`), solicitar directamente el ID de mensaje específico con `ChannelsGetMessages` o `MessagesGetMessages`.
4. **Seguridad en concurrencia**: Proteger siempre las escrituras de WebSocket y el acceso a mapas en memoria (`sync.RWMutex` y `wsClient.mu`).
5. **No bloquear el arranque de la ventana**: La interfaz debe mostrar el panel de inmediato; las tareas de red lentas (como buscar actualizaciones en GitHub) deben ejecutarse en segundo plano.
