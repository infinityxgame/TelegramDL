# TelegramDL (TGDown)

TelegramDL es un gestor de descargas de Telegram para escritorio, desarrollado con Go, Wails v2 y Vue 3. Está pensado exclusivamente para recibir, vigilar y descargar contenido multimedia; no es un cliente de mensajería.

## Características

- Descarga de documentos, vídeos, audios, fotos y stickers desde enlaces de mensajes de Telegram.
- Rangos de mensajes, por ejemplo `https://t.me/c/2121902112/31449-31455`.
- Descargas paralelas por fragmentos con hasta 8 workers por archivo.
- Reanudación desde los fragmentos ya descargados sin transferirlos de nuevo.
- Pausar, reanudar, cancelar y reintentar descargas.
- Historial persistente en SQLite.
- Escucha de canales, grupos y chats con filtros por tipo de contenido.
- Descarga automática o detección para descarga manual.
- Panel web y aplicación de escritorio mediante Wails.
- Indicador de velocidad suavizado para evitar picos falsos.

## Tecnologías

- Go 1.22 o superior.
- [gotd/td](https://github.com/gotd/td) para MTProto.
- [Wails v2](https://wails.io/) para la aplicación de escritorio.
- Vue 3 + Vite para el panel.
- SQLite mediante `modernc.org/sqlite`, sin CGO.

## Requisitos

- Go instalado.
- Node.js 18 o superior y npm.
- Wails CLI:

```powershell
go install github.com/wailsapp/wails/v2/cmd/wails@latest
```

Para usar Telegram también se necesita un `API ID` y un `API HASH` obtenidos desde [my.telegram.org](https://my.telegram.org/).

## Configuración

Las credenciales se pueden introducir desde el asistente de autenticación o mediante un archivo `.env` local:

```env
TGDL_API_ID=tu_api_id
TGDL_API_HASH=tu_api_hash
TGDL_BIND_HOST=127.0.0.1
TGDL_PORT=8000
```

No se deben subir credenciales, archivos de sesión ni bases de datos al repositorio.

La configuración y el historial se guardan en:

- Windows: `%USERPROFILE%\\.tgdown\\tgdown.sqlite3`
- Linux/macOS: `~/.tgdown/tgdown.sqlite3`

## Desarrollo

Instalar las dependencias del panel:

```powershell
npm --prefix dashboard install
```

Ejecutar la aplicación con recarga automática:

```powershell
wails dev
```

Compilar el panel por separado:

```powershell
npm --prefix dashboard run build
```

Compilar la aplicación completa de escritorio con Wails:

```powershell
wails build
```

Wails ejecuta la compilación del panel y del backend según la configuración de `wails.json`. El ejecutable se genera dentro de `build/bin/`. Este proyecto no utiliza PyInstaller.

`dashboard/dist/` es generado por Vite y debe existir para que `main.go` pueda incrustar el panel durante la compilación; `build/`, `dist/` y `dashboard/node_modules/` son artefactos locales prescindibles.

### Modos de ejecución desde terminal

El ejecutable acepta estos parámetros:

```powershell
TGDown.exe --server
TGDown.exe --update
TGDown.exe --help
```

- `--server` inicia el servidor HTTP/WebSocket sin abrir la ventana de Wails y permanece activo hasta recibir `Ctrl+C`.
- `--update` busca e instala la última versión compatible con el sistema operativo.
- `--help` muestra los parámetros disponibles.

## Pruebas y validación

```powershell
go test ./...
go vet ./...
npm --prefix dashboard run build
```

La validación del proyecto se realiza mediante la compilación de Go, `go vet` y la compilación del panel.

## Arquitectura

```text
TelegramDL/
├── main.go
├── app.go
├── pkg/
│   ├── config/       Configuración y valores predeterminados
│   ├── downloader/   Motor, fragmentos, progreso y reanudación
│   ├── listener/     Escucha de mensajes y filtros multimedia
│   ├── server/       API HTTP, WebSocket y snapshots de estado
│   ├── storage/      SQLite y migraciones
│   ├── telegram/     Cliente MTProto y autenticación
│   └── updater/       Comprobación e instalación de actualizaciones
├── dashboard/        Panel Vue 3
├── RESUMEN_PROYECTO.md
└── wails.json
```

## API local

El servidor interno escucha por defecto en `127.0.0.1:8000`.

- `GET /api/downloads`: lista de descargas.
- `POST /api/download`: añade un enlace o rango.
- `POST /api/pause`, `/api/resume`, `/api/cancel` y `/api/retry`: control de tareas.
- `GET /api/ws`: estado en tiempo real mediante WebSocket.
- `GET /api/listener`: multimedia detectada por la escucha.

## Licencia

Consulta [LICENSE](LICENSE) para conocer las condiciones de uso y distribución.
