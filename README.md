# TelegramDL

Panel web para gestionar descargas multimedia de Telegram y escuchar chats configurados.

## Requisitos

- Windows 10/11
- Python 3.10 o superior
- Node.js 18 o superior y npm
- Credenciales de Telegram: `api_id` y `api_hash`

## Instalación desde cero

Abre PowerShell en la raíz del proyecto:

```powershell
python -m venv .venv
.\.venv\Scripts\Activate.ps1
python -m pip install --upgrade pip
pip install -r requirements.txt
```

Instala las dependencias del dashboard:

```powershell
cd dashboard
npm install
cd ..
```

## Configuración

1. Copia `.env.example` como `.env`.
2. Edita `.env` y añade tus credenciales de Telegram:

```env
TGDL_API_ID=123456
TGDL_API_HASH=tu_api_hash
TGDL_BIND_HOST=0.0.0.0
TGDL_PORT=8000
```

`config.json` se crea automáticamente al iniciar el programa. Guarda ahí las preferencias del dashboard, los chats vigilados y la carpeta de descargas. No se debe subir al repositorio.

## Generar el dashboard

Desde la raíz del proyecto:

```powershell
npm --prefix dashboard run build
```

O desde la carpeta `dashboard`:

```powershell
cd dashboard
npm run build
```

El resultado se genera en `dashboard/dist`. Esta carpeta está ignorada por Git y debe regenerarse en cada instalación o después de modificar el frontend.

## Ejecutar la aplicación

Desde la raíz, con el entorno virtual activo:

```powershell
.\.venv\Scripts\python.exe .\TelegramDL.py
```

Abre el panel en el propio equipo usando `127.0.0.1`, o desde otro dispositivo usando la IP local del PC, por ejemplo `192.168.1.50`:

```text
http://192.168.1.50:8000/dashboard/
```

La primera vez que se conecte Telegram puede solicitar el número de teléfono, el código de acceso y, si está activada, la contraseña de verificación en dos pasos. La sesión se guarda localmente y está excluida de Git.

## Desarrollo del frontend

Para trabajar con recarga automática:

```powershell
cd dashboard
npm run dev
```

Abre la URL que muestre Vite, normalmente:

```text
http://192.168.1.50:8080/dashboard/
```

El servidor de desarrollo redirige las peticiones `/api` al backend de `http://127.0.0.1:8000`. Por tanto, el backend debe estar ejecutándose en otra terminal. Vite escucha en todas las interfaces para permitir el acceso desde la red local.

## Uso del panel

- **Descargas:** añade enlaces de mensajes de Telegram y consulta el progreso.
- **Configuración:** ajusta descargas simultáneas, partes por archivo, límite de velocidad y carpeta de destino.
- **Escucha:** activa la escucha y añade IDs numéricos de grupos o chats privados. Cada ID puede eliminarse desde el propio panel y los cambios se guardan en `config.json`. Los archivos detectados aparecen en una lista donde pueden descargarse o eliminarse si no se desean.

**Nota:** Si desean descargar de un grupo que tiene configurado los temas deben poner el grupo en modo mensajes y entonces copiar el enlace del archivo deseado o el rango deseado, si intentan copiar la url sin estar en modo mensaje les dará un error de URL inválida.

## Archivos importantes

- `TelegramDL.py`: backend, API y lógica de descargas.
- `dashboard/src/`: código Vue del frontend.
- `dashboard/dist/`: frontend compilado localmente, no versionado.
- `.env`: credenciales y variables de ejecución, no versionado.
- `config.json`: configuración persistente local, no versionado.
- `descargas/`: archivos descargados, no versionado.

## Problemas frecuentes

Si el navegador muestra una versión antigua del panel, detén el backend, ejecuta de nuevo `npm --prefix dashboard run build`, inicia `TelegramDL.py` y recarga con `Ctrl+F5`.

Si el puerto `5173` falla en Windows por permisos, el desarrollo usa `0.0.0.0:8080`. Desde otro dispositivo se debe abrir la IP local del PC, por ejemplo `http://192.168.1.50:8080/dashboard/`.

Para acceder desde otro dispositivo, permite el puerto `8000` en el Firewall de Windows si el sistema lo solicita y asegúrate de que ambos dispositivos estén en la misma red. No expongas el servidor directamente a Internet sin añadir autenticación y HTTPS.
