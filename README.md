# TelegramDL 🤖

**TGDown** es una aplicación de escritorio nativa de alto rendimiento y panel web moderno para gestionar descargas multimedia de Telegram y automatizar la escucha de canales y grupos.

La aplicación combina un backend nativo de alta velocidad desarrollado en **Go** (utilizando el cliente MTProto oficial `gotd/td` y persistencia SQLite embebida sin CGO) con una interfaz de usuario moderna desarrollada en **Vue 3 + Vite**, empaquetada e integrada para el escritorio mediante **Wails v2**.

---

## 🌟 Características principales

* **Interfaz nativa (Desktop con Wails v2):** utiliza el motor webview nativo del sistema operativo (WebView2 en Windows) para una experiencia de escritorio ultraligera, con arranque instantáneo y mínimo consumo de memoria RAM.

* **Motor MTProto en Go:** descargas concurrentes por fragmentos de alta velocidad, soporte para CDN y control de límite de ancho de banda.

* **Panel web moderno:** interfaz desarrollada con Vue 3 para administrar descargas, configuración, sesiones y escucha automática.

* **Asistente de configuración integrado:** el panel guía al usuario durante la configuración de `API ID`, `API HASH`, número de teléfono, código de confirmación de Telegram y contraseña 2FA.

* **Persistencia local segura:** base de datos SQLite integrada (`modernc.org/sqlite`) con soporte de transacciones WAL para historial de descargas y reanudación de fragmentos.

* **Escucha automática:** vigila chats y canales públicos o privados para detectar archivos multimedia y listarlos o descargarlos automáticamente con filtros personalizables.

---

# 🛠️ Desarrollo y Compilación

## Requisitos previos

* **Go 1.22 o superior** (probado con Go 1.27)
* **Wails v2 CLI** (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`)
* **Node.js 18 o superior** (con npm)
* **Git**

## Ejecutar en modo desarrollo

Para ejecutar la aplicación con hot-reload en Go y Vite simultáneamente:

```bash
wails dev
```

## Compilar binario de producción

Para compilar el ejecutable portable independiente:

```bash
wails build
```

El ejecutable resultante se encontrará en `build/bin/tgdown.exe` (en Windows) o `build/bin/tgdown` (en Linux/macOS).


---

# 🪟 Windows

## 1. Crear entorno virtual

Desde la raíz del proyecto:

```powershell
python -m venv .venv
```

Activar el entorno:

```powershell
.\.venv\Scripts\Activate.ps1
```

Actualizar `pip`:

```powershell
python -m pip install --upgrade pip
```

Instalar las dependencias:

```powershell
pip install -r requirements.txt
```

---

## 2. Instalar dependencias del Dashboard

```powershell
cd dashboard
npm install
cd ..
```

---

## 3. Compilar el Dashboard

```powershell
npm --prefix dashboard run build
```

---

## 4. Ejecutar TGDown

### Modo Desktop

```powershell
.\.venv\Scripts\python.exe TelegramDL.py
```

### Modo servidor

```powershell
.\.venv\Scripts\python.exe TelegramDL.py --server
```

En modo servidor, abre:

```text
http://127.0.0.1:8000/dashboard/
```

---

# 🐧 Linux

Linux requiere algunas dependencias adicionales para que `pywebview` pueda utilizar GTK mediante PyGObject.

## 1. Instalar dependencias del sistema

En distribuciones basadas en Debian/Ubuntu:

```bash
sudo apt update

sudo apt install -y \
    build-essential \
    pkg-config \
    libcairo2-dev \
    libgirepository1.0-dev \
    gir1.2-gtk-3.0 \
    libgtk-3-dev \
    libglib2.0-dev \
    libffi-dev \
    patchelf
```

> Los nombres de los paquetes pueden variar entre distribuciones Linux.

---

## 2. Crear el entorno virtual

```bash
python3 -m venv .venv
```

Activarlo:

```bash
source .venv/bin/activate
```

Actualizar `pip`:

```bash
python -m pip install --upgrade pip
```

---

## 3. Instalar dependencias Python

Primero:

```bash
pip install -r requirements.txt
```

Después, las dependencias específicas de Linux:

```bash
pip install -r requirements-linux.txt
```

El archivo `requirements-linux.txt` contiene las dependencias necesarias para GTK/PyGObject, entre ellas:

```text
PyGObject
```

---

## 4. Comprobar PyGObject y GTK

Antes de compilar, se puede comprobar que GTK está disponible:

```bash
python -c "
import gi
gi.require_version('Gtk', '3.0')
from gi.repository import Gtk, GLib, GObject
print('PyGObject and GTK OK')
"
```

Si aparece:

```text
PyGObject and GTK OK
```

el entorno Python puede cargar GTK correctamente.

---

## 5. Instalar dependencias del Dashboard

```bash
cd dashboard
npm install
cd ..
```

Compilar:

```bash
npm --prefix dashboard run build
```

---

## 6. Ejecutar desde el código fuente

### Modo Desktop

```bash
.venv/bin/python TelegramDL.py
```

### Modo servidor

```bash
.venv/bin/python TelegramDL.py --server
```

---

# 🍎 macOS

El proyecto permite compilar tanto para Intel como para Apple Silicon.

## 1. Crear entorno virtual

```bash
python3 -m venv .venv
```

Activarlo:

```bash
source .venv/bin/activate
```

Actualizar `pip`:

```bash
python -m pip install --upgrade pip
```

Instalar dependencias:

```bash
pip install -r requirements.txt
```

---

## 2. Instalar y compilar el Dashboard

```bash
cd dashboard
npm install
npm run build
cd ..
```

---

## 3. Ejecutar desde el código fuente

### Modo Desktop

```bash
.venv/bin/python TelegramDL.py
```

### Modo servidor

```bash
.venv/bin/python TelegramDL.py --server
```

---

# 📦 Compilación con PyInstaller

La aplicación utiliza **PyInstaller** para generar las versiones portables.

Antes de compilar, asegúrate de haber generado el frontend:

```bash
npm --prefix dashboard run build
```

---

# 🪟 Compilar para Windows

Instalar PyInstaller:

```powershell
pip install pyinstaller
```

Compilar:

```powershell
pyinstaller --noconfirm --clean --onedir --noconsole --icon="icon.ico" --name "TelegramDL" --add-data "dashboard/dist;dashboard/dist" TelegramDL.py
```

El resultado estará en:

```text
dist/TelegramDL/
```

Y el ejecutable principal será:

```text
dist/TelegramDL/TelegramDL.exe
```

Para distribuirlo como ZIP:

```powershell
Compress-Archive -Path dist/TelegramDL/* -DestinationPath "TelegramDL-Windows.zip"
```

---

# 🐧 Compilar para Linux

Primero instala las dependencias específicas de Linux descritas anteriormente.

Instala PyInstaller:

```bash
pip install pyinstaller
```

Compila:

```bash
pyinstaller --noconfirm --clean --onedir --name "TelegramDL" --add-data "dashboard/dist:dashboard/dist" --collect-submodules gi --collect-submodules gi.repository --hidden-import gi --hidden-import gi.repository.Gtk --hidden-import gi.repository.Gdk --hidden-import gi.repository.GLib --hidden-import gi.repository.GObject --hidden-import webview.platforms.gtk TelegramDL.py
```

El bundle generado estará en:

```text
dist/TelegramDL/
```

### AppImage

El workflow oficial utiliza `linuxdeploy` para convertir el bundle de PyInstaller en un AppImage portable.

El resultado tiene el formato:

```text
TelegramDL-Linux-x86_64-vX.X.X.AppImage
```

Para una distribución Linux diferente a Ubuntu/Debian, se recomienda utilizar el AppImage generado por GitHub Actions en lugar de realizar manualmente el proceso de empaquetado.

---

# 🍎 Compilar para macOS

El repositorio contiene directamente el icono de macOS:

```text
icon.icns
```

Por lo tanto, **no es necesario generar el ICNS durante la compilación**.

Los tres formatos de icono del proyecto son:

```text
icon.png
icon.ico
icon.icns
```

| Archivo     | Plataforma       |
| ----------- | ---------------- |
| `icon.png`  | Linux / AppImage |
| `icon.ico`  | Windows          |
| `icon.icns` | macOS            |

---

## macOS Universal

La compilación utiliza:

```text
universal2
```

Comando:

```bash
pyinstaller --noconfirm --clean --onedir --windowed --icon="icon.icns" --target-arch universal2 --osx-bundle-identifier "com.infinityxgame.tgdown" --name "TelegramDL" --add-data "dashboard/dist:dashboard/dist" TelegramDL.py
```

---

# 🔐 Firma Ad-Hoc de macOS

Para firmar localmente la aplicación:

```bash
codesign --force --deep --sign - "dist/TelegramDL.app"
```

Comprobar la firma:

```bash
codesign --verify --deep --strict --verbose=2 "dist/TelegramDL.app"
```

Comprobar la arquitectura:

```bash
file "dist/TelegramDL.app/Contents/MacOS/TelegramDL"
```

---

# ⚡ GitHub Actions

El repositorio incluye un workflow automatizado en:

```text
.github/workflows/release.yml
```

El workflow compila automáticamente las cuatro versiones:

```text
Windows
Linux x86_64
macOS Universal
```

Cada compilación utiliza el tag de Git como número de versión.

Por ejemplo:

```text
v2.0.9
```

generará archivos similares a:

```text
TelegramDL-Windows-v2.0.9.zip
TelegramDL-Linux-x86_64-v2.0.9.AppImage
TelegramDL-macOS-Universal-v2.0.9.zip
```

---

# 📁 Estructura importante del proyecto

```text
TelegramDL/
│
├── TelegramDL.py
│
├── requirements.txt
├── requirements-linux.txt
│
├── icon.png
├── icon.ico
├── icon.icns
│
├── dashboard/
│   ├── src/
│   ├── package.json
│   └── dist/
│
└── .github/
    └── workflows/
        └── release.yml
```

### Archivos principales

| Archivo                         | Descripción                                                    |
| ------------------------------- | -------------------------------------------------------------- |
| `TelegramDL.py`                 | Backend FastAPI y lógica principal de Telegram/descargas       |
| `requirements.txt`              | Dependencias Python comunes                                    |
| `requirements-linux.txt`        | Dependencias Python específicas de Linux, incluyendo PyGObject |
| `dashboard/src/`                | Código fuente del frontend Vue 3                               |
| `dashboard/dist/`               | Frontend Vue compilado                                         |
| `icon.png`                      | Icono utilizado para Linux/AppImage                            |
| `icon.ico`                      | Icono utilizado para Windows                                   |
| `icon.icns`                     | Icono utilizado para macOS                                     |
| `.github/workflows/release.yml` | Automatización de builds y releases                            |

---

# 💾 Datos locales

TGDown almacena sus datos de configuración en la carpeta `.tgdown` del usuario.

### Windows

```text
%USERPROFILE%\.tgdown\
```

### Linux / macOS

```text
~/.tgdown/
```

Dentro de esta carpeta se encuentran, entre otros:

```text
.env
tgdown.sqlite3
```

El archivo `.env` contiene información de configuración local como:

```text
TGDL_API_ID
TGDL_API_HASH
```

Además de la configuración relacionada con el puerto y otros parámetros de la aplicación.

La base de datos:

```text
tgdown.sqlite3
```

almacena la configuración, cola de descargas, historial y progreso por fragmentos.

---

# 🔄 Migración desde versiones anteriores

En la primera ejecución, TGDown puede migrar automáticamente los archivos antiguos:

```text
config.json
downloads.json
```

a la nueva base de datos SQLite:

```text
tgdown.sqlite3
```

Los archivos JSON antiguos no se vuelven a escribir y pueden conservarse como respaldo.

---

# 💡 Uso del panel web

## Descargas

Permite pegar enlaces de mensajes de Telegram y consultar el estado de las descargas activas.

## Configuración

Permite configurar parámetros como:

* Descargas simultáneas.
* Partes por archivo.
* Límite de velocidad.
* Carpeta de destino.

## Escucha automática

Permite activar la vigilancia de chats o canales y añadir los IDs numéricos correspondientes.

Los archivos detectados aparecen automáticamente en la interfaz.

## Usuario y sesión

El panel permite consultar el usuario conectado y cerrar la sesión de Telegram para cambiar de cuenta.

> **Nota:** si deseas descargar un archivo de un grupo que utiliza temas, debes poner el grupo en modo mensajes antes de copiar el enlace del archivo.

---

# ⚠️ Consideraciones de seguridad

Las credenciales de Telegram se almacenan localmente en el equipo del usuario.

No compartas tu:

```text
TGDL_API_ID
TGDL_API_HASH
```

ni los archivos de sesión o configuración con terceros.

La aplicación utiliza la API oficial de Telegram mediante las bibliotecas correspondientes y la sesión queda asociada al dispositivo `TGDown Desktop`.

---

# 📄 Licencia

Consulta el archivo `LICENSE` incluido en este repositorio para conocer las condiciones de uso y distribución de TelegramDL/TGDown.
