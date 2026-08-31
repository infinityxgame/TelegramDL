"""Persistencia local de TelegramDL en SQLite.

La base de datos es deliberadamente local: permite transacciones, consultas y
recuperación tras un cierre sin depender de archivos temporales JSON.
"""
from __future__ import annotations

import json
import sqlite3
import threading
import time
from pathlib import Path
from typing import Any, Dict, Iterable, Optional


class Storage:
    def __init__(self, path: Path):
        self.path = path
        self._lock = threading.RLock()
        self.db = sqlite3.connect(str(path), check_same_thread=False, timeout=30)
        self.db.row_factory = sqlite3.Row
        with self._lock:
            self.db.execute("PRAGMA journal_mode=WAL")
            self.db.execute("PRAGMA synchronous=NORMAL")
            self.db.executescript(
                """
                CREATE TABLE IF NOT EXISTS app_config (
                    key TEXT PRIMARY KEY,
                    value TEXT NOT NULL
                );
                CREATE TABLE IF NOT EXISTS listener_chats (
                    chat_id INTEGER PRIMARY KEY,
                    name TEXT NOT NULL,
                    auto_download INTEGER NOT NULL DEFAULT 0
                );
                CREATE TABLE IF NOT EXISTS downloads (
                    id TEXT PRIMARY KEY, job_id TEXT, message_id INTEGER,
                    chat_id INTEGER, file_name TEXT NOT NULL,
                    status TEXT NOT NULL, progress REAL NOT NULL DEFAULT 0,
                    total_str TEXT NOT NULL DEFAULT '0 B',
                    current_str TEXT NOT NULL DEFAULT '0 B',
                    speed TEXT NOT NULL DEFAULT '0 B/s', kind TEXT,
                    file_path TEXT, source TEXT, updated_at REAL NOT NULL,
                    created_at REAL NOT NULL
                );
                CREATE INDEX IF NOT EXISTS idx_downloads_updated ON downloads(updated_at DESC);
                CREATE TABLE IF NOT EXISTS download_chunks (
                    download_id TEXT NOT NULL,
                    chunk_index INTEGER NOT NULL,
                    PRIMARY KEY(download_id, chunk_index),
                    FOREIGN KEY(download_id) REFERENCES downloads(id) ON DELETE CASCADE
                );
                """
            )
            self.db.commit()

    def _set(self, key: str, value: Any) -> None:
        self.db.execute("INSERT INTO app_config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", (key, str(value)))

    def load_config(self, defaults: Dict[str, Any], legacy: Optional[Path] = None) -> Dict[str, Any]:
        with self._lock:
            rows = dict(self.db.execute("SELECT key,value FROM app_config"))
            if not rows and legacy and legacy.exists():
                try:
                    raw = json.loads(legacy.read_text(encoding="utf-8"))
                    if isinstance(raw, dict):
                        for key in ("max_concurrent_downloads", "parallel_chunks", "chunk_workers", "download_folder", "listener_enabled", "color_id"):
                            if key in raw and raw[key] is not None:
                                self._set(key, int(raw[key]) if isinstance(raw[key], bool) else raw[key])
                        speed = raw.get("speed_limit", {})
                        self._set("speed_value", speed.get("value", 0))
                        self._set("speed_unit", speed.get("unit", "MB"))
                        for chat in raw.get("listener_chats", raw.get("listener_chat_ids", [])):
                            if not isinstance(chat, dict): chat = {"id": chat}
                            self.db.execute("INSERT OR REPLACE INTO listener_chats(chat_id,name,auto_download) VALUES(?,?,?)", (int(chat["id"]), str(chat.get("name") or chat["id"]), int(bool(chat.get("auto_download")))) )
                        self.db.commit()
                        rows = dict(self.db.execute("SELECT key,value FROM app_config"))
                except (OSError, ValueError, json.JSONDecodeError):
                    pass
            config = dict(defaults)
            for key in ("max_concurrent_downloads", "chunk_workers"):
                if key in rows:
                    config[key] = int(rows[key])
            for key in ("parallel_chunks", "listener_enabled"):
                if key in rows:
                    config[key] = rows[key].lower() in {"1", "true"}
            for key in ("download_folder", "color_id"):
                if key in rows: config[key] = rows[key] if key != "color_id" else (None if rows[key] == "None" else int(rows[key]))
            config["speed_limit"] = {"value": float(rows.get("speed_value", defaults["speed_limit"]["value"])), "unit": rows.get("speed_unit", "MB")}
            chats = [dict(row) for row in self.db.execute("SELECT chat_id AS id,name,auto_download FROM listener_chats ORDER BY chat_id")]
            config["listener_chats"] = [{"id": c["id"], "name": c["name"], "auto_download": bool(c["auto_download"])} for c in chats]
            config["listener_chat_ids"] = [c["id"] for c in config["listener_chats"]]
            return config

    def save_config(self, config: Dict[str, Any]) -> None:
        with self._lock:
            for key in ("max_concurrent_downloads", "parallel_chunks", "chunk_workers", "download_folder", "listener_enabled", "color_id"):
                self._set(key, config.get(key))
            self._set("speed_value", config["speed_limit"]["value"])
            self._set("speed_unit", config["speed_limit"]["unit"])
            self.db.execute("DELETE FROM listener_chats")
            for chat in config.get("listener_chats", []):
                self.db.execute("INSERT INTO listener_chats(chat_id,name,auto_download) VALUES(?,?,?)", (int(chat["id"]), chat.get("name", str(chat["id"])), int(bool(chat.get("auto_download")))))
            self.db.commit()

    def load_downloads(self, legacy: Optional[Path] = None) -> Dict[str, Dict[str, Any]]:
        with self._lock:
            if self.db.execute("SELECT 1 FROM downloads LIMIT 1").fetchone() is None and legacy and legacy.exists():
                try:
                    data = json.loads(legacy.read_text(encoding="utf-8"))
                    for item in data.values() if isinstance(data, dict) else []: self.upsert_download(item)
                except (OSError, ValueError, json.JSONDecodeError): pass
            return {row["id"]: dict(row) for row in self.db.execute("SELECT * FROM downloads ORDER BY created_at DESC")}

    def upsert_download(self, item: Dict[str, Any]) -> None:
        now = item.get("updated_at", time.time())
        fields = (item.get("id"), item.get("job_id"), item.get("message_id"), item.get("chat_id"), item.get("file_name", "Cargando..."), item.get("status", "pending"), item.get("progress", 0), item.get("total_str", "0 B"), item.get("current_str", "0 B"), item.get("speed", "0 B/s"), item.get("kind"), item.get("file_path"), item.get("source"), now, item.get("created_at", now))
        with self._lock:
            self.db.execute("""INSERT INTO downloads(id,job_id,message_id,chat_id,file_name,status,progress,total_str,current_str,speed,kind,file_path,source,updated_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET job_id=excluded.job_id,message_id=excluded.message_id,chat_id=excluded.chat_id,file_name=excluded.file_name,status=excluded.status,progress=excluded.progress,total_str=excluded.total_str,current_str=excluded.current_str,speed=excluded.speed,kind=excluded.kind,file_path=excluded.file_path,source=excluded.source,updated_at=excluded.updated_at""", fields)
            self.db.commit()

    def delete_download(self, item_id: str) -> None:
        with self._lock: self.db.execute("DELETE FROM downloads WHERE id=?", (item_id,)); self.db.commit()

    def clear_finished_downloads(self) -> int:
        """Elimina solo registros terminados; nunca toca los archivos físicos."""
        with self._lock:
            self.db.execute("DELETE FROM download_chunks WHERE download_id IN (SELECT id FROM downloads WHERE status IN ('completed','skipped','failed','cancelled'))")
            cursor = self.db.execute("DELETE FROM downloads WHERE status IN ('completed','skipped','failed','cancelled')")
            self.db.commit()
            return cursor.rowcount

    def chunks(self, item_id: str) -> set[int]:
        with self._lock: return {r[0] for r in self.db.execute("SELECT chunk_index FROM download_chunks WHERE download_id=?", (item_id,))}

    def add_chunk(self, item_id: str, index: int) -> None:
        with self._lock: self.db.execute("INSERT OR IGNORE INTO download_chunks(download_id,chunk_index) VALUES(?,?)", (item_id, index)); self.db.commit()
