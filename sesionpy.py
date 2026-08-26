import asyncio
import os
from pathlib import Path

from dotenv import load_dotenv

load_dotenv(Path(__file__).resolve().parent / ".env")

# Compatibilidad con Python 3.14
asyncio.set_event_loop(asyncio.new_event_loop())

from pyrogram import Client

API_ID = os.getenv("TGDL_API_ID")
API_HASH = os.getenv("TGDL_API_HASH")


async def main():
    if not API_ID or not API_HASH:
        raise RuntimeError("Define TGDL_API_ID y TGDL_API_HASH antes de crear la sesión")

    # Asignamos el nombre exacto 'downloader_session' para que genere 'downloader_session.session'
    app = Client(
        "downloader_session",
        api_id=API_ID,
        api_hash=API_HASH,
        app_version="4.16.3 x64",
        device_model="PC 64bit",
        system_version="Windows 11",
        lang_code="es",
    )

    print("🚀 Iniciando creación de la sesión...")
    # app.start() maneja de forma automática el pedido de teléfono, código y clave 2FA
    await app.start()

    me = await app.get_me()

    print("\n" + "=" * 50)
    print("🎉 ¡ARCHIVO .SESSION GENERADO CON ÉXITO!")
    print("=" * 50)
    print(f"👤 Usuario: {me.first_name} (@{me.username})")
    print("📁 Archivo guardado: downloader_session.session")
    print("=" * 50 + "\n")

    await app.stop()


if __name__ == "__main__":
    asyncio.run(main())
