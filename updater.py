import os
import sys
import platform
import shutil
import zipfile
import json
import urllib.request
import subprocess
import threading
import time
from pathlib import Path

class AppUpdater:
    def __init__(self, current_version, repo_url, base_dir):
        self.current_version = current_version
        self.repo_url = repo_url
        self.base_dir = Path(base_dir)
        self.temp_dir = self.base_dir / "update_temp"
        self.progress = {
            "status": "idle",
            "downloaded": 0,
            "total": 0,
            "percentage": 0
        }
        # Archivos que se deben mantener siempre
        self.preserve_files = [
            ".env",
            "config.json",
            "downloads.json",
            "downloader_session.session",
            "downloader_session.session-journal"
        ]

    def version_to_tuple(self, v):
        try:
            # Elimina la 'v' y convierte a tupla numérica: 'v2.0.1' -> (2, 0, 1)
            return tuple(map(int, v.lstrip('v').split('.')))
        except (ValueError, AttributeError):
            return (0, 0, 0)

    def check_for_update(self):
        api_url = f"https://api.github.com/repos/{self.repo_url}/releases/latest"
        try:
            req = urllib.request.Request(api_url)
            req.add_header('User-Agent', 'TelegramDL-Updater')
            with urllib.request.urlopen(req) as response:
                latest = json.loads(response.read().decode())
                latest_tag = latest.get('tag_name', 'v0.0.0')
                if self.version_to_tuple(latest_tag) > self.version_to_tuple(self.current_version):
                    return latest
        except Exception as e:
            print(f"Error al verificar actualizaciones: {e}")
        return None

    def get_asset_for_platform(self, release):
        system = platform.system().lower()
        assets = release.get('assets', [])

        for asset in assets:
            name = asset['name'].lower()
            if system == "windows" and name.endswith(".zip") and ("win" in name or "windows" in name):
                return asset
            elif system == "linux" and name.endswith(".appimage"):
                return asset
            elif system == "darwin" and name.endswith(".zip") and ("mac" in name or "darwin" in name or "osx" in name):
                # Preferimos el ZIP para actualizar el .app en macOS
                return asset

        # Si no hay match específico, buscamos el primer zip o AppImage
        return next((a for a in assets if a['name'].endswith(('.zip', '.AppImage'))), None)

    def _download_progress_hook(self, count, block_size, total_size):
        self.progress["status"] = "downloading"
        self.progress["total"] = total_size
        self.progress["downloaded"] = count * block_size
        if total_size > 0:
            self.progress["percentage"] = min(100, int((self.progress["downloaded"] / total_size) * 100))

    def download_and_install(self, asset):
        try:
            self.progress = {"status": "starting", "downloaded": 0, "total": 0, "percentage": 0}
            if self.temp_dir.exists():
                shutil.rmtree(self.temp_dir)
            self.temp_dir.mkdir(parents=True)

            archive_path = self.temp_dir / asset['name']
            print(f"Descargando actualización: {asset['browser_download_url']}")

            urllib.request.urlretrieve(
                asset['browser_download_url'],
                archive_path,
                reporthook=self._download_progress_hook
            )

            if archive_path.suffix.lower() == '.appimage':
                self.progress["status"] = "finishing"
                self._finish_appimage_update(archive_path)
                return

            self.progress["status"] = "extracting"
            extract_path = self.temp_dir / "extracted"
            if archive_path.suffix == '.zip':
                with zipfile.ZipFile(archive_path, 'r') as zip_ref:
                    zip_ref.extractall(extract_path)
            else:
                import tarfile
                with tarfile.open(archive_path, 'r:*') as tar_ref:
                    tar_ref.extractall(extract_path)

            # --- BUSCADOR DE RAÍZ DE APLICACIÓN ---
            # Buscamos la carpeta que contiene '_internal' o es el .app
            final_src = None
            for root, dirs, files in os.walk(extract_path):
                if "_internal" in dirs or "TelegramDL.app" in dirs:
                    final_src = Path(root)
                    if "TelegramDL.app" in dirs:
                        final_src = final_src / "TelegramDL.app"
                    break

            # Si no encontramos los anteriores, buscamos donde esté el .exe
            if not final_src:
                for root, dirs, files in os.walk(extract_path):
                    if "TelegramDL.exe" in files:
                        final_src = Path(root)
                        break

            if not final_src:
                raise RuntimeError("No se encontró la estructura de la aplicación en el paquete descargado.")

            self.progress["status"] = "finishing"
            self._create_finish_script(final_src)

        except Exception as e:
            self.progress["status"] = f"error: {str(e)}"
            print(f"Error durante la instalación de la actualización: {e}")

    def _finish_appimage_update(self, new_appimage_path):
        """Específico para Linux AppImage"""
        running_appimage = os.environ.get('APPIMAGE')
        if not running_appimage:
            print("No se detectó entorno AppImage (desarrollo).")
            return

        script_path = self.base_dir / "finish_update.sh"
        appimage_filename = os.path.basename(running_appimage)

        script_content = f"""#!/bin/bash
sleep 2
pkill -9 -f "{appimage_filename}"
mv "{new_appimage_path}" "{running_appimage}"
chmod +x "{running_appimage}"
"{running_appimage}" &
rm "$0"
"""
        with open(script_path, "w", encoding="utf-8") as f:
            f.write(script_content)

        os.chmod(script_path, 0o755)
        subprocess.Popen(["/bin/bash", str(script_path)], start_new_session=True)
        os._exit(0)

    def _create_finish_script(self, src_path):
        system = platform.system()
        is_windows = system == "Windows"
        is_mac = system == "Darwin"

        if is_windows:
            script_path = self.base_dir / "finish_update.bat"
            exe_name = "TelegramDL.exe"
            exe_path = self.base_dir / exe_name

            # Archivos y carpetas a preservar (no sobreescribir ni borrar)
            exclude_files = " ".join(self.preserve_files)
            exclude_dirs = "descargas cache update_temp .git .github"

            script_content = f"""@echo off
setlocal enabledelayedexpansion
title Actualizando TelegramDL...
echo.
echo ========================================
echo   INSTALANDO ACTUALIZACION
echo ========================================
echo.

:: 1. Esperar a que el proceso se cierre completamente
echo [*] Finalizando procesos...
:wait_process
taskkill /f /im "{exe_name}" >nul 2>&1
timeout /t 1 /nobreak >nul
tasklist /FI "IMAGENAME eq {exe_name}" 2>NUL | find /I /N "{exe_name}">NUL
if "%ERRORLEVEL%"=="0" goto wait_process

echo [*] Instalando nuevos archivos...
:: Usamos Robocopy de forma silenciosa para evitar el "spam" de archivos.
:: /nfl /ndl /njh /njs ocultan listas de archivos, carpetas, cabecera y resumen.
robocopy "{src_path}" "{self.base_dir}" /e /move /is /it /xf {exclude_files} /xd {exclude_dirs} /r:5 /w:2 /nfl /ndl /njh /njs > nul

if %ERRORLEVEL% GEQ 8 (
    echo.
    echo [!] ERROR: No se pudieron copiar todos los archivos.
    echo [!] Por favor, cierra cualquier carpeta abierta y pulsa una tecla para reintentar.
    pause
    goto wait_process
)

echo [*] Limpiando...
if exist "{self.temp_dir}" rd /s /q "{self.temp_dir}" >nul 2>&1

echo [*] Actualizacion completada. Reiniciando...
start "" "{exe_path}"
endlocal
(goto) 2>nul & del "%~f0"
"""
            with open(script_path, "w", encoding="cp1252") as f:
                f.write(script_content)

            os.startfile(script_path)
        elif is_mac:
            # macOS .app bundle replacement
            script_path = self.base_dir / "finish_update.sh"
            app_name = "TelegramDL.app"
            # Asumimos que base_dir es el directorio que CONTIENE al .app si está instalado,
            # o es el MacOS dir si está dentro del bundle.
            # PyInstaller suele poner sys.executable en .app/Contents/MacOS/TelegramDL

            target_app_path = self.base_dir
            if target_app_path.name == "MacOS" and target_app_path.parent.name == "Contents":
                target_app_path = target_app_path.parent.parent # La raíz del .app

            # Si src_path es el .app descargado, queremos reemplazar el target_app_path

            script_content = f"""#!/bin/bash
sleep 2
pkill -9 -f "TelegramDL"
rm -rf "{target_app_path}"
cp -R "{src_path}" "{target_app_path.parent}"
rm -rf "{self.temp_dir}"
open "{target_app_path}"
rm "$0"
"""
            with open(script_path, "w", encoding="utf-8") as f:
                f.write(script_content)

            os.chmod(script_path, 0o755)
            subprocess.Popen(["/bin/bash", str(script_path)], start_new_session=True)
        else:
            # Linux (Standard / Internal)
            script_path = self.base_dir / "finish_update.sh"
            exe_name = "TelegramDL"
            exe_path = self.base_dir / exe_name
            exclude_args = " ".join([f'--exclude="{f}"' for f in self.preserve_files + ["descargas", "cache", "update_temp"]])

            script_content = f"""#!/bin/bash
sleep 2
pkill -9 -x "{exe_name}"
rsync -av --remove-source-files {exclude_args} "{src_path}/" "{self.base_dir}/"
rm -rf "{self.temp_dir}"
chmod +x "{exe_path}"
"{exe_path}" &
rm "$0"
"""
            with open(script_path, "w", encoding="utf-8") as f:
                f.write(script_content)

            os.chmod(script_path, 0o755)
            subprocess.Popen(["/bin/bash", str(script_path)], start_new_session=True)

        os._exit(0)
