import asyncio
import math
import os
import re
import socket
import string
import sys
import threading
import time
import uuid
import argparse
import webbrowser
import ctypes
import shutil
from storage import Storage

# --- Redirección temprana de flujos para evitar bloqueos en modo --noconsole ---
if os.name == "nt" and getattr(sys, "frozen", False):
    try:
        import ctypes
        if not ctypes.windll.kernel32.AttachConsole(-1):
            # Usar os.devnull es más compatible que una clase personalizada
            # ya que proporciona fileno() y otros métodos que librerías como uvicorn esperan.
            null_file = open(os.devnull, "w")
            sys.stdout = null_file
            sys.stderr = null_file
        else:
            sys.stdout = open("CONOUT$", "w", buffering=1)
            sys.stderr = open("CONOUT$", "w", buffering=1)
    except Exception:
        pass
elif sys.stdout is None:
    try:
        null_file = open(os.devnull, "w")
        sys.stdout = null_file
        sys.stderr = null_file
    except Exception:
        pass

from contextlib import suppress
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Dict, List, Optional, Set, Tuple

import logging

# Silenciar logs que puedan escribir en stdout/stderr
logging.getLogger("pyrogram").setLevel(logging.WARNING)
logging.getLogger("uvicorn.access").setLevel(logging.WARNING)

from dotenv import load_dotenv
from pyrogram import Client, filters
from pyrogram.errors import (
    ApiIdInvalid,
    FloodWait,
    PasswordHashInvalid,
    PhoneCodeExpired,
    PhoneCodeInvalid,
    PhoneNumberInvalid,
    SessionPasswordNeeded,
)
from pyrogram.file_id import FileId
from pyrogram.handlers import MessageHandler

try:
    from fastapi import Body, FastAPI, HTTPException, WebSocket, WebSocketDisconnect
    from fastapi.middleware.cors import CORSMiddleware
    from fastapi.responses import RedirectResponse
    from fastapi.staticfiles import StaticFiles
    import uvicorn
    import webview
    from updater import AppUpdater
except ImportError:
    print("Faltan dependencias. Ejecuta: pip install -r requirements.txt pywebview")
    sys.exit(1)


# --- Paths and persistent configuration ---
if getattr(sys, "frozen", False):
    if os.environ.get('APPIMAGE'):
        BASE_DIR = Path(os.environ.get('APPIMAGE')).resolve().parent
    else:
        BASE_DIR = Path(sys.executable).resolve().parent
    BUNDLE_DIR = Path(getattr(sys, "_MEIPASS", BASE_DIR))
else:
    BASE_DIR = Path(__file__).resolve().parent
    BUNDLE_DIR = BASE_DIR

APP_VERSION = "2.0.9"
GITHUB_REPO = "infinityxgame/tgdown"
DATA_DIR = Path.home() / ".tgdown"
DATA_DIR.mkdir(parents=True, exist_ok=True)
USER_ENV_PATH = DATA_DIR / ".env"
LEGACY_ENV_PATH = BASE_DIR / ".env"
if not USER_ENV_PATH.exists() and LEGACY_ENV_PATH.exists():
    try:
        shutil.copy2(LEGACY_ENV_PATH, USER_ENV_PATH)
    except OSError:
        pass
load_dotenv(USER_ENV_PATH)
if not USER_ENV_PATH.exists():
    load_dotenv(LEGACY_ENV_PATH)

# La sesión de Pyrogram también pertenece al perfil del usuario. Se migra
# desde la carpeta antigua para no obligar a iniciar sesión otra vez.
for legacy_session_name in ("downloader_session.session", "downloader_session.session-journal"):
    legacy_session = BASE_DIR / legacy_session_name
    user_session = DATA_DIR / legacy_session_name
    if not user_session.exists() and legacy_session.exists():
        try:
            shutil.copy2(legacy_session, user_session)
        except OSError:
            pass

updater = AppUpdater(APP_VERSION, GITHUB_REPO, BASE_DIR)

DB_PATH = DATA_DIR / "tgdown.sqlite3"
storage = Storage(DB_PATH)
CONFIG_PATH = BASE_DIR / "config.json"  # Solo se usa para migrar instalaciones anteriores.
STATE_PATH = BASE_DIR / "downloads.json"  # Solo se usa para migrar instalaciones anteriores.
state_dirty = False

DEFAULT_CONFIG: Dict[str, Any] = {
    "max_concurrent_downloads": 2,
    "parallel_chunks": True,
    "chunk_workers": 4,
    "speed_limit": {"value": 0, "unit": "MB"},
    "download_folder": "descargas",
    "listener_enabled": True,
    "listener_chat_ids": [],
    "listener_chats": [],
    "color_id": None,
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

    raw_chats = raw.get("listener_chats")
    if raw_chats is None:
        raw_chats = raw.get("listener_chat_ids", [])
    if isinstance(raw_chats, str):
        raw_chats = [part.strip() for part in raw_chats.split(",") if part.strip()]
    if not isinstance(raw_chats, list):
        if strict:
            raise ValueError("Los IDs de escucha deben ser una lista")
        raw_chats = []
    listener_chats = []
    chat_ids = []
    for raw_chat in raw_chats:
        if isinstance(raw_chat, dict):
            raw_chat_id = raw_chat.get("id")
            name = str(raw_chat.get("name", "")).strip()
            auto_download = bool(raw_chat.get("auto_download", False))
        else:
            raw_chat_id = raw_chat
            name = ""
            auto_download = False
        try:
            chat_id = int(raw_chat_id)
        except (TypeError, ValueError):
            if strict:
                raise ValueError("Cada ID de escucha debe ser numérico")
            continue
        if chat_id not in chat_ids:
            chat_ids.append(chat_id)
            listener_chats.append({
                "id": chat_id,
                "name": name or str(chat_id),
                "auto_download": auto_download,
            })

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
        "listener_chats": listener_chats,
        "color_id": raw.get("color_id", DEFAULT_CONFIG["color_id"]),
    }


def save_config(config: Dict[str, Any]) -> None:
    storage.save_config(normalize_config(config))


def load_config() -> Dict[str, Any]:
    config = normalize_config(storage.load_config(DEFAULT_CONFIG, CONFIG_PATH))
    save_config(config)
    return config


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
        "listener_chats": normalized["listener_chats"],
        "color_id": normalized["color_id"],
    }


runtime_config = load_config()


# --- Runtime state ---
stop_event = threading.Event()
shutting_down = False
downloads_state: Dict[str, Dict[str, Any]] = {}
active_tasks: Dict[str, asyncio.Task] = {}
active_jobs: Dict[str, asyncio.Task] = {}
downloader_instance = None
downloader_lock = None
websocket_clients: Set[WebSocket] = set()
websocket_broadcast_task: Optional[asyncio.Task] = None
websocket_broadcast_dirty = False


def save_downloads_state() -> None:
    global state_dirty
    for item in downloads_state.values():
        storage.upsert_download(item)
    state_dirty = False


def load_downloads_state() -> None:
    global downloads_state
    downloads_state = storage.load_downloads(STATE_PATH)
    for item in downloads_state.values():
        if item.get("status") == "downloading":
            item["status"] = "queued"
            storage.upsert_download(item)


def websocket_snapshot() -> Dict[str, Any]:
    downloads = [
        {key: value for key, value in item.items() if key != "file_path"}
        for item in sorted(
            downloads_state.values(),
            key=lambda item: (
                item.get("status") == "downloading",
                item.get("status") == "paused",
                (item.get("progress") or 0) > 0,
                item.get("created_at", item.get("updated_at", 0))
            ),
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
    global state_dirty
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
        downloads_state[item_id]["created_at"] = downloads_state[item_id].get(
            "updated_at", time.time()
        )
    downloads_state[item_id].update(kwargs)
    downloads_state[item_id]["updated_at"] = time.time()
    state_dirty = True
    storage.upsert_download(downloads_state[item_id])
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
        storage.delete_download(item_id)


# --- Auth & Session Manager ---
auth_session: Dict[str, Any] = {
    "state": "UNCONFIGURED",  # UNCONFIGURED | NOT_LOGGED_IN | WAITING_CODE | WAITING_2FA | LOGGED_IN
    "phone_number": None,
    "phone_code_hash": None,
    "user": None,
}


def save_env_credentials(api_id: str, api_hash: str) -> None:
    env_path = USER_ENV_PATH
    lines = []
    if env_path.exists():
        lines = env_path.read_text(encoding="utf-8").splitlines()

    new_lines = []
    has_id = False
    has_hash = False
    for line in lines:
        if line.startswith("TGDL_API_ID="):
            new_lines.append(f"TGDL_API_ID={api_id}")
            has_id = True
        elif line.startswith("TGDL_API_HASH="):
            new_lines.append(f"TGDL_API_HASH={api_hash}")
            has_hash = True
        else:
            new_lines.append(line)

    if not has_id:
        new_lines.append(f"TGDL_API_ID={api_id}")
    if not has_hash:
        new_lines.append(f"TGDL_API_HASH={api_hash}")

    env_path.write_text("\n".join(new_lines) + "\n", encoding="utf-8")
    os.environ["TGDL_API_ID"] = str(api_id)
    os.environ["TGDL_API_HASH"] = str(api_hash)


async def check_auth_status() -> Dict[str, Any]:
    global downloader_instance, auth_session, downloader_lock

    if downloader_lock is None:
        downloader_lock = asyncio.Lock()

    async with downloader_lock:
        api_id = os.getenv("TGDL_API_ID")
        api_hash = os.getenv("TGDL_API_HASH")

        if not api_id or not api_hash:
            auth_session["state"] = "UNCONFIGURED"
            auth_session["user"] = None
            return {"authenticated": False, "state": "UNCONFIGURED", "has_credentials": False}

        if not downloader_instance:
            try:
                downloader_instance = TelegramDownloader(api_id, api_hash)
            except Exception as err:
                return {"authenticated": False, "state": "UNCONFIGURED", "has_credentials": False, "error": str(err)}

        try:
            if not downloader_instance.client.is_connected:
                await downloader_instance.client.connect()

            # Asegurar que el despachador de Pyrogram esté iniciado si ya estamos autenticados
            if await downloader_instance.client.get_me():
                try:
                    await downloader_instance.client.initialize()
                except Exception:
                    pass

            if auth_session["state"] == "LOGGED_IN" and auth_session["user"]:
                return {
                    "authenticated": True,
                    "state": "LOGGED_IN",
                    "has_credentials": True,
                    "user": auth_session["user"],
                }

            me = await downloader_instance.client.get_me()
            if me:
                account_color_id = getattr(me.color, "color_id", 5) if hasattr(me, "color") and me.color else 5

                # Si no hay color configurado, usamos el de la cuenta y guardamos
                if runtime_config.get("color_id") is None:
                    runtime_config["color_id"] = account_color_id
                    save_config(runtime_config)

                user_info = {
                    "id": me.id,
                    "first_name": me.first_name or "",
                    "username": me.username or "",
                    "phone": getattr(me, "phone_number", ""),
                    "color_id": account_color_id
                }
                auth_session["state"] = "LOGGED_IN"
                auth_session["user"] = user_info
                return {
                    "authenticated": True,
                    "state": "LOGGED_IN",
                    "has_credentials": True,
                    "user": user_info,
                }
        except Exception:
            pass

        if auth_session["state"] not in {"WAITING_CODE", "WAITING_2FA"}:
            auth_session["state"] = "NOT_LOGGED_IN"
            auth_session["user"] = None

        return {
            "authenticated": False,
            "state": auth_session["state"],
            "has_credentials": True,
            "phone_number": auth_session.get("phone_number"),
        }


# --- Dashboard API ---
app = FastAPI(title="Telegram DL API")

@app.post("/api/app/exit")
async def exit_app():
    stop_event.set()
    return {"status": "ok"}


@app.get("/api/update/check")
async def check_update():
    latest = updater.check_for_update()
    size = 0
    if latest:
        asset = updater.get_asset_for_platform(latest)
        if asset:
            size = asset.get('size', 0)

    return {
        "update_available": latest is not None,
        "latest": latest["tag_name"] if latest else None,
        "current": APP_VERSION,
        "size_bytes": size
    }

@app.get("/api/update/progress")
async def get_update_progress():
    return updater.progress

@app.post("/api/update/install")
async def install_update():
    latest = updater.check_for_update()
    if not latest:
        raise HTTPException(status_code=400, detail="No hay actualizaciones disponibles")

    asset = updater.get_asset_for_platform(latest)
    if not asset:
        raise HTTPException(status_code=400, detail="No se encontró un instalador para tu sistema")

    # Ejecutar descarga e instalación en un hilo separado
    if updater.progress["status"] not in ["starting", "downloading", "extracting"]:
        threading.Thread(target=updater.download_and_install, args=(asset,), daemon=True).start()

    return {"status": "ok", "message": "Iniciando descarga e instalación"}

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


@app.get("/api/auth/status")
async def get_auth_status() -> Dict[str, Any]:
    return await check_auth_status()


@app.post("/api/auth/credentials")
async def set_auth_credentials(data: Dict[str, Any] = Body(...)) -> Dict[str, Any]:
    global downloader_instance, downloader_lock

    if downloader_lock is None:
        downloader_lock = asyncio.Lock()

    async with downloader_lock:
        api_id = str(data.get("api_id", "")).strip()
        api_hash = str(data.get("api_hash", "")).strip()

        if not api_id or not api_hash:
            raise HTTPException(status_code=422, detail="Debe ingresar tanto API ID como API HASH")

        save_env_credentials(api_id, api_hash)

        if downloader_instance and downloader_instance.client:
            with suppress(Exception):
                await downloader_instance.client.disconnect()
            downloader_instance = None

        try:
            downloader_instance = TelegramDownloader(api_id, api_hash)
            await downloader_instance.client.connect()
        except Exception as error:
            raise HTTPException(status_code=400, detail=f"Credenciales de API inválidas: {error}") from error

    return await check_auth_status()


@app.post("/api/auth/send-code")
async def auth_send_code(data: Dict[str, Any] = Body(...)) -> Dict[str, Any]:
    global downloader_instance, auth_session
    phone = str(data.get("phone_number", "")).strip().replace(" ", "").replace("-", "")
    if not phone:
        raise HTTPException(status_code=422, detail="Número de teléfono requerido")

    status = await check_auth_status()
    if not status["has_credentials"]:
        raise HTTPException(status_code=400, detail="Faltan credenciales API ID / API HASH")

    try:
        if not downloader_instance.client.is_connected:
            await downloader_instance.client.connect()
        sent_code = await downloader_instance.client.send_code(phone)
        auth_session["phone_number"] = phone
        auth_session["phone_code_hash"] = sent_code.phone_code_hash
        auth_session["state"] = "WAITING_CODE"
        return {
            "status": "ok",
            "state": "WAITING_CODE",
            "phone_number": phone,
            "phone_code_hash": sent_code.phone_code_hash,
        }
    except PhoneNumberInvalid:
        raise HTTPException(status_code=400, detail="El número de teléfono introducido no es válido.")
    except ApiIdInvalid:
        raise HTTPException(status_code=400, detail="El API ID o API HASH configurados no son válidos.")
    except FloodWait as e:
        raise HTTPException(status_code=429, detail=f"Telegram ha limitado las peticiones. Intenta de nuevo en {e.value} segundos.")
    except Exception as error:
        raise HTTPException(status_code=400, detail=f"Error al enviar código: {error}") from error


@app.post("/api/auth/verify-code")
async def auth_verify_code(data: Dict[str, Any] = Body(...)) -> Dict[str, Any]:
    global downloader_instance, auth_session
    code = str(data.get("code", "")).strip().replace(" ", "")
    phone = str(data.get("phone_number", "")).strip().replace(" ", "").replace("-", "") or auth_session.get("phone_number")
    phone_code_hash = auth_session.get("phone_code_hash")

    if not code:
        raise HTTPException(status_code=422, detail="Código de verificación requerido")
    if not phone or not phone_code_hash:
        raise HTTPException(status_code=400, detail="No se ha iniciado un proceso de login previo. Solicita un nuevo código.")

    try:
        if not downloader_instance.client.is_connected:
            await downloader_instance.client.connect()
        await downloader_instance.client.sign_in(phone, phone_code_hash, code)

        # Iniciar el despachador tras el inicio de sesión exitoso
        try:
            await downloader_instance.client.initialize()
        except Exception:
            pass

        me = await downloader_instance.client.get_me()
        user_info = {
            "id": me.id,
            "first_name": me.first_name or "",
            "username": me.username or "",
            "phone": getattr(me, "phone_number", ""),
        }
        auth_session["state"] = "LOGGED_IN"
        auth_session["user"] = user_info
        asyncio.create_task(downloader_instance.resume_all())
        return {"status": "ok", "state": "LOGGED_IN", "user": user_info}
    except SessionPasswordNeeded:
        auth_session["state"] = "WAITING_2FA"
        return {"status": "2fa_required", "state": "WAITING_2FA"}
    except (PhoneCodeInvalid, PhoneCodeExpired):
        raise HTTPException(status_code=400, detail="El código ingresado es incorrecto o ha expirado.")
    except Exception as error:
        raise HTTPException(status_code=400, detail=f"Error al verificar código: {error}") from error


@app.post("/api/auth/verify-2fa")
async def auth_verify_2fa(data: Dict[str, Any] = Body(...)) -> Dict[str, Any]:
    global downloader_instance, auth_session
    password = str(data.get("password", ""))
    if not password:
        raise HTTPException(status_code=422, detail="Contraseña requerida")

    try:
        if not downloader_instance.client.is_connected:
            await downloader_instance.client.connect()
        await downloader_instance.client.check_password(password)

        # Iniciar el despachador tras verificar 2FA
        try:
            await downloader_instance.client.initialize()
        except Exception:
            pass

        me = await downloader_instance.client.get_me()
        user_info = {
            "id": me.id,
            "first_name": me.first_name or "",
            "username": me.username or "",
            "phone": getattr(me, "phone_number", ""),
        }
        auth_session["state"] = "LOGGED_IN"
        auth_session["user"] = user_info
        asyncio.create_task(downloader_instance.resume_all())
        return {"status": "ok", "state": "LOGGED_IN", "user": user_info}
    except PasswordHashInvalid:
        raise HTTPException(status_code=400, detail="La contraseña 2FA ingresada es incorrecta.")
    except Exception as error:
        raise HTTPException(status_code=400, detail=f"Error en verificación 2FA: {error}") from error


@app.post("/api/auth/logout")
async def auth_logout() -> Dict[str, Any]:
    global downloader_instance, auth_session
    if downloader_instance and downloader_instance.client:
        with suppress(Exception):
            await downloader_instance.client.log_out()
        with suppress(Exception):
            await downloader_instance.client.disconnect()
        downloader_instance = None

    session_file = DATA_DIR / "downloader_session.session"
    if session_file.exists():
        with suppress(OSError):
            session_file.unlink()
    session_journal = DATA_DIR / "downloader_session.session-journal"
    if session_journal.exists():
        with suppress(OSError):
            session_journal.unlink()

    auth_session["state"] = "NOT_LOGGED_IN"
    auth_session["phone_number"] = None
    auth_session["phone_code_hash"] = None
    auth_session["user"] = None
    return {"status": "ok", "state": "NOT_LOGGED_IN"}


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


@app.delete("/api/downloads/history")
async def clear_download_history() -> Dict[str, Any]:
    finished_statuses = {"completed", "skipped", "failed", "cancelled"}
    removed = [item_id for item_id, item in downloads_state.items() if item.get("status") in finished_statuses]
    for item_id in removed:
        downloads_state.pop(item_id, None)
    storage.clear_finished_downloads()
    schedule_websocket_broadcast()
    return {"status": "ok", "removed": len(removed)}


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
    storage.delete_download(item_id)
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
        "chats": runtime_config["listener_chats"],
    }


@app.get("/api/listener")
async def get_listener_items() -> List[Dict[str, Any]]:
    if not downloader_instance:
        return []
    return downloader_instance.get_listener_items()


@app.get("/api/listener/chat/{chat_id}")
async def resolve_listener_chat(chat_id: int) -> Dict[str, Any]:
    if not downloader_instance:
        raise HTTPException(status_code=503, detail="El cliente no está listo")
    try:
        chat = await downloader_instance.client.get_chat(chat_id)
    except Exception as error:
        raise HTTPException(status_code=404, detail=f"No se pudo encontrar el chat: {error}") from error
    name = getattr(chat, "title", None) or getattr(chat, "first_name", None) or getattr(chat, "username", None)
    if not name:
        name = str(chat_id)
    return {
        "status": "ok",
        "chat": {
            "id": chat.id,
            "name": str(name),
            "type": str(getattr(chat, "type", "")),
            "username": getattr(chat, "username", None),
        },
    }


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
    chats = data.get("chats")
    if chats is None:
        chats = data.get("chat_ids", [])
    settings = await apply_and_save_settings(
        {
            "listener_enabled": data.get("enabled", True),
            "listener_chats": chats,
        }
    )
    return {
        "status": "ok",
        "enabled": settings["listener_enabled"],
        "chat_ids": settings["listener_chat_ids"],
        "chats": settings["listener_chats"],
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
        # Asegurar que la tarea de descarga esté en ejecución si se está reanudando
        if item_id not in active_tasks:
            task = asyncio.create_task(downloader_instance.resume_item(item_id))
            active_tasks[item_id] = task
            task.add_done_callback(lambda f, i=item_id: active_tasks.pop(i, None))

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


dashboard_path = BUNDLE_DIR / "dashboard" / "dist"
if not dashboard_path.exists():
    dashboard_path = BASE_DIR / "dashboard" / "dist"

if dashboard_path.exists():
    app.mount("/dashboard", StaticFiles(directory=dashboard_path, html=True), name="dashboard")
else:
    print(f"⚠️ Advertencia: No se encontró la carpeta del panel web en {dashboard_path}")


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
    # Desactivar access_log y subir log_level para evitar bloqueos por I/O
    config = uvicorn.Config(app, host=host, port=port, log_level="warning", access_log=False)
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

        # Solo intentar imprimir si el flujo es válido y no estamos en un entorno bloqueado
        try:
            if sys.stdout and hasattr(sys.stdout, "write"):
                with PRINT_LOCK:
                    print(
                        f"Descargando: {self.file_name} - {percentage:5.1f}% "
                        f"({format_bytes(speed_value)}/s)"
                    )
        except Exception:
            pass


class TelegramDownloader:
    def __init__(self, api_id: str, api_hash: str):
        self.config = normalize_config(runtime_config)
        self.parallel_chunks = self.config["parallel_chunks"]
        self.chunk_workers = self.config["chunk_workers"]
        self.max_concurrent_downloads = self.config["max_concurrent_downloads"]
        self.listener_enabled = self.config["listener_enabled"]
        self.watched_chat_ids = set(self.config["listener_chat_ids"])
        self.auto_download_chat_ids = {
            chat["id"] for chat in self.config["listener_chats"] if chat.get("auto_download")
        }
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
            workdir=str(DATA_DIR),
            app_version=APP_VERSION,
            device_model="TGDown Desktop",
            system_version="Windows 11",
            lang_code="es",
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
        self.auto_download_chat_ids = {
            chat["id"] for chat in normalized["listener_chats"] if chat.get("auto_download")
        }
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
        if getattr(message, "outgoing", False) or getattr(
            getattr(message, "from_user", None), "is_self", False
        ):
            return
        chat_id = message.chat.id
        if chat_id not in self.watched_chat_ids:
            return
        info = self._extract_media_info(message)
        if not info:
            return

        chat_name = None
        for c in self.config.get("listener_chats", []):
            if c.get("id") == chat_id:
                chat_name = c.get("name")
                break
        if not chat_name and message.chat:
            chat_name = (
                getattr(message.chat, "title", None)
                or getattr(message.chat, "first_name", None)
                or getattr(message.chat, "username", None)
            )
        if not chat_name:
            chat_name = str(chat_id)

        item_id = f"listener:{chat_id}:{message.id}"
        self.listener_messages[item_id] = (message, chat_id)
        update_state(
            item_id,
            job_id=f"listener:{chat_id}",
            message_id=message.id,
            chat_id=chat_id,
            chat_name=str(chat_name),
            file_name=sanitize_file_name(info.file_name) or f"file_{message.id}",
            total_str=format_bytes(info.file_size),
            kind=info.kind,
            source="listener",
            status="available",
            progress=0,
        )
        if chat_id in self.auto_download_chat_ids:
            update_state(item_id, status="queued")
            task = asyncio.create_task(
                self.download_file_from_message(message, chat_id, item_id)
            )
            active_tasks[item_id] = task
            task.add_done_callback(
                lambda finished, current_item=item_id: active_tasks.pop(current_item, None)
            )
        if len(self.listener_messages) > 300:
            oldest = sorted(
                self.listener_messages,
                key=lambda key: downloads_state.get(key, {}).get("updated_at", 0),
            )[:50]
            for old_item_id in oldest:
                self.listener_messages.pop(old_item_id, None)
                downloads_state.pop(old_item_id, None)
                storage.delete_download(old_item_id)

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
            for message_id in range(end_id, start_id - 1, -1):
                update_state(
                    self._item_id(job_id, message_id),
                    job_id=job_id,
                    message_id=message_id,
                    chat_id=chat_id,
                    status="pending",
                    file_name=f"Mensaje {message_id}",
                )

            all_tasks: List[asyncio.Task] = []
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
                    all_tasks.append(task)

            if all_tasks:
                await asyncio.gather(*all_tasks, return_exceptions=True)
        except asyncio.CancelledError:
            raise
        except Exception as error:
            print(f"Error en el trabajo {job_id}: {error}")

    async def resume_all(self) -> None:
        to_resume = []
        for item_id, item in downloads_state.items():
            if item.get("status") in {"pending", "queued", "downloading", "paused"}:
                has_progress = bool(storage.chunks(item_id))
                file_path = item.get("file_path")
                if file_path and os.path.exists(f"{file_path}.temp"):
                    has_progress = True

                to_resume.append({
                    "id": item_id,
                    "progress": has_progress,
                    "created_at": item.get("created_at", 0)
                })

        if not to_resume:
            return

        to_resume.sort(key=lambda x: (not x["progress"], x["created_at"]))

        print(f"Reanudando {len(to_resume)} tareas pendientes...")
        for entry in to_resume:
            item_id = entry["id"]
            task = asyncio.create_task(self.resume_item(item_id))
            active_tasks[item_id] = task
            task.add_done_callback(lambda f, i=item_id: active_tasks.pop(i, None))

    async def resume_item(self, item_id: str) -> None:
        item = downloads_state.get(item_id)
        if not item: return
        chat_id = item.get("chat_id")
        message_id = item.get("message_id")
        if not chat_id or not message_id:
            update_state(item_id, status="failed")
            return

        try:
            message = await self.client.get_messages(chat_id, message_id)
            if not message or message.empty:
                update_state(item_id, status="skipped")
                return
            await self.download_file_from_message(message, chat_id, item_id)
        except Exception as e:
            print(f"Error al reanudar {item_id}: {e}")
            if shutting_down:
                update_state(item_id, status="queued")
            else:
                update_state(item_id, status="failed")

    async def download_file_from_message(self, message: Any, chat_id: Any, item_id: str) -> None:
        info = self._extract_media_info(message)
        if not info:
            update_state(item_id, status="skipped")
            return

        update_state(
            item_id,
            file_name=info.file_name,
            kind=info.kind,
            total_str=format_bytes(info.file_size),
        )

        pause_event = self.pause_events.get(item_id)
        if pause_event is None:
            pause_event = asyncio.Event()
            # Si el estado persistido es 'paused', el evento empieza bloqueado
            if downloads_state.get(item_id, {}).get("status") == "paused":
                pause_event.clear()
            else:
                pause_event.set()
            self.pause_events[item_id] = pause_event
        await pause_event.wait()
        await self._acquire_download_slot()
        reserved_path: Optional[str] = None
        try:
            file_path, file_name, already_exists = await self._reserve_download_path(
                info.file_name, message.id, info.file_size
            )
            if already_exists:
                update_state(
                    item_id,
                    file_name=file_name,
                    kind=info.kind,
                    total_str=format_bytes(info.file_size),
                    file_path=file_path,
                    status="skipped",
                    progress=100,
                )
                return

            reserved_path = file_path
            update_state(
                item_id,
                file_name=file_name,
                kind=info.kind,
                total_str=format_bytes(info.file_size),
                file_path=file_path,
                status="downloading",
            )

            workers = self.chunk_workers if self.parallel_chunks else 1
            await self._download_manual(info, file_path, file_name, item_id, workers)
            update_state(item_id, status="completed", progress=100)
        except asyncio.CancelledError:
            if shutting_down:
                # Si estaba pausado, mantenemos el estado para que no se reanude solo
                if downloads_state.get(item_id, {}).get("status") != "paused":
                    update_state(item_id, status="queued")
            else:
                update_state(item_id, status="cancelled", progress=0)
            raise
        except Exception as error:
            print(f"Error en la descarga {item_id}: {error}")
            if shutting_down:
                update_state(item_id, status="queued")
            else:
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

        downloaded_chunks = storage.chunks(item_id)

        progress = DownloadProgress(
            item_id,
            file_name,
            file_size,
            initial=len(downloaded_chunks) * CHUNK_SIZE,
            kind=info.kind
        )

        if not os.path.exists(temp_path) or os.path.getsize(temp_path) != file_size:
            with open(temp_path, "w+b") as file_handle:
                file_handle.truncate(file_size)
            downloaded_chunks = set()

        queue: asyncio.Queue[int] = asyncio.Queue()
        for index in range(total_chunks):
            if index not in downloaded_chunks:
                queue.put_nowait(index)

        downloaded = len(downloaded_chunks) * CHUNK_SIZE
        errors: List[Exception] = []
        state_lock = asyncio.Lock()

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

                    async with state_lock:
                        downloaded_chunks.add(index)
                        downloaded += len(chunk_data)
                        progress.update(downloaded)
                        
                        storage.add_chunk(item_id, index)

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
        with storage._lock:
            storage.db.execute("DELETE FROM download_chunks WHERE download_id=?", (item_id,))
            storage.db.commit()

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

        tg_name = getattr(media, "file_name", None)

        is_generic = False
        if not tg_name:
            is_generic = True
            if message.photo:
                tg_name = f"photo_{message.id}.jpg"
            else:
                tg_name = f"file_{message.id}"

        generic_prefixes = ("video_", "doc_", "music_", "audio_", "sticker_", "photo_", "file_")
        if any(tg_name.lower().startswith(p) for p in generic_prefixes):
            is_generic = True

        _, ext = os.path.splitext(tg_name)

        name = tg_name
        if is_generic and message.caption:
            first_line = message.caption.split("\n")[0].strip()
            if first_line:
                if len(first_line) > 50:
                    first_line = first_line[:50].strip()
                name = f"{first_line}{ext}"

        kind = "file"
        if message.photo:
            kind = "photo"
        elif message.video or message.video_note or message.animation:
            kind = "video"
        elif message.audio or message.voice:
            kind = "song"
        elif message.document:
            mime = getattr(media, "mime_type", "") or ""
            lower_ext = ext.lower()
            if mime.startswith("image/") or lower_ext in {".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".heic", ".svg"}:
                kind = "photo"
            elif mime.startswith("video/") or lower_ext in {".mp4", ".mkv", ".webm", ".avi", ".mov", ".flv", ".wmv", ".m4v"}:
                kind = "video"
            elif mime.startswith("audio/") or lower_ext in {".mp3", ".m4a", ".flac", ".wav", ".ogg", ".opus", ".aac", ".wma"}:
                kind = "song"
            else:
                kind = "file"

        return MediaInfo(
            media,
            name,
            kind,
            int(getattr(media, "file_size", 0) or 0),
        )

    async def _reserve_download_path(
        self, name: str, message_id: int, expected_size: int = 0
    ) -> Tuple[str, str, bool]:
        safe_name = sanitize_file_name(name)
        if not safe_name:
            safe_name = f"file_{message_id}"
        stem, extension = os.path.splitext(safe_name)
        candidate = safe_name
        counter = 1
        async with self._reserved_paths_lock:
            while True:
                path = str(self.download_folder / candidate)
                if os.path.exists(path) and path not in self._reserved_paths:
                    try:
                        if os.path.getsize(path) == expected_size:
                            return path, candidate, True
                    except OSError:
                        pass

                if not os.path.exists(path) and path not in self._reserved_paths:
                    self._reserved_paths.add(path)
                    return path, candidate, False
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


def check_webview2_runtime() -> bool:
    """Verifica si WebView2 está instalado y tiene una versión compatible."""
    if os.name != "nt":
        return True

    import winreg

    # Lista para almacenar todas las versiones encontradas
    found_versions = []

    # 1. Intentar buscar en el Registro de Windows (Rutas de Runtime y Navegador)
    try:
        guids = [
            "{F3C4FE00-EFD5-403b-9569-398A20F1BA4A}", # Evergreen Runtime
            "{56EB18F8-B008-4CBD-B6D2-8C97FE7E9062}", # Edge Stable
            "{2CD8A007-E189-409D-A2C8-9AFAD3EF3D72}", # Edge Beta
            "{0D5074D7-3D0A-4ca1-8D04-80C6190F693D}"  # Edge Dev
        ]

        for guid in guids:
            # Probar en HKLM y HKCU, y en ramas de 32 y 64 bits
            for root in [winreg.HKEY_LOCAL_MACHINE, winreg.HKEY_CURRENT_USER]:
                for base_path in [r"SOFTWARE\Microsoft\EdgeUpdate\Clients", r"SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients"]:
                    try:
                        path = f"{base_path}\\{guid}"
                        with winreg.OpenKey(root, path, 0, winreg.KEY_READ | winreg.KEY_WOW64_64KEY) as key:
                            v, _ = winreg.QueryValueEx(key, "pv")
                            if v and not v.startswith("1.3."): # Omitir el versionado del Updater
                                found_versions.append(v)
                    except OSError: continue
    except Exception: pass

    # 2. Intentar buscar en las carpetas de instalación por defecto
    search_dirs = [
        os.environ.get("ProgramFiles(x86)", "C:\\Program Files (x86)") + "\\Microsoft\\EdgeWebView\\Application",
        os.environ.get("ProgramFiles", "C:\\Program Files") + "\\Microsoft\\EdgeWebView\\Application",
        os.environ.get("LocalAppData", "") + "\\Microsoft\\EdgeWebView\\Application"
    ]

    for sdir in search_dirs:
        if os.path.exists(sdir):
            try:
                # Buscar carpetas que tengan formato de versión (ej. 120.0.2210.91)
                for entry in os.listdir(sdir):
                    if entry[0].isdigit() and "." in entry:
                        found_versions.append(entry)
            except Exception: continue

    # Determinar la versión más alta encontrada
    version = None
    if found_versions:
        # Limpiar y ordenar versiones (ignorando las que empiezan por 1.3 que son del updater)
        valid_vs = [v for v in found_versions if v.split('.')[0].isdigit() and int(v.split('.')[0]) > 5]
        if valid_vs:
            version = sorted(valid_vs, key=lambda x: [int(i) for i in x.split('.')], reverse=True)[0]

    if version:
        print(f"[WebView2] Detectado: v{version}")
    else:
        print("[WebView2] No detectado directamente.")

    is_valid = False
    if version:
        try:
            major = int(version.split('.')[0])
            if major >= 101:
                is_valid = True
        except (ValueError, IndexError):
            pass

    if not is_valid:
        try:
            import tkinter as tk
            from tkinter import font as tkfont

            root = tk.Tk()
            root.withdraw() 

            try:
                from ctypes import windll
                windll.shcore.SetProcessDpiAwareness(1)
            except Exception:
                pass

            dialog = tk.Toplevel(root)
            dialog.title("TelegramDL")
            dialog.configure(bg='#0f172a') 
            dialog.geometry("500x320")
            dialog.resizable(False, False)

            sw = dialog.winfo_screenwidth()
            sh = dialog.winfo_screenheight()
            dialog.geometry(f"+{(sw-500)//2}+{(sh-320)//2}")
            dialog.overrideredirect(True) 

            main_frame = tk.Frame(dialog, bg='#1e293b', highlightbackground='#334155', highlightthickness=1)
            main_frame.pack(fill="both", expand=True, padx=2, pady=2)

            title_f = tkfont.Font(family="Segoe UI", size=18, weight="bold")
            header_f = tkfont.Font(family="Segoe UI", size=13, weight="bold")
            body_f = tkfont.Font(family="Segoe UI", size=10)

            tk.Label(main_frame, text="TelegramDL", bg='#1e293b', fg='#3b82f6', font=title_f, pady=20).pack()
            tk.Label(main_frame, text="Componente de Sistema Requerido", bg='#1e293b', fg='#f8fafc', font=header_f).pack()

            msg = (
                "\nPara funcionar correctamente como aplicación nativa, se requiere el componente\n"
                "'Microsoft Edge WebView2 Runtime' (versión 101 o superior).\n\n"
                "Parece que no está instalado o debe actualizarse en su sistema.\n\n"
                "¿Desea abrir la página de descarga oficial ahora?"
            )
            tk.Label(main_frame, text=msg, bg='#1e293b', fg='#94a3b8', font=body_f, justify="center").pack(padx=40)

            btn_frame = tk.Frame(main_frame, bg='#1e293b', pady=30)
            btn_frame.pack()

            def on_yes():
                webbrowser.open("https://developer.microsoft.com/en-us/microsoft-edge/webview2/#download-section")
                root.destroy()
                sys.exit(0)

            def on_no():
                root.destroy()
                sys.exit(0)

            btn_dl = tk.Button(btn_frame, text="SÍ, DESCARGAR", command=on_yes, bg='#3b82f6', fg='white',
                               font=("Segoe UI", 9, "bold"), padx=25, pady=8, border=0, cursor="hand2",
                               activebackground='#2563eb', activeforeground='white')
            btn_dl.pack(side="left", padx=10)

            btn_close = tk.Button(btn_frame, text="NO, CERRAR", command=on_no, bg='#334155', fg='white',
                                  font=("Segoe UI", 9, "bold"), padx=25, pady=8, border=0, cursor="hand2",
                                  activebackground='#475569', activeforeground='white')
            btn_close.pack(side="left", padx=10)

            dialog.attributes("-topmost", True)
            root.mainloop()
        except Exception:
            title = "TelegramDL"
            message = "Se requiere WebView2 Runtime. ¿Desea descargarlo?"
            res = ctypes.windll.user32.MessageBoxW(0, message, title, 0x4 | 0x40)
            if res == 6:
                webbrowser.open("https://developer.microsoft.com/en-us/microsoft-edge/webview2/#download-section")

        return False

    return True


async def main(server_mode: bool = False) -> None:
    global downloader_instance, shutting_down

    # Asegurar que el directorio de datos existe
    DATA_DIR.mkdir(parents=True, exist_ok=True)
    log_file = DATA_DIR / "backend.log"

    def log_backend(msg: str):
        timestamp = time.strftime("%Y-%m-%d %H:%M:%S")
        with open(log_file, "a", encoding="utf-8") as f:
            f.write(f"[{timestamp}] {msg}\n")
        print(msg)

    log_backend(f"--- Iniciando TG Downloader v{APP_VERSION} (Modo {'Servidor' if server_mode else 'Nativo'}) ---")

    load_downloads_state()
    log_backend("Estado de descargas cargado.")

    server_task = asyncio.create_task(run_server())

    port = os.getenv('TGDL_PORT', '8000')
    host = os.getenv("TGDL_BIND_HOST", "127.0.0.1")
    visible_host = get_local_ip() if host == "0.0.0.0" else host

    dashboard_url = f"http://{visible_host}:{port}/dashboard/"
    log_backend(f"Dashboard disponible en: {dashboard_url}")

    api_id = os.getenv("TGDL_API_ID")
    api_hash = os.getenv("TGDL_API_HASH")

    if api_id and api_hash:
        async def startup_resume():
            # En modo nativo, dar tiempo a que la UI y el server se asienten
            if not server_mode:
                await asyncio.sleep(2)

            log_backend("Verificando autenticación para reanudación automática...")
            for attempt in range(3):
                try:
                    status = await check_auth_status()
                    if status.get("authenticated"):
                        log_backend("Conectado a Telegram. Iniciando reanudación...")
                        if downloader_instance:
                            await downloader_instance.resume_all()
                            log_backend("Comando resume_all enviado con éxito.")
                            break
                    else:
                        log_backend("Esperando autenticación del usuario...")
                        break
                except Exception as e:
                    log_backend(f"Intento {attempt+1} fallido: {e}")
                    await asyncio.sleep(2)

        asyncio.create_task(startup_resume())
    else:
        log_backend("Sin credenciales API configuradas.")

    if not server_mode:
        log_backend("Iniciando interfaz nativa...")

    try:
        # En modo nativo, usamos una espera que reaccione al stop_event de threading
        while not stop_event.is_set():
            await asyncio.sleep(0.5)
    except (asyncio.CancelledError, KeyboardInterrupt):
        log_backend("Interrupción detectada.")
    finally:
        shutting_down = True
        log_backend("Iniciando proceso de cierre...")

        # Guardar estado de descargas activas como en cola ANTES de cerrar
        active_count = 0
        for item_id, item in list(downloads_state.items()):
            if item.get("status") == "downloading":
                update_state(item_id, status="queued")
                active_count += 1

        if active_count > 0:
            log_backend(f"Marcadas {active_count} descargas como 'en cola'.")

        save_downloads_state()
        log_backend("Estado guardado en base de datos.")

        print("Cerrando aplicacion de forma segura...")
        server_task.cancel()
        with suppress(asyncio.CancelledError):
            await server_task
        log_backend("Servidor web detenido.")

        if downloader_instance and downloader_instance.client:
            log_backend("Desconectando de Telegram...")
            with suppress(Exception):
                await downloader_instance.client.disconnect()
        print("Proceso finalizado correctamente.")
        log_backend("Cierre completo.")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Telegram DL - Gestor de descargas nativo")
    parser.add_argument(
        "--server", "-s",
        action="store_true",
        help="Ejecuta solo el servidor backend sin abrir la ventana nativa"
    )
    args = parser.parse_args()

    if args.server:
        try:
            asyncio.run(main(server_mode=True))
        except (KeyboardInterrupt, SystemExit):
            print("\nTG Downloader finalizado.")
    else:
        
        if not check_webview2_runtime():
            sys.exit(0)

        
        def start_backend():
            asyncio.run(main(server_mode=False))

        t = threading.Thread(target=start_backend, daemon=True)
        t.start()

        try:
            port = os.getenv("TGDL_PORT", "8000")

            os.environ["WDM_LOG_LEVEL"] = "0"

            webview.create_window(
                "TelegramDL",
                f"http://127.0.0.1:{port}/dashboard/",
                width=1000,
                height=650,
                min_size=(800, 600),
                resizable=True,
                background_color="#ffffff"
            )

            webview.start(storage_path=str(DATA_DIR / "cache"))

            # Al salir de la ventana, avisar al backend y esperar cierre limpio
            stop_event.set()
            t.join(timeout=15)

        except Exception as e:
            import traceback
            print(f"Error al iniciar la interfaz gráfica: {e}")
            traceback.print_exc()
            print("\nTip: Asegúrate de tener instalado 'WebView2 Runtime' de Microsoft.")
            print("Iniciando modo servidor como alternativa...")
            asyncio.run(main(server_mode=True))

