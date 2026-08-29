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
        script_path = self.base_dir / "finish_update.py"
        is_windows = platform.system() == "Windows"

        # Convertir a strings seguras para el script
        base_str = str(self.base_dir).replace('\\', '\\\\')
        src_str = str(src_path).replace('\\', '\\\\')

        script_content = f"""
import os
import shutil
import time
import sys
import subprocess

time.sleep(2)  # Esperar a que la app principal se cierre

base = r"{base_str}"
src = r"{src_str}"
keep = {json.dumps(self.preserve_files)}

try:
    # Recorrer archivos extraídos
    for root, dirs, files in os.walk(src):
        rel = os.path.relpath(root, src)
        target_dir = os.path.join(base, rel)

        if not os.path.exists(target_dir):
            os.makedirs(target_dir)

        for f in files:
            # Omitir archivos protegidos en la raíz
            if rel == "." and f in keep:
                continue

            target_file = os.path.join(target_dir, f)
            src_file = os.path.join(root, f)

            try:
                if os.path.exists(target_file):
                    os.remove(target_file)
                shutil.move(src_file, target_file)
            except Exception as e:
                print(f"Error moviendo {{f}}: {{e}}")

    print("Actualización completada.")
except Exception as e:
    print(f"Error general en el script: {{e}}")
finally:
    # Intentar reiniciar la aplicación
    exe_name = "TelegramDL.exe" if {is_windows} else "TelegramDL"
    exe_path = os.path.join(base, exe_name)

    if os.path.exists(exe_path):
        subprocess.Popen([exe_path])
    else:
        # Si no hay exe, intentar con el script python (desarrollo)
        main_py = os.path.join(base, "TelegramDL.py")
        if os.path.exists(main_py):
            subprocess.Popen([sys.executable, main_py])

    # Autodestruirse
    sys.exit(0)
"""
        with open(script_path, "w", encoding="utf-8") as f:
            f.write(script_content)

        # Ejecutar el script y cerrar la app actual
        subprocess.Popen([sys.executable, str(script_path)])
        os._exit(0)
