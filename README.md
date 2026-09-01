# TelegramDL 🤖

**TGDown** es una aplicación de escritorio y panel web moderno para gestionar descargas multimedia de Telegram y automatizar la escucha de canales y grupos.

La aplicación combina un backend desarrollado en Python con FastAPI y un frontend desarrollado con Vue 3. Puede ejecutarse desde el código fuente o distribuirse como aplicación portable para **Windows, Linux y macOS**.

---

## 🌟 Características principales

* **Interfaz nativa (modo Desktop):** utiliza `pywebview` para abrir el panel dentro de una ventana propia, ofreciendo una experiencia de aplicación de escritorio.

* **Panel web:** interfaz moderna desarrollada con Vue 3 para administrar descargas, configuración, sesiones y escucha automática.

* **Asistente de configuración:** si no existen credenciales configuradas, el propio panel guía al usuario durante la configuración de `API ID`, `API HASH`, número de teléfono, código de confirmación de Telegram y contraseña 2FA, sin necesidad de introducir estos datos manualmente desde la consola.

* **Aplicación portable:** puede compilarse mediante PyInstaller para ejecutarse sin instalar Python ni Node.js en el equipo del usuario final.

* **Multiplataforma:** existen builds automatizados para:

  * Windows x64
  * Linux x86_64 mediante AppImage
  * macOS Intel (`x86_64`)
  * macOS Apple Silicon (`arm64`)

* **Identificación de dispositivo:** la aplicación se registra en Telegram como `TGDown Desktop`, permitiendo administrar la sesión desde **Ajustes > Dispositivos** en Telegram.

* **Gestor de descargas:** permite descargar archivos individuales o lotes mediante enlaces de mensajes, mostrando velocidad, progreso en tiempo real y estado de las descargas mediante WebSocket.

* **Escucha automática:** permite vigilar chats y canales públicos o privados para detectar archivos y listarlos o descargarlos automáticamente.

* **Persistencia mediante SQLite:** configuración, cola, historial y progreso de descargas se almacenan localmente.

---

# 🚀 Usar una versión compilada

La forma recomendada para usuarios que no desean configurar un entorno de desarrollo es descargar una versión compilada desde **GitHub Releases**.

Las versiones publicadas contienen los siguientes formatos:

| Plataforma          | Archivo                                   |
| ------------------- | ----------------------------------------- |
| Windows             | `TelegramDL-Windows-vX.X.X.zip`           |
| Linux x86_64        | `TelegramDL-Linux-x86_64-vX.X.X.AppImage` |
| macOS Intel         | `TelegramDL-macOS-Intel-vX.X.X.dmg`       |
| macOS Apple Silicon | `TelegramDL-macOS-ARM64-vX.X.X.dmg`       |

## Windows

1. Descarga el archivo `.zip`.
2. Descomprime la carpeta.
3. Ejecuta:

```text
TelegramDL.exe
```

La aplicación iniciará el servidor local y abrirá la interfaz nativa mediante `pywebview`.

Si utilizas el modo servidor, también puedes acceder manualmente desde:

```text
http://127.0.0.1:8000/dashboard/
```

---

## Linux

Descarga:

```text
TelegramDL-Linux-x86_64-vX.X.X.AppImage
```

Dale permisos de ejecución:

```bash
chmod +x TelegramDL-Linux-x86_64-vX.X.X.AppImage
```

Y ejecútalo:

```bash
./TelegramDL-Linux-x86_64-vX.X.X.AppImage
```

El AppImage está diseñado para funcionar como una aplicación portable sin necesidad de instalar Python ni las dependencias del proyecto.

> **Nota:** el build Linux utiliza `pywebview` con GTK/PyGObject. La compatibilidad puede depender de las bibliotecas gráficas disponibles en la distribución Linux utilizada.

---

## macOS

Existen dos versiones:

### Mac Intel

```text
TelegramDL-macOS-Intel-vX.X.X.dmg
```

### Mac Apple Silicon

```text
TelegramDL-macOS-ARM64-vX.X.X.dmg
```

Abre el `.dmg`, arrastra `TGDown` a la carpeta `Applications` y ejecuta la aplicación.

### Firma Ad-Hoc

Los builds oficiales generados por GitHub Actions utilizan **firma Ad-Hoc**.

Esto permite que la aplicación tenga una firma válida a nivel técnico y evita distribuir una aplicación completamente sin firmar, pero **no equivale a una firma Developer ID de Apple ni a una aplicación notarizada**.

Dependiendo de la configuración de seguridad de macOS, el sistema puede mostrar una advertencia al abrir la aplicación por primera vez.

---

# 🛠️ Ejecutar desde el código fuente

## Requisitos generales

* Python 3.10 o superior
* Node.js 18 o superior
* npm
* Git

La versión utilizada por el workflow de GitHub Actions es:

```text
Python 3.12
Node.js 24
```

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
    libgirepository-2.0-dev \
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
pyinstaller `
    --noconfirm `
    --clean `
    --onedir `
    --noconsole `
    --icon="icon.ico" `
    --name "TelegramDL" `
    --add-data "dashboard/dist;dashboard/dist" `
    TelegramDL.py
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
Compress-Archive `
    -Path dist/TelegramDL/* `
    -DestinationPath "TelegramDL-Windows.zip"
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
pyinstaller \
    --noconfirm \
    --clean \
    --onedir \
    --name "TelegramDL" \
    --add-data "dashboard/dist:dashboard/dist" \
    --collect-submodules gi \
    --collect-submodules gi.repository \
    --hidden-import gi \
    --hidden-import gi.repository.Gtk \
    --hidden-import gi.repository.Gdk \
    --hidden-import gi.repository.GLib \
    --hidden-import gi.repository.GObject \
    --hidden-import webview.platforms.gtk \
    TelegramDL.py
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

## macOS Intel

La compilación utiliza:

```text
x86_64
```

Comando:

```bash
pyinstaller \
    --noconfirm \
    --clean \
    --onedir \
    --windowed \
    --icon="icon.icns" \
    --target-arch x86_64 \
    --osx-bundle-identifier "com.infinityxgame.tgdown" \
    --name "TelegramDL" \
    --add-data "dashboard/dist:dashboard/dist" \
    TelegramDL.py
```

---

## macOS Apple Silicon

Para Macs con procesadores Apple Silicon:

```text
arm64
```

Comando:

```bash
pyinstaller \
    --noconfirm \
    --clean \
    --onedir \
    --windowed \
    --icon="icon.icns" \
    --target-arch arm64 \
    --osx-bundle-identifier "com.infinityxgame.tgdown" \
    --name "TelegramDL" \
    --add-data "dashboard/dist:dashboard/dist" \
    TelegramDL.py
```

El resultado será:

```text
dist/TelegramDL.app
```

---

# 🔐 Firma Ad-Hoc de macOS

Para firmar localmente la aplicación:

```bash
codesign \
    --force \
    --deep \
    --sign - \
    "dist/TelegramDL.app"
```

Comprobar la firma:

```bash
codesign \
    --verify \
    --deep \
    --strict \
    --verbose=2 \
    "dist/TelegramDL.app"
```

Comprobar la arquitectura:

```bash
file "dist/TelegramDL.app/Contents/MacOS/TelegramDL"
```

---

# 💿 Crear un DMG en macOS

Una vez compilada y firmada la aplicación:

```bash
rm -rf dmg
mkdir dmg
```

Copiar la aplicación:

```bash
cp -R "dist/TelegramDL.app" dmg/
```

Crear el acceso a Applications:

```bash
ln -s /Applications dmg/Applications
```

Crear el DMG:

```bash
hdiutil create \
    -volname "TGDown" \
    -srcfolder dmg \
    -ov \
    -format UDZO \
    "TelegramDL.dmg"
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
macOS Intel
macOS ARM64
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
TelegramDL-macOS-Intel-v2.0.9.dmg
TelegramDL-macOS-ARM64-v2.0.9.dmg
```

---

## Crear una nueva versión

Después de realizar los cambios:

```bash
git add .
git commit -m "Release v2.0.9"
```

Crear el tag:

```bash
git tag v2.0.9
```

Subirlo a GitHub:

```bash
git push origin v2.0.9
```

El workflow se ejecutará automáticamente y creará el **GitHub Release** con los cuatro archivos compilados.

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
