---
title: Architecture & Database
description: Modular analysis of the Go backend, SQLite persistence, and frontend communication.
---

**TelegramDL** implements a modular, decoupled Go architecture designed to achieve high transfer speeds, low memory footprint, and complete independence between the download engine, user interface, and data persistence layers.

---

## Component Diagram

```mermaid
graph TD
    subgraph Frontend ["Frontend (Vue 3 + Vite)"]
        UI[Views: Downloads, Listener, Settings]
        State[Pinia / Composition API State]
    end

    subgraph Bridge ["Wails v2 Desktop Bridge"]
        Events[Native IPC Events 'tgdl:state']
        Dialogs[System Dialogs & Directory Pickers]
    end

    subgraph Backend ["Go Backend"]
        Server[REST & WebSocket Server]
        Downloader[pkg/downloader - Parallel Engine]
        Listener[pkg/listener - MTProto Watcher]
        TelegramClient[pkg/telegram - gotd/td MTProto]
        Storage[pkg/storage - SQLite modernc]
        Updater[pkg/updater - Auto-updater]
    end

    UI <--> Events
    Events <--> Server
    UI <--> Server
    Server --> Downloader
    Server --> Listener
    Downloader <--> TelegramClient
    Listener <--> TelegramClient
    Downloader --> Storage
    Listener --> Storage
    Server --> Storage
```

---

## Package Structure (`pkg/`)

- **`pkg/telegram`**: Manages the native MTProto client lifecycle using `gotd/td`. Handles network handshakes, supervised reconnects with automatic WebSocket fallback, NTP clock synchronization, authentication (phone or QR, and 2FA via SRP), session evidence in SQLite, entity cache, `AccessHash` resolution, and download readiness gating (`WaitReady`).
- **`pkg/downloader`**: Parallel chunk download engine. Handles 512 KB chunk slicing, scheduling up to 8 parallel workers per task, global concurrency semaphores, automatic resume, and duplicate avoidance.
- **`pkg/listener`**: Evaluates real-time message events from Telegram. Applies chat and media type filters to trigger automatic downloads or register items for manual review.
- **`pkg/storage`**: SQLite persistence layer (`modernc.org/sqlite`, 100% pure Go without CGO). Handles schema migrations, transactions, and concurrent safe reads/writes.
- **`pkg/server`**: HTTP REST and WebSocket server for internal desktop communication and remote access. Emits reactive state snapshots.
- **`pkg/updater`**: Background GitHub release checker and in-place application updater.
- **`pkg/logbus`**: Memory-buffered log streaming with retention policies for real-time monitoring and file exports.

---

## MTProto Client, Clock, and Session

The Telegram client is not a single connection: a supervisor owns its lifecycle and replaces the client when the network, transport, or proxy changes.

```mermaid
flowchart TD
    A[Save API ID / API HASH] --> B[MTProto Supervisor]
    B --> C{First attempt ready?}
    C -- Yes --> D[Direct transport]
    C -- No --> E[WebSocket :443]
    D --> F[WaitReady]
    E --> F
    F --> G{Phone or QR}
    G --> H[Optional 2FA]
    H --> I[tg_session.json + SQLite evidence]
```

### NTP-synchronized clock

Before building the first client, `pkg/telegram` queries `pool.ntp.org`, `time.google.com`, and `time.cloudflare.com` (2 s per server). If a response is valid, it stores the offset in memory and applies it to every MTProto timestamp and to QR code expiry. Startup waits for that attempt so the message ID generator is never initialized with an incorrect time. If all three servers fail, the application continues with the system clock and logs a warning.

### Supervision and transport

- Every client run has a **generation**; responses from an old generation are discarded.
- If the first direct attempt never reaches `ready`, it retries over **WebSocket** on port 443.
- Retries are spaced out and update the `connected` state; `WaitReady` follows the current client even when the transport changes.
- Authentication operations are serialized and re-read the active client after waiting, so they never reuse a stale reference.
- The chat list is requested only after the session is authorized.

### Authorization evidence

In addition to the `tg_session.json` file, the `app_config` table stores internal keys that help recognize an already authorized session:

| Key | Content |
| :--- | :--- |
| `tg_auth_session_id` | Fingerprint of the authorized session file, used to confirm the evidence matches the current file. |
| `tg_auth_user` | Public user data as JSON (ID, first name, username, phone). |
| `tg_auth_pending` | Marker for a started-but-unfinished sign-in setup. |

With that evidence, `GetAuthStatus` can report `CONNECTING` with the known user when the network fails temporarily, instead of sending the user back to the sign-in screen. The states exposed to the dashboard are `UNCONFIGURED`, `NEED_PHONE`, `CONNECTING`, `WAITING_CODE`, `WAITING_2FA`, and `LOGGED_IN`.

---

## SQLite Database Schema

The database resides at `~/.tgdown/tgdown.sqlite3` and contains the following core tables:

### 1. `downloads` Table
Tracks all past and active transfer tasks:

| Column | Type | Description |
| :--- | :--- | :--- |
| `id` | `TEXT PRIMARY KEY` | Unique download UUID |
| `job_id` | `TEXT` | Batch identifier for range downloads |
| `message_id` | `INTEGER` | Telegram message identifier |
| `chat_id` | `INTEGER` | MTProto Chat/Channel ID (prefixed with `-100`) |
| `file_name` | `TEXT` | Target filename on disk |
| `status` | `TEXT` | `queued`, `downloading`, `paused`, `completed`, `skipped`, `failed`, `cancelled` |
| `progress` | `REAL` | Progress percentage from 0.0 to 100.0 |
| `speed` | `TEXT` | Formatted transfer speed (e.g. "12.50 MB/s") |
| `kind` | `TEXT` | Media category (`file`, `video`, `song`, `photo`, `sticker`) |
| `file_path` | `TEXT` | Absolute path on disk |
| `source` | `TEXT` | Origin of the download (`manual` or `listener`) |
| `total_bytes` | `INTEGER` | Total file size in bytes |
| `current_bytes`| `INTEGER` | Downloaded bytes so far |

### 2. `listener_chats` Table
Maintains monitored channels and their filter rules:

| Column | Type | Description |
| :--- | :--- | :--- |
| `chat_id` | `INTEGER PRIMARY KEY` | Monitored Telegram chat ID |
| `name` | `TEXT` | Chat or channel title |
| `auto_download` | `INTEGER` | `1` for auto download, `0` for manual staging |
| `f_photos` | `INTEGER` | Photos filter enabled (`1` or `0`) |
| `f_videos` | `INTEGER` | Videos filter enabled (`1` or `0`) |
| `f_audios` | `INTEGER` | Audio/music filter enabled (`1` or `0`) |
| `f_docs` | `INTEGER` | Documents filter enabled (`1` or `0`) |
| `f_stickers` | `INTEGER` | Stickers filter enabled (`1` or `0`) |

### 3. `download_chunks` Table
Tracks completed chunks per download to allow resumption without data loss:

| Column | Type | Description |
| :--- | :--- | :--- |
| `download_id` | `TEXT` | Foreign key referencing `downloads.id` |
| `part_index` | `INTEGER` | Sequential chunk index |
| `offset_bytes` | `INTEGER` | Byte offset within the target file |
| `part_size` | `INTEGER` | Chunk byte size (typically 512 KB) |
