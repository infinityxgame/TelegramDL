import asyncio
import json
import math
import os
import re
import socket
import sys
import threading
import time
import uuid
from contextlib import suppress
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Dict, List, Optional, Set, Tuple

from dotenv import load_dotenv
from pyrogram import Client
from pyrogram.errors import FloodWait
from pyrogram.file_id import FileId

try:
    from fastapi import Body, FastAPI, HTTPException
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
            return normalize_config(json.load(config_file))
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
    }


runtime_config = load_config()


# --- Runtime state ---
downloads_state: Dict[str, Dict[str, Any]] = {}
active_tasks: Dict[str, asyncio.Task] = {}
active_jobs: Dict[str, asyncio.Task] = {}
downloader_instance = None


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
        }
    downloads_state[item_id].update(kwargs)
    downloads_state[item_id]["updated_at"] = time.time()
    _prune_state()


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
        allow_methods=["GET", "PUT", "POST"],
        allow_headers=["Content-Type"],
    )


@app.get("/api/downloads")
async def get_downloads() -> List[Dict[str, Any]]:
    return [
        {key: value for key, value in item.items() if key != "file_path"}
        for item in sorted(
            downloads_state.values(),
            key=lambda item: item.get("updated_at", 0),
            reverse=True,
        )
    ]


@app.get("/api/settings")
async def get_settings() -> Dict[str, Any]:
    return {"status": "ok", "settings": public_config(runtime_config)}


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
    if downloader_instance:
        await downloader_instance.apply_settings(normalized)
    return public_config(normalized)


@app.put("/api/settings")
async def set_settings(data: Dict[str, Any] = Body(...)) -> Dict[str, Any]:
    settings = await apply_and_save_settings(data)
    return {"status": "ok", "settings": settings}


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
            no_updates=True,
            max_concurrent_transmissions=10,
        )
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
            while True:
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
