# TelegramDL 🤖

Panel web moderno y aplicación portable para gestionar descargas multimedia de Telegram y automatizar la escucha de canales y grupos.

---

## 🌟 Características Principales

- **Asistente de Instalación Web (Estilo CMS / WordPress):** Si no has iniciado sesión o faltan credenciales, el propio panel web te guía paso a paso para ingresar tu `API ID`, `API HASH`, número de teléfono, código de confirmación de Telegram y contraseña 2FA. ¡Sin tocar la consola!
- **Distribución ejecutable (.exe):** Se puede empaquetar en un ejecutable autónomo para Windows que no requiere tener instalado Python ni Node.js.
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
```powershell
.\.venv\Scripts\python.exe TelegramDL.py
```

Abre en tu navegador:
```text
http://127.0.0.1:8000/dashboard/
```

---

## 📦 Cómo Compilar tu propio `.exe` (PyInstaller)

Para generar una versión portable lista para distribuir con su propio ícono:

```powershell
# 1. Asegurarte de que el frontend está compilado
npm --prefix dashboard run build

# 2. Instalar herramientas de compilación
pip install pyinstaller pillow

# 3. Compilar la aplicación
pyinstaller --noconfirm --onedir --console --icon="icon.ico" --name "TelegramDL" --add-data "dashboard/dist;dashboard/dist" TelegramDL.py
```

El ejecutable resultante se guardará en `dist/TelegramDL/TelegramDL.exe`. Puedes comprimir esa carpeta en `.zip` y compartirla.

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
