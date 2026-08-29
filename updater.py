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

        # Primero buscamos coincidencias específicas del sistema
        target_asset = None
        for asset in assets:
            name = asset['name'].lower()
            if system == "windows" and ("win" in name or "windows" in name):
                target_asset = asset
                break
            elif system == "linux" and "linux" in name:
                target_asset = asset
                break
            elif system == "darwin" and ("mac" in name or "darwin" in name or "osx" in name):
                target_asset = asset
                break

        # Si no hay match específico, buscamos el primer zip o tar.gz
        if not target_asset:
            target_asset = next((a for a in assets if a['name'].endswith(('.zip', '.tar.gz'))), None)

        return target_asset

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

            self.progress["status"] = "extracting"
            extract_path = self.temp_dir / "extracted"
            if archive_path.suffix == '.zip':
                with zipfile.ZipFile(archive_path, 'r') as zip_ref:
                    zip_ref.extractall(extract_path)
            else:
                import tarfile
                with tarfile.open(archive_path, 'r:*') as tar_ref:
                    tar_ref.extractall(extract_path)

            # Si el zip contenía una sola carpeta, entramos en ella
            extracted_items = list(extract_path.iterdir())
            if len(extracted_items) == 1 and extracted_items[0].is_dir():
                final_src = extracted_items[0]
            else:
                final_src = extract_path

            self.progress["status"] = "finishing"
            # Crear script de finalización para Windows/Linux/Mac
            self._create_finish_script(final_src)

        except Exception as e:
            self.progress["status"] = f"error: {str(e)}"
            print(f"Error durante la instalación de la actualización: {e}")
            if self.temp_dir.exists():
                shutil.rmtree(self.temp_dir)

    def _create_finish_script(self, src_path):
        is_windows = platform.system() == "Windows"

        if is_windows:
            script_path = self.base_dir / "finish_update.bat"
            exe_name = "TelegramDL.exe"
            exe_path = self.base_dir / exe_name

            # Lista de exclusiones para Robocopy
            exclude_files = " ".join(self.preserve_files)

            # Script usando Robocopy (mucho más robusto)
            script_content = f"""@echo off
setlocal
title Actualizando TelegramDL...
echo Esperando a que la aplicacion se cierre...
timeout /t 3 /nobreak > nul

:: Asegurar que el proceso este muerto
taskkill /f /im "{exe_name}" >nul 2>&1

echo Instalando archivos nuevos...
:: /e = subdirectorios (incl. vacios)
:: /move = mueve archivos (los borra del origen)
:: /y = sobreescribe sin preguntar
:: /xf = excluye los archivos de configuracion para no borrarlos ni pisarlos
robocopy "{src_path}" "{self.base_dir}" /e /move /y /xf {exclude_files} /r:3 /w:2 > nul

echo Limpiando...
rd /s /q "{self.temp_dir}" 2>nul

echo Reiniciando aplicacion...
start "" "{exe_path}"
endlocal
(goto) 2>nul & del "%~f0"
"""
            with open(script_path, "w", encoding="cp1252") as f:
                f.write(script_content)

            os.startfile(script_path)
        else:
            # Linux / Mac (usando rsync si está disponible, es el equivalente a robocopy)
            script_path = self.base_dir / "finish_update.sh"
            exe_name = "TelegramDL"
            exe_path = self.base_dir / exe_name
            exclude_args = " ".join([f'--exclude="{f}"' for f in self.preserve_files])

            script_content = f"""#!/bin/bash
sleep 3
pkill -f "{exe_name}"
rsync -a --remove-source-files {exclude_args} "{src_path}/" "{self.base_dir}/"
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
