import asyncio
# Solución para compatibilidad con Python 3.14
asyncio.set_event_loop(asyncio.new_event_loop())

import json
import math
import mimetypes
import os
import re
import sys
import threading
import time
import socket
from dataclasses import dataclass
from typing import Dict, Iterable, List, Optional, Set, Tuple, Union

from pyrogram import Client
from pyrogram.errors import ChannelPrivate, FloodWait, MessageIdInvalid
from pyrogram.file_id import FileId

# Nuevas importaciones para el Dashboard
try:
    from fastapi import FastAPI, BackgroundTasks, Body
    from fastapi.middleware.cors import CORSMiddleware
    from fastapi.staticfiles import StaticFiles
    from fastapi.responses import RedirectResponse
    import uvicorn
except ImportError:
    print("❌ Error: Faltan dependencias. Por favor ejecuta: pip install fastapi uvicorn jinja2")
    sys.exit(1)

# --- Global State ---
downloads_state = {}
active_tasks = {}
downloader_instance = None

def update_state(msg_id, **kwargs):
    if msg_id not in downloads_state:
        downloads_state[msg_id] = {
            "id": msg_id,
            "file_name": "Cargando...",
            "status": "pending",
            "progress": 0,
            "total_str": "0 B",
            "current_str": "0 B",
            "speed": "0 B/s",
            "kind": "desconocido",
            "file_path": None,
            "updated_at": time.time()
        }
    downloads_state[msg_id].update(kwargs)
    downloads_state[msg_id]["updated_at"] = time.time()

# --- Dashboard API ---
app = FastAPI()
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_methods=["*"],
    allow_headers=["*"],
)

@app.get("/api/downloads")
async def get_downloads():
    return list(downloads_state.values())

@app.post("/api/cancel")
async def cancel_download(payload: dict):
    msg_id = payload.get("id")
    if msg_id is None:
        return {"error": "ID requerido"}

    task = active_tasks.get(msg_id)
    if task:
        task.cancel()

    state = downloads_state.get(msg_id)
    if state and state.get("file_path"):
        path = state["file_path"]
        for p in (path, f"{path}.temp", f"{path}.temp.state.json"):
            try:
                if os.path.exists(p):
                    os.remove(p)
            except:
                pass

    if msg_id in downloads_state:
        del downloads_state[msg_id]
    if msg_id in active_tasks:
        del active_tasks[msg_id]

    return {"status": "ok"}

@app.post("/api/download")
async def start_download(background_tasks: BackgroundTasks, data: dict = Body(...)):
    url = data.get("url")
    parallel = data.get("parallel")

    if isinstance(parallel, str):
        parallel = parallel.lower() == "true"
    elif parallel is None:
        parallel = True

    if not url:
        return {"error": "URL requerida"}

    if not downloader_instance:
        return {"error": "El cliente no está listo"}

    # Actualizar modo en la instancia
    downloader_instance.parallel_mode = parallel

    mode_str = "PARALELO" if parallel else "SECUENCIAL"
    print(f"🚀 Petición recibida: Modo {mode_str}")

    background_tasks.add_task(downloader_instance.download_from_url, url)
    return {"status": "ok", "mode": mode_str}

@app.get("/")
async def root():
    return RedirectResponse(url="/dashboard/")

dashboard_path = os.path.join(os.path.dirname(__file__), "dashboard")
if os.path.exists(dashboard_path):
    app.mount("/dashboard", StaticFiles(directory=dashboard_path, html=True), name="dashboard")

def get_local_ip():
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    try:
        s.connect(('10.255.255.255', 1))
        IP = s.getsockname()[0]
    except Exception:
        IP = '127.0.0.1'
    finally:
        s.close()
    return IP

async def run_server():
    config = uvicorn.Config(app, host="0.0.0.0", port=8000, log_level="error")
    server = uvicorn.Server(config)
    await server.serve()

# --- Core Logic ---

@dataclass
class MediaInfo:
    media: object
    file_name: str
    kind: str
    file_size: int

class DownloadProgress:
    def __init__(self, msg_id: int, file_name: str, total: int = 0, initial: int = 0, kind: str = "archivo"):
        self.msg_id = msg_id
        self.file_name = file_name
        self.total = total or 0
        self.current = initial
        self.kind = kind
        self.started_at = time.monotonic()
        self.last_print = 0.0
        update_state(msg_id, file_name=file_name, status="downloading", total_str=format_bytes(total), kind=kind)

    def update(self, current: int, total: Optional[int] = None, force: bool = False) -> None:
        if total: self.total = total
        self.current = min(current, self.total) if self.total else current
        now = time.monotonic()
        elapsed = max(now - self.started_at, 0.001)
        speed_val = max(self.current, 0) / elapsed
        speed_str = f"{format_bytes(speed_val)}/s"
        percentage = (self.current / self.total) * 100 if self.total else 0

        update_state(self.msg_id, progress=percentage, current_str=format_bytes(self.current), total_str=format_bytes(self.total), speed=speed_str)

        if not force and now - self.last_print < 1.5: return
        self.last_print = now
        with PRINT_LOCK:
            print(f"📥 {self.file_name} - {percentage:5.1f}% ({speed_str})")

class TelegramDownloader:
    def __init__(self, api_id, api_hash):
        self.parallel_mode = True
        self.client = Client("downloader_session", api_id=api_id, api_hash=api_hash, no_updates=True, max_concurrent_transmissions=10)
        self.download_folder = os.path.join(os.path.dirname(os.path.abspath(__file__)), "descargas")
        os.makedirs(self.download_folder, exist_ok=True)
        self._reserved_paths: Set[str] = set()
        self._reserved_paths_lock = asyncio.Lock()
        self.global_lock = asyncio.Lock() # Bloqueo para modo secuencial

    def parse_url(self, url: str):
        clean_url = url.strip()
        p1 = r"https://t\.me/c/(\d+)/(\d+)(?:-(\d+))?"
        m = re.match(p1, clean_url)
        if m: return int(f"-100{m.group(1)}"), int(m.group(2)), int(m.group(3)) if m.group(3) else int(m.group(2))
        p2 = r"https://t\.me/([^/]+)/(\d+)(?:-(\d+))?"
        m = re.match(p2, clean_url)
        if m: return m.group(1), int(m.group(2)), int(m.group(3)) if m.group(3) else int(m.group(2))
        raise ValueError("URL no válida")

    async def download_from_url(self, url):
        try:
            chat_id, start_id, end_id = self.parse_url(url)
            for msg_id in range(start_id, end_id + 1):
                update_state(msg_id, status="pending", file_name=f"Mensaje {msg_id}")

            for batch_start in range(start_id, end_id + 1, MAX_GET_MESSAGES_BATCH):
                batch_end = min(batch_start + MAX_GET_MESSAGES_BATCH - 1, end_id)
                batch_ids = list(range(batch_start, batch_end + 1))
                messages = await self.client.get_messages(chat_id, batch_ids)
                if not isinstance(messages, list): messages = [messages]

                tasks = []
                for msg in messages:
                    if not msg or msg.empty:
                        update_state(msg.id if msg else 0, status="skipped")
                        continue

                    if self.parallel_mode:
                        # MODO PARALELO: Crear y lanzar todas a la vez
                        task = asyncio.create_task(self.download_file_from_message(msg, chat_id))
                        active_tasks[msg.id] = task
                        tasks.append(task)
                    else:
                        # MODO SECUENCIAL: Solo creamos la tarea cuando sea su turno
                        update_state(msg.id, status="queued")
                        async with self.global_lock:
                            task = asyncio.create_task(self.download_file_from_message(msg, chat_id))
                            active_tasks[msg.id] = task
                            try:
                                await task
                            except asyncio.CancelledError:
                                print(f"ℹ️ Descarga {msg.id} cancelada.")
                            except Exception as e:
                                print(f"⚠️ Error en {msg.id}: {e}")
                            finally:
                                if msg.id in active_tasks:
                                    del active_tasks[msg.id]

                if tasks:
                    await asyncio.gather(*tasks, return_exceptions=True)
        except Exception as e:
            print(f"❌ Error en lote: {e}")

    async def download_file_from_message(self, message, chat_id):
        info = self._extract_media_info(message)
        if not info:
            update_state(message.id, status="skipped")
            return

        file_path, file_name, exists = await self._reserve_download_path(info.file_name, message.id)
        update_state(message.id, file_name=file_name, kind=info.kind, total_str=format_bytes(info.file_size), file_path=file_path)

        if exists:
            update_state(message.id, status="skipped", progress=100)
            return

        try:
            # Si no hay paralelo, usamos solo 1 worker manual
            workers = DEFAULT_CHUNK_WORKERS if self.parallel_mode else 1
            await self._download_manual(info, file_path, file_name, message.id, workers)
            update_state(message.id, status="completed", progress=100)
        except asyncio.CancelledError:
            self._cleanup_files(file_path)
            raise
        except Exception as e:
            print(f"❌ Error en {file_name}: {e}")
            update_state(message.id, status="failed")

    async def _download_manual(self, info, file_path, file_name, msg_id, workers_count):
        file_size = info.file_size
        file_id = FileId.decode(info.media.file_id)
        temp_path = f"{file_path}.temp"
        total_chunks = math.ceil(file_size / CHUNK_SIZE)

        progress = DownloadProgress(msg_id, file_name, file_size, kind=info.kind)

        with open(temp_path, "a+b") as f: f.truncate(file_size)

        queue = asyncio.Queue()
        for i in range(total_chunks): queue.put_nowait(i)

        downloaded = 0
        async def worker():
            nonlocal downloaded
            try:
                while not queue.empty():
                    idx = await queue.get()
                    try:
                        chunk_data = b""
                        async for chunk in self.client.get_file(file_id, file_size=file_size, limit=1, offset=idx):
                            chunk_data = chunk

                        if not os.path.exists(temp_path): return
                        with open(temp_path, "r+b") as f:
                            f.seek(idx * CHUNK_SIZE)
                            f.write(chunk_data)
                        downloaded += len(chunk_data)
                        progress.update(downloaded)
                    finally:
                        queue.task_done()
            except (asyncio.CancelledError, FileNotFoundError):
                return

        w_tasks = [asyncio.create_task(worker()) for _ in range(min(workers_count, total_chunks))]
        try:
            await queue.join()
        finally:
            for t in w_tasks: t.cancel()
            await asyncio.gather(*w_tasks, return_exceptions=True)

        if os.path.exists(temp_path):
            os.replace(temp_path, file_path)

    def _extract_media_info(self, m):
        media = m.document or m.video or m.audio or m.photo or m.voice or m.video_note or m.animation or m.sticker
        if not media: return None
        name = getattr(media, "file_name", None) or f"file_{m.id}"
        if m.photo: name = f"photo_{m.id}.jpg"
        return MediaInfo(media, name, "media", getattr(media, "file_size", 0))

    async def _reserve_download_path(self, name, mid):
        safe = sanitize_file_name(name)
        path = os.path.join(self.download_folder, safe)
        if os.path.exists(path): return path, safe, True
        return path, safe, False

    def _cleanup_files(self, p):
        for f in (p, f"{p}.temp", f"{p}.temp.state.json"):
            if os.path.exists(f): os.remove(f)

# --- Utils ---
CHUNK_SIZE = 1024 * 1024
MAX_GET_MESSAGES_BATCH = 200
DEFAULT_CHUNK_WORKERS = 4
PRINT_LOCK = threading.Lock()
INVALID_FILENAME_CHARS = re.compile(r'[<>:"/\\|?*\x00-\x1f]')

def sanitize_file_name(n):
    n = INVALID_FILENAME_CHARS.sub("_", n)
    return n[:240]

def format_bytes(s):
    for u in ["B","KB","MB","GB"]:
        if s < 1024: return f"{s:.1f} {u}"
        s /= 1024
    return f"{s:.1f} TB"

async def main():
    global downloader_instance
    print("🤖 TG Downloader Pro")
    API_ID = os.getenv("TGDL_API_ID", "7684605")
    API_HASH = os.getenv("TGDL_API_HASH", "d270d70e8d3c3ad969ea6ecb5857e30b")

    asyncio.create_task(run_server())
    print(f"🌐 Dashboard: http://{get_local_ip()}:8000")

    downloader_instance = TelegramDownloader(API_ID, API_HASH)
    await downloader_instance.client.start()
    print("✅ Conectado. Esperando órdenes desde la web...")
    while True: await asyncio.sleep(3600)

if __name__ == "__main__":
    asyncio.run(main())
