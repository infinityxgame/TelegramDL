---
title: Referencia de API y WebSockets
description: Catálogo detallado de endpoints REST y comunicación en tiempo real vía WebSockets.
---

TelegramDL expone una API HTTP REST completa y un canal WebSocket en el puerto configurado (por defecto `http://127.0.0.1:8000`).

---

## Autenticación

Todas las rutas bajo `/api/*` requieren autenticación mediante cabecera HTTP:

```http
Authorization: Bearer <TU_API_TOKEN>
```

Por ejemplo:

```bash
curl -H "Authorization: Bearer <TU_API_TOKEN>" http://127.0.0.1:8000/api/downloads
```

El parámetro de consulta `?token=` ya **no se acepta** (responde `401`).

### Endpoints Públicos (sin token)

Los siguientes endpoints pueden ser accedidos sin autenticación:

- **`GET /api/theme`**: Obtiene el tema del panel (color).
- **`POST /api/auth/token/send`**: Envía el token de acceso a los Mensajes guardados de Telegram (para acceso remoto). Tiene su propio límite de frecuencia.
- **`GET /api/auth/has-credentials`**: Indica si hay credenciales de Telegram configuradas y sesión activa.
- **`GET /api/ipc/download`**: Encola una descarga vía IPC; solo responde desde el propio equipo (loopback) y exige además las cabeceras `X-TGDL-Request: 1` y `Content-Type: application/json`.

### Puerta de configuración (sin token solo en el primer arranque)

`/api/auth/status` (admite `GET`), `POST /api/auth/credentials`, `POST /api/auth/send-code`, `POST /api/auth/verify-code`, `POST /api/auth/verify-2fa` y `POST /api/auth/logout` responden sin token **solo mientras no exista ningún fichero de sesión de Telegram** (primera configuración). Exigen método `POST`, cabecera `X-TGDL-Request: 1` y `Content-Type: application/json`, con un límite de 5 peticiones/minuto por origen y 30/min global (el `429` incluye cabecera `Retry-After`). Con sesión guardada responden `401`.

### Endpoints de Token (requieren autenticación)

- **`GET /api/auth/token`**: Obtiene el token de acceso actual.
- **`POST /api/auth/token/regenerate`**: Regenera un nuevo token de acceso.

---

## Endpoints de Descargas

### 1. Listar Descargas

- **Método**: `GET`
- **Ruta**: `/api/downloads`
- **Descripción**: Devuelve la lista completa de descargas (en cola, descargando, pausadas, completadas, omitidas y fallidas).

### 2. Rutas Adicionales de Descargas

- **`DELETE /api/downloads/history`**: Elimina del historial las descargas completadas, fallidas o canceladas (alias para `/api/clear-history`).
- **`POST /api/downloads/open`**: Abre una descarga en el explorador de archivos (alias para `/api/open`).
- **`DELETE /api/downloads/{id}`**: Elimina una descarga específica por ID. Soporta parámetro `?delete_file=true` para borrar también el archivo físico.

### 3. Añadir Descarga (o Rango)

- **Método**: `POST`
- **Ruta**: `/api/download`
- **Cuerpo JSON**:

```json

{
  "url": "https://t.me/c/2121902112/31449"
}

```

*Soporta también rangos como `https://t.me/c/2121902112/31449-31455`.*

### 4. Control Individual de Tareas

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

- **Abrir en Explorador**: `POST /api/open` con `{"id": "<uuid>"}`

### 5. Control Masivo

- `POST /api/downloads/pause-all`: Pausa todas las descargas activas.
- `POST /api/downloads/resume-all`: Reanuda todas las descargas pausadas.
- `POST /api/downloads/reorder`: Fija la prioridad de la cola. Recibe `{"ids": [...]}`; el primer ID es el siguiente en descargarse.
- `POST /api/downloads/cancel-all`: Cancela todas las descargas activas y en cola.
- `POST /api/clear-history`: Elimina del historial las descargas completadas, fallidas o canceladas.

---

## Endpoints del Modo Escucha (Listener)

- **`GET /api/listener/items`**: Obtiene la lista de archivos detectados pendientes de descarga.
- **`GET /api/listener/settings`**: Devuelve la configuración de chats vigilados y filtros activos.
- **`POST /api/listener/settings`**: Actualiza los chats vigilados y sus filtros multimedia (`f_photos`, `f_videos`, `f_audios`, `f_docs`, `f_stickers`, `auto_download`).
- **`POST /api/listener/download`**: Encola un elemento detectado para descargarlo `{ "id": "<id>" }`.
- **`POST /api/listener/clear`**: Limpia la lista de multimedia detectada.
- **`POST /api/listener/delete`**: Elimina un elemento detectado `{ "id": "<id>" }`.
- **`DELETE /api/listener/item/{id}`**: Elimina un elemento detectado por ID (ruta alternativa).
- **`POST /api/listener/resolve-chat`**: Resuelve un canal o grupo por su nombre de usuario o enlace `{ "query": "nombrecanal" }`.
- **`GET /api/listener/chat/{query}`**: Resuelve un canal o grupo por nombre (ruta alternativa).
- **`GET /api/listener/topics`**: Obtiene la lista de temas/hilos de un supergrupo con foros.
- **`GET /api/listener/topics/{chat_id}`**: Obtiene temas de un chat específico.
- **`POST /api/listener/update-filename`**: Actualiza el nombre de archivo de un elemento detectado.

---

## Configuración y Sistema

- **`GET /api/settings`**: Obtiene la configuración general actual.
- **`POST /api/settings`**: Actualiza rutas de guardado, workers de descarga, etc.
- **`POST /api/settings/speed`**: Modifica el límite global de velocidad en tiempo real `{ "limit_kb": 1024 }`.
- **`POST /api/settings/speed-limit`**: Alias para `/api/settings/speed`.

---

## Endpoints de Logs

- **`GET /api/logs`**: Obtiene el buffer de logs en tiempo real. Soporta parámetros:
  - `since=<id>`: Solo devuelve entradas después de ese ID.
  - `limit=<n>`: Número máximo de entradas (default: 500).
- **`POST /api/logs/clear`**: Limpia el registro en memoria (el archivo en disco se conserva).
- **`GET /api/logs/export`**: Exporta el registro completo en memoria como texto plano.

---

## Endpoints de Sistema de Archivos

- **`GET /api/filesystem`**: Explora directorios en el sistema de archivos (alias).
- **`GET /api/fs/browse`**: Explora directorios en el sistema de archivos del servidor.
- **`POST /api/fs/pick`**: Selecciona una carpeta (usado por el selector nativo).

---

## Endpoints de Sistema

- **`GET /api/system/info`**: Información del sistema operativo, versión de la app y uso de memoria.
- **`GET /api/server/info`**: Alias para `/api/system/info`.
- **`GET /api/system/disk`**: Espacio disponible y total en el disco de descargas.

---

## Endpoints de Actualizaciones

- **`GET /api/update/check`**: Verifica si hay actualizaciones disponibles.
- **`GET /api/updates/check`**: Alias para `/api/update/check`.
- **`GET /api/update/progress`**: Obtiene el progreso de la actualización en curso.
- **`GET /api/updates/progress`**: Alias para `/api/update/progress`.
- **`POST /api/update/install`**: Instala la actualización disponible.
- **`POST /api/updates/install`**: Alias para `/api/update/install`.
- **`POST /api/update/postpone`**: Pospone la actualización.

---

## Endpoints de Control de Aplicación

- **`POST /api/app/exit`**: Cierra limpiamente la aplicación.
- **`POST /api/exit`**: Alias para `/api/app/exit`.

---

## WebSocket en Tiempo Real

Para sincronizar la interfaz sin necesidad de peticiones HTTP constantes:

- **Ruta**: `ws://127.0.0.1:8000/api/ws`
- **Autenticación**: el token viaja en la cabecera `Sec-WebSocket-Protocol` como segundo subprotocolo: `Sec-WebSocket-Protocol: tgdl-v1, <TU_API_TOKEN>` (el panel lo monta automáticamente). Una conexión sin token responde `401`.
- **Comportamiento**: Al conectarse, el servidor envía un snapshot completo del estado. Posteriormente, cada cambio (progreso de descarga, velocidad, nuevo elemento en escucha) emite inmediatamente una actualización estructurada en JSON.
