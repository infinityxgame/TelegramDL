import asyncio
import json
import math
import os
import re
import socket
import string
import sys
import threading
import time
import uuid
from contextlib import suppress
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Dict, List, Optional, Set, Tuple

from dotenv import load_dotenv
from pyrogram import Client, filters
from pyrogram.errors import FloodWait
from pyrogram.file_id import FileId
from pyrogram.handlers import MessageHandler

try:
    from fastapi import Body, FastAPI, HTTPException, WebSocket, WebSocketDisconnect
    from fastapi.middleware.cors import CORSMiddleware
    from fastapi.responses import RedirectResponse
    from fastapi.staticfiles import StaticFiles
    import uvicorn
except ImportError:
    print("❌ Faltan dependencias. Ejecuta: pip install -r requirements.txt")
    sys.exit(1)


# --- Paths and persistent configuration ---
BASE_DIR = Path(__file__).resolve().parent
load_dotenv(BASE_DIR / ".env")
CONFIG_PATH = BASE_DIR / "config.json"
CONFIG_WRITE_LOCK = threading.Lock()

DEFAULT_CONFIG: Dict[str, Any] = {
    "max_concurrent_downloads": 2,
    "parallel_chunks": True,
    "chunk_workers": 4,
    "speed_limit": {"value": 0, "unit": "MB"},
    "download_folder": "descargas",
    "listener_enabled": True,
    "listener_chat_ids": [],
}
SPEED_MULTIPLIERS = {"KB": 1024, "MB": 1024**2, "GB": 1024**3}
MAX_STATE_ITEMS = 1000
MAX_MESSAGES_PER_JOB = 5000


def _bounded_int(value: Any, default: int, minimum: int, maximum: int, strict: bool) -> int:
    if isinstance(value, bool):
        if strict:
            raise ValueError("Debe ser un número entero")
        return default
    try:
        result = int(value)
    except (TypeError, ValueError):
        if strict:
            raise ValueError("Debe ser un número entero")
        return default
    if not minimum <= result <= maximum:
        if strict:
            raise ValueError(f"Debe estar entre {minimum} y {maximum}")
        return default
    return result


def normalize_config(raw: Any, strict: bool = False) -> Dict[str, Any]:
    raw = raw if isinstance(raw, dict) else {}
    speed = raw.get("speed_limit", {})
    speed = speed if isinstance(speed, dict) else {}

    value = speed.get("value", DEFAULT_CONFIG["speed_limit"]["value"])
    try:
        value = float(value or 0)
    except (TypeError, ValueError):
        if strict:
            raise ValueError("El límite de velocidad debe ser numérico")
        value = 0.0
    if value < 0 or not math.isfinite(value):
        if strict:
            raise ValueError("El límite de velocidad no es válido")
        value = 0.0

    unit = str(speed.get("unit", "MB")).upper()
    if unit not in SPEED_MULTIPLIERS:
        if strict:
            raise ValueError("La unidad debe ser KB, MB o GB")
        unit = "MB"

    folder = raw.get("download_folder", DEFAULT_CONFIG["download_folder"])
    folder = str(folder).strip() or DEFAULT_CONFIG["download_folder"]

    raw_chat_ids = raw.get("listener_chat_ids", [])
    if isinstance(raw_chat_ids, str):
        raw_chat_ids = [part.strip() for part in raw_chat_ids.split(",") if part.strip()]
    if not isinstance(raw_chat_ids, list):
        if strict:
            raise ValueError("Los IDs de escucha deben ser una lista")
        raw_chat_ids = []
    chat_ids = []
    for raw_chat_id in raw_chat_ids:
        try:
            chat_id = int(raw_chat_id)
        except (TypeError, ValueError):
            if strict:
                raise ValueError("Cada ID de escucha debe ser numérico")
            continue
        if chat_id not in chat_ids:
            chat_ids.append(chat_id)

    return {
        "max_concurrent_downloads": _bounded_int(
            raw.get("max_concurrent_downloads", DEFAULT_CONFIG["max_concurrent_downloads"]),
            DEFAULT_CONFIG["max_concurrent_downloads"],
            1,
            32,
            strict,
        ),
        "parallel_chunks": bool(raw.get("parallel_chunks", DEFAULT_CONFIG["parallel_chunks"])),
        "chunk_workers": _bounded_int(
            raw.get("chunk_workers", DEFAULT_CONFIG["chunk_workers"]),
            DEFAULT_CONFIG["chunk_workers"],
            1,
            16,
            strict,
        ),
        "speed_limit": {"value": value, "unit": unit},
        "download_folder": folder,
        "listener_enabled": bool(raw.get("listener_enabled", DEFAULT_CONFIG["listener_enabled"])),
        "listener_chat_ids": chat_ids,
    }


def save_config(config: Dict[str, Any]) -> None:
    normalized = normalize_config(config)
    pending_path = CONFIG_PATH.with_name(f"{CONFIG_PATH.name}.pending")
    with CONFIG_WRITE_LOCK:
        with pending_path.open("w", encoding="utf-8") as config_file:
            json.dump(normalized, config_file, ensure_ascii=False, indent=2)
            config_file.write("\n")
            config_file.flush()
            os.fsync(config_file.fileno())
        os.replace(pending_path, CONFIG_PATH)


def load_config() -> Dict[str, Any]:
    if not CONFIG_PATH.exists():
        config = normalize_config(DEFAULT_CONFIG)
        save_config(config)
        return config
    try:
        with CONFIG_PATH.open("r", encoding="utf-8") as config_file:
            raw_config = json.load(config_file)
        config = normalize_config(raw_config)
        if config != raw_config:
            save_config(config)
        return config
    except (OSError, json.JSONDecodeError):
        print(f"⚠️ No se pudo leer {CONFIG_PATH.name}; se usarán los valores por defecto.")
        return normalize_config(DEFAULT_CONFIG)


def public_config(config: Dict[str, Any]) -> Dict[str, Any]:
    normalized = normalize_config(config)
    return {
        "max_concurrent_downloads": normalized["max_concurrent_downloads"],
        "parallel_chunks": normalized["parallel_chunks"],
        "chunk_workers": normalized["chunk_workers"],
        "speed_limit": normalized["speed_limit"],
        "download_folder": normalized["download_folder"],
        "listener_enabled": normalized["listener_enabled"],
        "listener_chat_ids": normalized["listener_chat_ids"],
    }


runtime_config = load_config()


# --- Runtime state ---
downloads_state: Dict[str, Dict[str, Any]] = {}
active_tasks: Dict[str, asyncio.Task] = {}
active_jobs: Dict[str, asyncio.Task] = {}
downloader_instance = None
websocket_clients: Set[WebSocket] = set()
websocket_broadcast_task: Optional[asyncio.Task] = None
websocket_broadcast_dirty = False


def websocket_snapshot() -> Dict[str, Any]:
    downloads = [
        {key: value for key, value in item.items() if key != "file_path"}
        for item in sorted(
            downloads_state.values(),
            key=lambda item: item.get("created_at", item.get("updated_at", 0)),
            reverse=True,
        )
    ]
    listener = downloader_instance.get_listener_items() if downloader_instance else []
    return {
        "type": "state",
        "downloads": downloads,
        "listener": listener,
        "settings": public_config(runtime_config),
        "server_time": time.time(),
    }


async def broadcast_websocket_state() -> None:
    global websocket_broadcast_task, websocket_broadcast_dirty
    while websocket_clients:
        websocket_broadcast_dirty = False
        payload = websocket_snapshot()
        disconnected = set()
        for client in tuple(websocket_clients):
            try:
                await client.send_json(payload)
            except Exception:
                disconnected.add(client)
        websocket_clients.difference_update(disconnected)
        if not websocket_broadcast_dirty:
            break
    websocket_broadcast_task = None


def schedule_websocket_broadcast() -> None:
    global websocket_broadcast_task, websocket_broadcast_dirty
    if not websocket_clients:
        return
    if websocket_broadcast_task is not None:
        websocket_broadcast_dirty = True
        return
    try:
        loop = asyncio.get_running_loop()
    except RuntimeError:
        return
    websocket_broadcast_task = loop.create_task(broadcast_websocket_state())


def update_state(item_id: str, **kwargs: Any) -> None:
    if item_id not in downloads_state:
        downloads_state[item_id] = {
            "id": item_id,
            "job_id": None,
            "message_id": None,
            "chat_id": None,
            "file_name": "Cargando...",
            "status": "pending",
            "progress": 0,
            "total_str": "0 B",
            "current_str": "0 B",
            "speed": "0 B/s",
            "kind": "desconocido",
            "file_path": None,
            "updated_at": time.time(),
            "created_at": time.time(),
        }
    elif "created_at" not in downloads_state[item_id]:
        # Mantener una posición estable también para estados creados antes de
        # que se añadiera este campo.
        downloads_state[item_id]["created_at"] = downloads_state[item_id].get(
            "updated_at", time.time()
        )
    downloads_state[item_id].update(kwargs)
    downloads_state[item_id]["updated_at"] = time.time()
    _prune_state()
    schedule_websocket_broadcast()


def _prune_state() -> None:
    if len(downloads_state) <= MAX_STATE_ITEMS:
        return
    terminal = sorted(
        (
            (item_id, item.get("updated_at", 0))
            for item_id, item in downloads_state.items()
            if item.get("status") in {"completed", "skipped", "failed", "cancelled"}
        ),
        key=lambda item: item[1],
    )
    for item_id, _ in terminal[: max(0, len(downloads_state) - MAX_STATE_ITEMS)]:
        downloads_state.pop(item_id, None)


# --- Dashboard API ---
app = FastAPI(title="Telegram DL API")
cors_origins = [
    origin.strip()
    for origin in os.getenv("TGDL_CORS_ORIGINS", "").split(",")
    if origin.strip()
]
if cors_origins:
    app.add_middleware(
        CORSMiddleware,
        allow_origins=cors_origins,
        allow_methods=["GET", "PUT", "POST", "DELETE"],
        allow_headers=["Content-Type"],
    )


@app.get("/api/downloads")
async def get_downloads() -> List[Dict[str, Any]]:
    return [
        {key: value for key, value in item.items() if key != "file_path"}
        for item in sorted(
            downloads_state.values(),
            key=lambda item: item.get("created_at", item.get("updated_at", 0)),
            reverse=True,
        )
    ]


@app.websocket("/api/ws")
async def dashboard_websocket(websocket: WebSocket) -> None:
    await websocket.accept()
    websocket_clients.add(websocket)
    try:
        await websocket.send_json(websocket_snapshot())
        while True:
            await websocket.receive_text()
    except WebSocketDisconnect:
        pass
    finally:
        websocket_clients.discard(websocket)


@app.delete("/api/downloads/{item_id:path}")
async def delete_download(item_id: str, delete_file: bool = True) -> Dict[str, Any]:
    item_id = item_id.strip()
    state = downloads_state.get(item_id)
    if not state:
        raise HTTPException(status_code=404, detail="La descarga no existe")
    if state.get("status") not in {"available", "completed", "skipped", "failed", "cancelled"}:
        raise HTTPException(status_code=409, detail="No se puede borrar una descarga activa")

    file_path = state.get("file_path")
    deleted_file = False
    if file_path and delete_file:
        target = Path(file_path).expanduser().resolve()
        download_root = Path(downloader_instance.download_folder if downloader_instance else runtime_config["download_folder"])
        if not download_root.is_absolute():
            download_root = BASE_DIR / download_root
        download_root = download_root.resolve()
        if target != download_root and download_root not in target.parents:
            raise HTTPException(status_code=403, detail="La ruta del archivo no es válida")
        try:
            if target.exists():
                if not target.is_file():
                    raise HTTPException(status_code=409, detail="La ruta no corresponde a un archivo")
                target.unlink()
                deleted_file = True
        except OSError as error:
            raise HTTPException(status_code=500, detail=f"No se pudo borrar el archivo: {error}") from error

    downloads_state.pop(item_id, None)
    if downloader_instance:
        downloader_instance.listener_messages.pop(item_id, None)
    schedule_websocket_broadcast()
    return {"status": "ok", "id": item_id, "deleted_file": deleted_file}


@app.get("/api/settings")
async def get_settings() -> Dict[str, Any]:
    return {"status": "ok", "settings": public_config(runtime_config)}


@app.get("/api/listener/settings")
async def get_listener_settings() -> Dict[str, Any]:
    return {
        "status": "ok",
        "enabled": runtime_config["listener_enabled"],
        "chat_ids": runtime_config["listener_chat_ids"],
    }


@app.get("/api/listener")
async def get_listener_items() -> List[Dict[str, Any]]:
    if not downloader_instance:
        return []
    return downloader_instance.get_listener_items()


def _resolve_browse_path(raw_path: Optional[str]) -> Path:
    if not raw_path:
        raise ValueError("Ruta requerida")
    path = Path(raw_path).expanduser()
    if not path.is_absolute():
        path = BASE_DIR / path
    return path.resolve()


@app.get("/api/filesystem")
async def browse_filesystem(path: Optional[str] = None) -> Dict[str, Any]:
    if not path:
        if os.name == "nt":
            roots = [
                f"{letter}:\\"
                for letter in string.ascii_uppercase
                if os.path.isdir(f"{letter}:\\")
            ]
        else:
            roots = ["/"]
        return {"status": "ok", "roots": roots, "path": None, "parent": None, "entries": []}

    try:
        current_path = _resolve_browse_path(path)
    except ValueError as error:
        raise HTTPException(status_code=422, detail=str(error)) from error
    if not current_path.is_dir():
        raise HTTPException(status_code=404, detail="La carpeta no existe o no es accesible")

    entries = []
    try:
        children = sorted(current_path.iterdir(), key=lambda child: (not child.is_dir(), child.name.lower()))
        for child in children:
            try:
                if child.is_dir():
                    entries.append({"name": child.name, "path": str(child), "is_dir": True})
            except OSError:
                continue
    except OSError as error:
        raise HTTPException(status_code=403, detail=f"No se puede leer la carpeta: {error}") from error

    parent = current_path.parent
    if parent == current_path:
        parent_path = None
    elif os.name == "nt" and len(current_path.parts) <= 1:
        parent_path = None
    else:
        parent_path = str(parent)
    return {
        "status": "ok",
        "roots": [],
        "path": str(current_path),
        "parent": parent_path,
        "entries": entries,
    }


async def apply_and_save_settings(data: Dict[str, Any]) -> Dict[str, Any]:
    global runtime_config
    candidate = dict(runtime_config)
    candidate.update({key: value for key, value in data.items() if key in candidate})
    if "speed_limit" in data:
        candidate["speed_limit"] = data["speed_limit"]
    try:
        normalized = normalize_config(candidate, strict=True)
    except ValueError as error:
        raise HTTPException(status_code=422, detail=str(error)) from error

    save_config(normalized)
    runtime_config = normalized
    schedule_websocket_broadcast()
    if downloader_instance:
        await downloader_instance.apply_settings(normalized)
    return public_config(normalized)


@app.put("/api/settings")
async def set_settings(data: Dict[str, Any] = Body(...)) -> Dict[str, Any]:
    settings = await apply_and_save_settings(data)
    return {"status": "ok", "settings": settings}


@app.put("/api/listener/settings")
async def set_listener_settings(data: Dict[str, Any] = Body(...)) -> Dict[str, Any]:
    settings = await apply_and_save_settings(
        {
            "listener_enabled": data.get("enabled", True),
            "listener_chat_ids": data.get("chat_ids", []),
        }
    )
    return {
        "status": "ok",
        "enabled": settings["listener_enabled"],
        "chat_ids": settings["listener_chat_ids"],
    }


@app.post("/api/settings/speed")
async def set_speed_limit(data: Dict[str, Any] = Body(...)) -> Dict[str, Any]:
    settings = await apply_and_save_settings(
        {"speed_limit": {"value": data.get("value", 0), "unit": data.get("unit", "MB")}}
    )
    return {"status": "ok", "settings": settings}


@app.post("/api/cancel")
async def cancel_download(payload: Dict[str, Any]) -> Dict[str, Any]:
    item_id = str(payload.get("id", "")).strip()
    if not item_id:
        raise HTTPException(status_code=422, detail="ID requerido")

    state = downloads_state.get(item_id)
    if not state:
        return {"status": "ok", "message": "La descarga ya no está activa"}
    if state.get("status") in {"completed", "skipped", "failed", "cancelled"}:
        return {"status": "ok", "message": "La descarga ya terminó"}

    task = active_tasks.get(item_id)
    if task and not task.done():
        task.cancel()
        with suppress(asyncio.CancelledError, Exception):
            await task

    state = downloads_state.get(item_id, state)
    file_path = state.get("file_path")
    if file_path and downloader_instance:
        downloader_instance._cleanup_files(file_path)
    update_state(item_id, status="cancelled", progress=0)
    active_tasks.pop(item_id, None)
    return {"status": "ok"}


async def _set_download_pause(item_id: str, paused: bool) -> Dict[str, Any]:
    if not downloader_instance:
        raise HTTPException(status_code=503, detail="El cliente no está listo")
    state = downloads_state.get(item_id)
    if not state:
        raise HTTPException(status_code=404, detail="La descarga no existe")
    if state.get("status") in {"completed", "skipped", "failed", "cancelled"}:
        raise HTTPException(status_code=409, detail="La descarga ya terminó")
    if state.get("status") == "pending":
        raise HTTPException(status_code=409, detail="La descarga todavía no está preparada")

    event = downloader_instance.pause_events.setdefault(item_id, asyncio.Event())
    if not event.is_set() and not paused:
        event.set()
    elif event.is_set() and paused:
        event.clear()

    if paused:
        update_state(item_id, status="paused")
    else:
        update_state(item_id, status="downloading")
    return {"status": "ok", "id": item_id, "paused": paused}


@app.post("/api/pause")
async def pause_download(payload: Dict[str, Any]) -> Dict[str, Any]:
    item_id = str(payload.get("id", "")).strip()
    if not item_id:
        raise HTTPException(status_code=422, detail="ID requerido")
    return await _set_download_pause(item_id, True)


@app.post("/api/resume")
async def resume_download(payload: Dict[str, Any]) -> Dict[str, Any]:
    item_id = str(payload.get("id", "")).strip()
    if not item_id:
        raise HTTPException(status_code=422, detail="ID requerido")
    return await _set_download_pause(item_id, False)


@app.post("/api/download")
async def start_download(data: Dict[str, Any] = Body(...)) -> Dict[str, Any]:
    url = str(data.get("url", "")).strip()
    if not url:
        raise HTTPException(status_code=422, detail="URL requerida")
    if not downloader_instance:
        raise HTTPException(status_code=503, detail="El cliente no está listo")

    try:
        parsed = downloader_instance.parse_url(url)
    except ValueError as error:
        raise HTTPException(status_code=422, detail=str(error)) from error

    _, start_id, end_id = parsed
    if end_id - start_id + 1 > MAX_MESSAGES_PER_JOB:
        raise HTTPException(
            status_code=422,
            detail=f"El rango máximo es de {MAX_MESSAGES_PER_JOB} mensajes",
        )

    job_id = uuid.uuid4().hex
    task = asyncio.create_task(
        downloader_instance.download_from_url(url, job_id=job_id, parsed=parsed)
    )
    active_jobs[job_id] = task
    task.add_done_callback(lambda finished, current_job=job_id: active_jobs.pop(current_job, None))
    return {
        "status": "ok",
        "job_id": job_id,
        "max_concurrent_downloads": downloader_instance.max_concurrent_downloads,
    }


@app.post("/api/listener/download")
async def download_listener_item(data: Dict[str, Any] = Body(...)) -> Dict[str, Any]:
    item_id = str(data.get("id", "")).strip()
    if not downloader_instance:
        raise HTTPException(status_code=503, detail="El cliente no está listo")
    listener_item = downloader_instance.listener_messages.get(item_id)
    if not listener_item:
        raise HTTPException(status_code=404, detail="Multimedia no encontrado")
    message, chat_id = listener_item
    state = downloads_state.get(item_id, {})
    if state.get("status") in {"queued", "downloading", "completed"}:
        return {"status": "ok", "message": "La multimedia ya está gestionándose"}
    update_state(item_id, status="queued")
    task = asyncio.create_task(
        downloader_instance.download_file_from_message(message, chat_id, item_id)
    )
    active_tasks[item_id] = task
    task.add_done_callback(
        lambda finished, current_item=item_id: active_tasks.pop(current_item, None)
    )
    return {"status": "ok"}


@app.get("/")
async def root() -> RedirectResponse:
    return RedirectResponse(url="/dashboard/")


dashboard_path = BASE_DIR / "dashboard" / "dist"
if dashboard_path.exists():
    app.mount("/dashboard", StaticFiles(directory=dashboard_path, html=True), name="dashboard")


def get_local_ip() -> str:
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    try:
        sock.connect(("10.255.255.255", 1))
        return sock.getsockname()[0]
    except Exception:
        return "127.0.0.1"
    finally:
        sock.close()


async def run_server() -> None:
    host = os.getenv("TGDL_BIND_HOST", "127.0.0.1")
    try:
        port = int(os.getenv("TGDL_PORT", "8000"))
    except ValueError:
        port = 8000
    config = uvicorn.Config(app, host=host, port=port, log_level="info")
    server = uvicorn.Server(config)
    await server.serve()


# --- Core download logic ---
@dataclass
class MediaInfo:
    media: object
    file_name: str
    kind: str
    file_size: int


class DownloadProgress:
    def __init__(
        self,
        item_id: str,
        file_name: str,
        total: int = 0,
        initial: int = 0,
        kind: str = "archivo",
    ):
        self.item_id = item_id
        self.file_name = file_name
        self.total = total or 0
        self.current = initial
        self.kind = kind
        self.started_at = time.monotonic()
        self.last_print = 0.0
        update_state(
            item_id,
            file_name=file_name,
            status="downloading",
            total_str=format_bytes(total),
            kind=kind,
        )

    def update(self, current: int, total: Optional[int] = None, force: bool = False) -> None:
        if total:
            self.total = total
        self.current = min(current, self.total) if self.total else current
        now = time.monotonic()
        elapsed = max(now - self.started_at, 0.001)
        speed_value = max(self.current, 0) / elapsed
        percentage = (self.current / self.total) * 100 if self.total else 0
        update_state(
            self.item_id,
            progress=percentage,
            current_str=format_bytes(self.current),
            total_str=format_bytes(self.total),
            speed=f"{format_bytes(speed_value)}/s",
        )

        if not force and now - self.last_print < 1.5:
            return
        self.last_print = now
        with PRINT_LOCK:
            print(
                f"📥 {self.file_name} - {percentage:5.1f}% "
                f"({format_bytes(speed_value)}/s)"
            )


class TelegramDownloader:
    def __init__(self, api_id: str, api_hash: str):
        self.config = normalize_config(runtime_config)
        self.parallel_chunks = self.config["parallel_chunks"]
        self.chunk_workers = self.config["chunk_workers"]
        self.max_concurrent_downloads = self.config["max_concurrent_downloads"]
        self.listener_enabled = self.config["listener_enabled"]
        self.watched_chat_ids = set(self.config["listener_chat_ids"])
        self.listener_messages: Dict[str, Tuple[Any, Any]] = {}
        self.pause_events: Dict[str, asyncio.Event] = {}

        self.speed_limit: Optional[float] = None
        self.bytes_downloaded_since_limit_set = 0
        self.limit_start_time: Optional[float] = None
        self.limit_lock = asyncio.Lock()
        self._concurrency_condition = asyncio.Condition()
        self._active_file_downloads = 0

        self.client = Client(
            "downloader_session",
            api_id=api_id,
            api_hash=api_hash,
            no_updates=False,
            max_concurrent_transmissions=10,
        )
        self.client.add_handler(MessageHandler(self._on_new_media, filters.media))
        folder = Path(self.config["download_folder"])
        if not folder.is_absolute():
            folder = BASE_DIR / folder
        self.download_folder = folder
        self.download_folder.mkdir(parents=True, exist_ok=True)
        self._reserved_paths: Set[str] = set()
        self._reserved_paths_lock = asyncio.Lock()

        self._set_speed_limit_from_config(self.config)

    def _set_speed_limit_from_config(self, config: Dict[str, Any]) -> None:
        speed = config["speed_limit"]
        value = float(speed["value"])
        self.speed_limit = value * SPEED_MULTIPLIERS[speed["unit"]] if value > 0 else None
        self.bytes_downloaded_since_limit_set = 0
        self.limit_start_time = time.monotonic() if self.speed_limit else None

    async def apply_settings(self, config: Dict[str, Any]) -> None:
        normalized = normalize_config(config, strict=True)
        self.config = normalized
        self.parallel_chunks = normalized["parallel_chunks"]
        self.chunk_workers = normalized["chunk_workers"]
        self.listener_enabled = normalized["listener_enabled"]
        self.watched_chat_ids = set(normalized["listener_chat_ids"])
        folder = Path(normalized["download_folder"])
        if not folder.is_absolute():
            folder = BASE_DIR / folder
        folder.mkdir(parents=True, exist_ok=True)
        self.download_folder = folder

        async with self.limit_lock:
            self._set_speed_limit_from_config(normalized)
        async with self._concurrency_condition:
            self.max_concurrent_downloads = normalized["max_concurrent_downloads"]
            self._concurrency_condition.notify_all()

    def parse_url(self, url: str) -> Tuple[Any, int, int]:
        clean_url = url.strip()
        patterns = (
            r"^https://t\.me/c/(\d+)/(\d+)(?:-(\d+))?(?:[?#].*)?$",
            r"^https://t\.me/([A-Za-z0-9_]{1,64})/(\d+)(?:-(\d+))?(?:[?#].*)?$",
        )
        for pattern in patterns:
            match = re.match(pattern, clean_url)
            if not match:
                continue
            chat = match.group(1)
            start = int(match.group(2))
            end = int(match.group(3) or match.group(2))
            if pattern.startswith(r"^https://t\.me/c/"):
                chat = int(f"-100{chat}")
            if end < start:
                raise ValueError("El mensaje final no puede ser menor que el inicial")
            return chat, start, end
        raise ValueError("URL no válida")

    async def throttle(self, bytes_count: int) -> None:
        if not self.speed_limit:
            return

        async with self.limit_lock:
            if not self.limit_start_time:
                self.limit_start_time = time.monotonic()
                self.bytes_downloaded_since_limit_set = 0
            self.bytes_downloaded_since_limit_set += bytes_count
            elapsed = time.monotonic() - self.limit_start_time
            expected_time = self.bytes_downloaded_since_limit_set / self.speed_limit
            sleep_time = max(0.0, expected_time - elapsed)

        if sleep_time > 0.001:
            await asyncio.sleep(sleep_time)

    async def _acquire_download_slot(self) -> None:
        async with self._concurrency_condition:
            while self._active_file_downloads >= self.max_concurrent_downloads:
                await self._concurrency_condition.wait()
            self._active_file_downloads += 1

    async def _release_download_slot(self) -> None:
        async with self._concurrency_condition:
            self._active_file_downloads = max(0, self._active_file_downloads - 1)
            self._concurrency_condition.notify_all()

    def get_listener_items(self) -> List[Dict[str, Any]]:
        return [
            {key: value for key, value in item.items() if key != "file_path"}
            for item in sorted(
                (
                    item
                    for item in downloads_state.values()
                    if item.get("source") == "listener" and item.get("status") == "available"
                ),
                key=lambda item: item.get("updated_at", 0),
                reverse=True,
            )[:200]
        ]

    async def _on_new_media(self, client: Client, message: Any) -> None:
        if not self.listener_enabled or not message.chat:
            return
        chat_id = message.chat.id
        if chat_id not in self.watched_chat_ids:
            return
        info = self._extract_media_info(message)
        if not info:
            return

        item_id = f"listener:{chat_id}:{message.id}"
        self.listener_messages[item_id] = (message, chat_id)
        update_state(
            item_id,
            job_id=f"listener:{chat_id}",
            message_id=message.id,
            chat_id=chat_id,
            file_name=sanitize_file_name(info.file_name) or f"file_{message.id}",
            total_str=format_bytes(info.file_size),
            kind=info.kind,
            source="listener",
            status="available",
            progress=0,
        )
        if len(self.listener_messages) > 300:
            oldest = sorted(
                self.listener_messages,
                key=lambda key: downloads_state.get(key, {}).get("updated_at", 0),
            )[:50]
            for old_item_id in oldest:
                self.listener_messages.pop(old_item_id, None)
                downloads_state.pop(old_item_id, None)

    @staticmethod
    def _item_id(job_id: str, message_id: int) -> str:
        return f"{job_id}:{message_id}"

    async def download_from_url(
        self,
        url: str,
        job_id: Optional[str] = None,
        parsed: Optional[Tuple[Any, int, int]] = None,
    ) -> None:
        job_id = job_id or uuid.uuid4().hex
        try:
            chat_id, start_id, end_id = parsed or self.parse_url(url)
            for message_id in range(start_id, end_id + 1):
                update_state(
                    self._item_id(job_id, message_id),
                    job_id=job_id,
                    message_id=message_id,
                    chat_id=chat_id,
                    status="pending",
                    file_name=f"Mensaje {message_id}",
                )

            for batch_start in range(start_id, end_id + 1, MAX_GET_MESSAGES_BATCH):
                batch_end = min(batch_start + MAX_GET_MESSAGES_BATCH - 1, end_id)
                batch_ids = list(range(batch_start, batch_end + 1))
                messages = await self.client.get_messages(chat_id, batch_ids)
                if not isinstance(messages, list):
                    messages = [messages]
                message_map = {
                    message.id: message
                    for message in messages
                    if message and getattr(message, "id", None) is not None
                }

                tasks: List[asyncio.Task] = []
                for message_id in batch_ids:
                    item_id = self._item_id(job_id, message_id)
                    message = message_map.get(message_id)
                    if not message or message.empty:
                        update_state(item_id, status="skipped")
                        continue

                    update_state(item_id, status="queued")
                    task = asyncio.create_task(
                        self.download_file_from_message(message, chat_id, item_id)
                    )
                    active_tasks[item_id] = task
                    task.add_done_callback(
                        lambda finished, current_item=item_id: active_tasks.pop(
                            current_item, None
                        )
                    )
                    tasks.append(task)

                if tasks:
                    await asyncio.gather(*tasks, return_exceptions=True)
        except asyncio.CancelledError:
            raise
        except Exception as error:
            print(f"❌ Error en el trabajo {job_id}: {error}")

    async def download_file_from_message(self, message: Any, chat_id: Any, item_id: str) -> None:
        pause_event = self.pause_events.get(item_id)
        if pause_event is None:
            pause_event = asyncio.Event()
            pause_event.set()
            self.pause_events[item_id] = pause_event
        await pause_event.wait()
        await self._acquire_download_slot()
        reserved_path: Optional[str] = None
        try:
            info = self._extract_media_info(message)
            if not info:
                update_state(item_id, status="skipped")
                return

            file_path, file_name = await self._reserve_download_path(
                info.file_name, message.id
            )
            reserved_path = file_path
            update_state(
                item_id,
                file_name=file_name,
                kind=info.kind,
                total_str=format_bytes(info.file_size),
                file_path=file_path,
            )

            workers = self.chunk_workers if self.parallel_chunks else 1
            await self._download_manual(info, file_path, file_name, item_id, workers)
            update_state(item_id, status="completed", progress=100)
        except asyncio.CancelledError:
            if reserved_path:
                self._cleanup_files(reserved_path)
            update_state(item_id, status="cancelled", progress=0)
            raise
        except Exception as error:
            if reserved_path:
                self._cleanup_files(reserved_path)
            print(f"❌ Error en la descarga {item_id}: {error}")
            update_state(item_id, status="failed")
        finally:
            if reserved_path:
                await self._release_download_path(reserved_path)
            await self._release_download_slot()
            self.pause_events.pop(item_id, None)

    async def _fetch_chunk(self, file_id: FileId, file_size: int, index: int) -> bytes:
        expected_size = min(CHUNK_SIZE, file_size - index * CHUNK_SIZE)
        last_error: Optional[Exception] = None
        for attempt in range(3):
            try:
                chunks: List[bytes] = []
                async for chunk in self.client.get_file(
                    file_id,
                    file_size=file_size,
                    limit=1,
                    offset=index,
                ):
                    chunks.append(chunk)
                data = b"".join(chunks)
                if len(data) != expected_size:
                    raise IOError(
                        f"Chunk incompleto: {len(data)} de {expected_size} bytes"
                    )
                return data
            except asyncio.CancelledError:
                raise
            except FloodWait as error:
                last_error = error
                if attempt == 2:
                    raise
                await asyncio.sleep(max(1, int(getattr(error, "value", 1))))
            except Exception as error:
                last_error = error
                if attempt == 2:
                    raise
                await asyncio.sleep(2**attempt)
        raise last_error or IOError("No se pudo descargar el chunk")

    async def _download_manual(
        self,
        info: MediaInfo,
        file_path: str,
        file_name: str,
        item_id: str,
        workers_count: int,
    ) -> None:
        file_size = int(info.file_size or 0)
        if file_size < 0:
            raise ValueError("Tamaño de archivo inválido")
        file_id = FileId.decode(info.media.file_id)
        temp_path = f"{file_path}.temp"
        total_chunks = math.ceil(file_size / CHUNK_SIZE)
        progress = DownloadProgress(item_id, file_name, file_size, kind=info.kind)

        with open(temp_path, "w+b") as file_handle:
            file_handle.truncate(file_size)

        queue: asyncio.Queue[int] = asyncio.Queue()
        for index in range(total_chunks):
            queue.put_nowait(index)

        downloaded = 0
        errors: List[Exception] = []

        def drain_queue() -> None:
            while True:
                try:
                    queue.get_nowait()
                except asyncio.QueueEmpty:
                    return
                else:
                    queue.task_done()

        async def worker() -> None:
            nonlocal downloaded
            pause_event = self.pause_events.setdefault(item_id, asyncio.Event())
            while True:
                await pause_event.wait()
                try:
                    index = queue.get_nowait()
                except asyncio.QueueEmpty:
                    return
                try:
                    chunk_data = await self._fetch_chunk(file_id, file_size, index)
                    with open(temp_path, "r+b") as file_handle:
                        file_handle.seek(index * CHUNK_SIZE)
                        file_handle.write(chunk_data)
                    downloaded += len(chunk_data)
                    progress.update(downloaded)
                    await self.throttle(len(chunk_data))
                except asyncio.CancelledError:
                    raise
                except Exception as error:
                    errors.append(error)
                    drain_queue()
                    return
                finally:
                    queue.task_done()

        worker_tasks = [
            asyncio.create_task(worker())
            for _ in range(min(max(1, workers_count), total_chunks))
        ]
        try:
            await queue.join()
        finally:
            for task in worker_tasks:
                task.cancel()
            await asyncio.gather(*worker_tasks, return_exceptions=True)

        if errors:
            raise RuntimeError(f"Falló un chunk: {errors[0]}") from errors[0]
        if not os.path.exists(temp_path):
            raise IOError("No se generó el archivo temporal")
        os.replace(temp_path, file_path)

    def _extract_media_info(self, message: Any) -> Optional[MediaInfo]:
        media = (
            message.document
            or message.video
            or message.audio
            or message.photo
            or message.voice
            or message.video_note
            or message.animation
            or message.sticker
        )
        if not media:
            return None
        name = getattr(media, "file_name", None) or f"file_{message.id}"
        if message.photo:
            name = f"photo_{message.id}.jpg"
        return MediaInfo(
            media,
            name,
            "media",
            int(getattr(media, "file_size", 0) or 0),
        )

    async def _reserve_download_path(self, name: str, message_id: int) -> Tuple[str, str]:
        safe_name = sanitize_file_name(name)
        if not safe_name:
            safe_name = f"file_{message_id}"
        stem, extension = os.path.splitext(safe_name)
        candidate = safe_name
        counter = 1
        async with self._reserved_paths_lock:
            while True:
                path = str(self.download_folder / candidate)
                if not os.path.exists(path) and path not in self._reserved_paths:
                    self._reserved_paths.add(path)
                    return path, candidate
                counter += 1
                candidate = f"{stem} ({counter}){extension}"

    async def _release_download_path(self, path: str) -> None:
        async with self._reserved_paths_lock:
            self._reserved_paths.discard(path)

    def _cleanup_files(self, path: str) -> None:
        for candidate in (path, f"{path}.temp", f"{path}.temp.state.json"):
            try:
                os.remove(candidate)
            except FileNotFoundError:
                pass
            except OSError as error:
                print(f"⚠️ No se pudo limpiar {candidate}: {error}")


# --- Utilities ---
CHUNK_SIZE = 1024 * 1024
MAX_GET_MESSAGES_BATCH = 200
PRINT_LOCK = threading.Lock()
INVALID_FILENAME_CHARS = re.compile(r'[<>:"/\\|?*\x00-\x1f]')
WINDOWS_RESERVED_NAMES = {
    "CON",
    "PRN",
    "AUX",
    "NUL",
    "COM1",
    "COM2",
    "COM3",
    "COM4",
    "LPT1",
    "LPT2",
    "LPT3",
}


def sanitize_file_name(name: Any) -> str:
    clean = INVALID_FILENAME_CHARS.sub("_", str(name)).strip().rstrip(". ")
    if clean.upper().split(".", 1)[0] in WINDOWS_RESERVED_NAMES:
        clean = f"_{clean}"
    return clean[:240].rstrip(". ")


def format_bytes(size: float) -> str:
    value = float(size or 0)
    for unit in ("B", "KB", "MB", "GB"):
        if value < 1024:
            return f"{value:.1f} {unit}"
        value /= 1024
    return f"{value:.1f} TB"


async def main() -> None:
    global downloader_instance
    print("🤖 TG Downloader Pro")
    api_id = os.getenv("TGDL_API_ID")
    api_hash = os.getenv("TGDL_API_HASH")
    if not api_id or not api_hash:
        print("❌ Define TGDL_API_ID y TGDL_API_HASH antes de iniciar.")
        return

    downloader_instance = TelegramDownloader(api_id, api_hash)
    server_task = asyncio.create_task(run_server())
    host = os.getenv("TGDL_BIND_HOST", "127.0.0.1")
    visible_host = get_local_ip() if host == "0.0.0.0" else host
    print(f"🌐 Dashboard: http://{visible_host}:{os.getenv('TGDL_PORT', '8000')}/dashboard/")

    try:
        await downloader_instance.client.start()
        print("✅ Conectado. Esperando órdenes desde la web...")
        while True:
            await asyncio.sleep(3600)
    finally:
        server_task.cancel()
        with suppress(asyncio.CancelledError):
            await server_task
        with suppress(Exception):
            await downloader_instance.client.stop()


if __name__ == "__main__":
    asyncio.run(main())
