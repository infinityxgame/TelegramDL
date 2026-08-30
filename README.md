# TelegramDL 🤖

Panel web moderno y aplicación portable para gestionar descargas multimedia de Telegram y automatizar la escucha de canales y grupos.

---

## 🌟 Características Principales

- **Interfaz Nativa (Modo Desktop):** Utiliza `pywebview` para abrir una ventana propia sin necesidad de usar el navegador, ofreciendo una experiencia de aplicación de escritorio real.
- **Asistente de Instalación Web (Estilo CMS / WordPress):** Si no has iniciado sesión o faltan credenciales, el propio panel web te guía paso a paso para ingresar tu `API ID`, `API HASH`, número de teléfono, código de confirmación de Telegram y contraseña 2FA. ¡Sin tocar la consola!
- **Distribución ejecutable (.exe):** Se puede empaquetar en un ejecutable autónomo para Windows que incluye tanto el backend Python como el frontend Vue 3.
- **Identificación de Dispositivo:** Se registra en Telegram como `TGDown Desktop` para que puedas ver y gestionar la sesión desde *Ajustes > Dispositivos* en la app oficial de Telegram.
- **Gestor de Descargas:** Permite descargar archivos individuales o lotes por enlace de mensaje con indicador de velocidad, progreso en tiempo real y soporte WebSocket.
- **Escucha Automática:** Vigilancia continua de chats/canales privados o públicos para listar o descargar archivos automáticamente.

---

## 🚀 Opción 1: Usar la Versión Compilada (Portátil)

Para distribuir la aplicación a usuarios que **no tienen Python ni Node.js instalados**:

1. Descomprime la carpeta `TelegramDL`.
2. Haz doble clic en `TelegramDL.exe`.
3. Abre la dirección que aparece en la consola (por ejemplo, `http://localhost:8000/dashboard/`).
4. Completa la configuración desde el panel web.

---

## 🛠️ Opción 2: Ejecución e Instalación desde Código Fuente

### Requisitos
- Windows 10/11
- Python 3.10 o superior
- Node.js 18 o superior y npm

### 1. Preparar el entorno Python
```powershell
python -m venv .venv
.\.venv\Scripts\Activate.ps1
python -m pip install --upgrade pip
pip install -r requirements.txt
```

### 2. Preparar e instalar dependencias del Dashboard (Vue 3)
```powershell
cd dashboard
npm install
cd ..
```

### 3. Compilar el Dashboard (Frontend)
```powershell
npm --prefix dashboard run build
```

### 4. Iniciar la aplicación

#### Modo Desktop (Ventana Nativa)
```powershell
.\.venv\Scripts\python.exe TelegramDL.py
```

#### Modo Servidor (Solo consola, acceso vía navegador)
```powershell
.\.venv\Scripts\python.exe TelegramDL.py --server
```

Abre en tu navegador (solo si usas `--server`):
```text
http://127.0.0.1:8000/dashboard/
```

---

## 📦 Compilación y Distribución

### Compilación Automática (GitHub Actions) ⚡
El repositorio cuenta con un flujo de **GitHub Actions** automatizado (`.github/workflows/release.yml`). 
Cada vez que creas y subes un **Tag de versión** (ej. `v1.0.0`), GitHub compilará automáticamente los ejecutables portátiles para **Windows**, **Linux** y **macOS** y creará un **GitHub Release** con los archivos descargables.

Para publicar una nueva versión:
```powershell
git tag v1.0.0
git push origin v1.0.0
```

---

### Compilación Manual en tu equipo (PyInstaller)

Para generar manualmente una versión portable local en Windows:

```powershell
# 1. Asegurarte de que el frontend está compilado
npm --prefix dashboard run build

# 2. Instalar herramientas de compilación
pip install pyinstaller pillow pywebview

# 3. Compilar la aplicación
# Nota: --noconsole se puede usar si no quieres ver la terminal detrás de la ventana nativa,
# pero se recomienda --console inicialmente para depuración.
pyinstaller --noconfirm --onedir --noconsole --icon="icon.ico" --name "TelegramDL" --add-data "dashboard/dist;dashboard/dist" TelegramDL.py
```

El ejecutable resultante se guardará en `dist/TelegramDL/TelegramDL.exe`.

---

## 💡 Uso del Panel Web

- **Descargas:** Pega enlaces de mensajes de Telegram y consulta el estado de las descargas activas.
- **Configuración:** Ajusta descargas simultáneas, partes por archivo, límite de velocidad y carpeta de destino.
- **Escucha:** Activa la escucha y añade IDs numéricos de grupos o chats privados. Los archivos detectados aparecerán en una lista interactiva.
- **Usuario & Sesión:** Puedes ver tu perfil conectado y un botón para **Cerrar sesión** en Telegram si deseas cambiar de cuenta.

> **Nota:** Si deseas descargar de un grupo que tiene configurado temas, debes poner el grupo en modo mensajes antes de copiar el enlace del archivo deseado.

---

## 📁 Archivos Importantes

- `TelegramDL.py`: Servidor backend FastAPI y lógica de descargas Pyrogram.
- `dashboard/src/`: Código fuente de Vue 3 (Panel Web y Asistente de Autenticación).
- `dashboard/dist/`: Bundle compilado del frontend.
- `icon.ico`: Ícono oficial generado para la aplicación ejecutable.
- `.env`: Credenciales locales (`TGDL_API_ID`, `TGDL_API_HASH`, puerto).
- `config.json`: Configuración persistente del dashboard.
