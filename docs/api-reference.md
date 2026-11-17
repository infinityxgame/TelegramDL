# Referencia de API y WebSockets

TelegramDL expone una API HTTP REST completa y un canal WebSocket en el puerto configurado (por defecto `http://127.0.0.1:8000`).

---

## :material-lock: Autenticación

Todas las rutas bajo `/api/*` requieren autenticación mediante cabecera HTTP:

```http
Authorization: Bearer <TU_API_TOKEN>
```

Por ejemplo:

```bash
curl -H "Authorization: Bearer <TU_API_TOKEN>" http://127.0.0.1:8000/api/downloads
```

El parámetro de consulta `?token=` ya **no se acepta** (responde `401`).

Excepciones sin token: los públicos `/api/theme`, `/api/auth/has-credentials` y `/api/auth/token/send` (con sus propios límites de frecuencia) más `/api/ipc/download` (solo desde el propio equipo, con las cabeceras `X-TGDL-Request: 1` y `Content-Type: application/json`); y la primera configuración de Telegram (`/api/auth/status`, `credentials`, `send-code`, `verify-code`, `verify-2fa`, `logout`), que responde sin token **solo mientras no exista sesión de Telegram guardada**, con método `POST` (`status` admite `GET`), cabecera `X-TGDL-Request: 1`, `Content-Type: application/json` y límite de 5/min por origen y 30/min global. El resto de rutas responde `401` sin token.

---

## :material-download: Endpoints de Descargas

### 1. Listar Descargas
- **Método**: `GET`
- **Ruta**: `/api/downloads`
- **Descripción**: Devuelve la lista completa de descargas (en cola, descargando, pausadas, completadas, omitidas y fallidas).

### 2. Añadir Descarga (o Rango)
- **Método**: `POST`
- **Ruta**: `/api/download`
- **Cuerpo JSON**:
```json
{
  "url": "https://t.me/c/2121902112/31449"
}
```
*Soporta también rangos como `https://t.me/c/2121902112/31449-31455`.*

### 3. Control Individual de Tareas
- **Pausar**: `POST /api/pause` con `{"id": "<uuid>"}`
- **Reanudar**: `POST /api/resume` con `{"id": "<uuid>"}`
- **Cancelar**: `POST /api/cancel` con `{"id": "<uuid>"}`
- **Reintentar**: `POST /api/retry` con `{"id": "<uuid>"}`
- **Eliminar**: `POST /api/delete` con:
```json
{
  "id": "<uuid>",
  "delete_file": false
}
```
- **Abrir Archivo**: `POST /api/open` o `POST /api/downloads/open` con `{"id": "<uuid>"}`
- **Abrir Carpeta**: `POST /api/downloads/open-folder` con `{"id": "<uuid>"}`

### 4. Control Masivo
- `POST /api/downloads/pause-all`: Pausa todas las descargas activas.
- `POST /api/downloads/resume-all`: Reanuda todas las descargas pausadas.
- `POST /api/downloads/reorder`: Fija la prioridad de la cola. Recibe `{"ids": [...]}`; el primer ID es el siguiente en descargarse.
- `POST /api/downloads/cancel-all`: Cancela todas las descargas activas y en cola.
- `POST /api/clear-history`: Elimina del historial las descargas completadas, fallidas o canceladas.

---

## :material-headphones: Endpoints del Modo Escucha (Listener)

- **`GET /api/listener/items`**: Obtiene la lista de archivos detectados pendientes de descarga.
- **`GET /api/listener/settings`**: Devuelve la configuración de chats vigilados y filtros activos.
- **`POST /api/listener/settings`**: Actualiza los chats vigilados y sus filtros multimedia (`f_photos`, `f_videos`, `f_audios`, `f_docs`, `f_stickers`, `auto_download`, `listen_self`).
- **`POST /api/listener/download`**: Encola un elemento detectado para descargarlo `{ "id": "<id>" }`.
- **`POST /api/listener/clear`**: Limpia la lista de multimedia detectada.
- **`POST /api/listener/resolve-chat`**: Resuelve un canal o grupo por su nombre de usuario o enlace `{ "query": "nombrecanal" }`.
- **`GET /api/listener/topics`**: Obtiene la lista de temas/hilos de un supergrupo con foros.

---

## :material-cog: Configuración y Sistema

- **`GET /api/settings`**: Obtiene la configuración general actual.
- **`POST /api/settings`**: Actualiza rutas de guardado, workers de descarga, etc.
- **`POST /api/settings/speed`**: Modifica el límite global de velocidad en tiempo real `{ "limit_kb": 1024 }`.
- **`GET /api/system/info`**: Información del sistema operativo, versión de la app y uso de memoria.
- **`GET /api/system/disk`**: Espacio disponible y total en el disco de descargas.
- **`POST /api/fs/browse`**: Explora directorios en el sistema de archivos del servidor.
- **`GET /api/logs`**: Obtiene el buffer de logs en tiempo real.
- **`POST /api/app/exit`**: Cierra limpiamente la aplicación.

---

## :material-sync: WebSocket en Tiempo Real

Para sincronizar la interfaz sin necesidad de peticiones HTTP constantes:

- **Ruta**: `ws://127.0.0.1:8000/api/ws`
- **Autenticación**: el token viaja en la cabecera `Sec-WebSocket-Protocol` como segundo subprotocolo: `Sec-WebSocket-Protocol: tgdl-v1, <TU_API_TOKEN>` (el panel lo monta automáticamente). Una conexión sin token responde `401`.
- **Comportamiento**: Al conectarse, el servidor envía un snapshot completo del estado. Posteriormente, cada cambio (progreso de descarga, velocidad, nuevo elemento en escucha) emite inmediatamente una actualización estructurada en JSON.
