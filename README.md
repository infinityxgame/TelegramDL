# TelegramDL (TGDown)

TelegramDL es un gestor de descargas de Telegram para escritorio, desarrollado con Go, Wails v2 y Vue 3. Está pensado exclusivamente para recibir, vigilar y descargar contenido multimedia; no es un cliente de mensajería.

> 📖 **Documentación oficial (Español / English)**: [https://infinityxgame.github.io/TelegramDL/](https://infinityxgame.github.io/TelegramDL/)

> 📖 **Grupo de soporte en Telegram**: [TelegramDL Grupo Soporte](https://t.me/tgdownrepo)

## Características

- Descarga de documentos, vídeos, audios, fotos y stickers desde enlaces de mensajes de Telegram.
- Descarga en lote de múltiples enlaces pegados en una sola entrada (separados por espacio o salto de línea) respetando el orden secuencial.
- Rangos de mensajes, por ejemplo `https://t.me/c/2121902112/31449-31455`.
- Descarga de canales con temas igual copiando su enlace.
- Posibilidad de poner en la escucha un grupo con temas, este le da la posibilidad de escuchar todo el grupo o un tema específico
- Descargas paralelas por fragmentos con hasta 8 workers por archivo.
- Reanudación desde los fragmentos ya descargados sin transferirlos de nuevo.
- Pausar, reanudar, cancelar y reintentar descargas.
- Historial persistente en SQLite.
- Escucha de canales, grupos y chats con filtros por tipo de contenido.
- Escáner histórico para buscar y descargar contenido pasado en chats, canales y bots, con rango de fechas de 3 meses por defecto, caché inteligente por rangos e inspección/ordenación interactiva por columnas.
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

Al abrir TelegramDL por primera vez ya sea por la aplicación nativa o por el navegador mediante el modo `--server` la interfaz mostrará un asistente interactivo que te solicitará:
1. `API ID` y `API HASH`.
2. Número de teléfono internacional (ejemplo: `+34600000000`).
3. Código de verificación (enviado por la app oficial de Telegram o SMS).
4. Contraseña de verificación en dos pasos (2FA), en caso de tenerla activada.

:::caution[IMPORTANTE]
Nunca compartas tu `API ID`, `API HASH`, ni los archivos de sesión generados (`tg_session.jsontg_session.json`) con terceros por seguridad propia.
:::

TelegramDL guarda la configuración, sesiones y base de datos en una ruta dedicada en tu directorio de usuario:

- **Windows**: `%USERPROFILE%\.tgdown\tgdown.sqlite3`
- **Linux / macOS**: `~/.tgdown/tgdown.sqlite3`

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
TelegramDL.exe --server
TelegramDL.exe --update
TelegramDL.exe --help
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

## Acceso remoto

Casi toda la API (`/api/*`) exige un token de acceso (visible y regenerable en
**Ajustes → Acceso remoto** dentro de la app), enviado en la cabecera
`Authorization: Bearer`. Son públicos solo unos pocos endpoints (`/api/theme`,
`/api/auth/has-credentials`, `/api/auth/token/send` con su límite de
frecuencia y `/api/ipc/download` desde el propio equipo), y los endpoints de
la primera configuración de Telegram responden sin token únicamente mientras
no haya sesión guardada. Para controlar TelegramDL
desde otro dispositivo (celular, otra PC) sin exponer el puerto a internet,
consulta [REMOTE_ACCESS.md](REMOTE_ACCESS.md).

## API local

El servidor interno escucha por defecto en `127.0.0.1:8000`.

- `GET /api/downloads`: lista de descargas.
- `POST /api/download`: añade un enlace o rango.
- `POST /api/pause`, `/api/resume`, `/api/cancel` y `/api/retry`: control de tareas.
- `GET /api/ws`: estado en tiempo real mediante WebSocket.
- `GET /api/listener`: multimedia detectada por la escucha.

## Contribuir

¿Quieres reportar un bug o proponer un cambio? Consulta [CONTRIBUTING.md](CONTRIBUTING.md)
para la guía de contribución (entorno de desarrollo, convenciones y checklist
antes de abrir un pull request).

## Colaboradores

¡Gracias a todas las personas que contribuyen a **TelegramDL**!

<!-- CONTRIBUTORS-START -->
<a href="https://github.com/ElJoker63" title="ElJoker63">
  <img src="https://avatars.githubusercontent.com/u/28916466?v=4" width="48" height="48" style="border-radius: 50%; margin: 3px;" alt="ElJoker63" />
</a>
<a href="https://github.com/Tauromachian" title="Tauromachian">
  <img src="https://avatars.githubusercontent.com/u/54786476?v=4" width="48" height="48" style="border-radius: 50%; margin: 3px;" alt="Tauromachian" />
</a>
<a href="https://github.com/SatellitexOT12" title="SatellitexOT12">
  <img src="https://avatars.githubusercontent.com/u/82134529?v=4" width="48" height="48" style="border-radius: 50%; margin: 3px;" alt="SatellitexOT12" />
</a>
<!-- CONTRIBUTORS-END -->

## Licencia

Consulta [LICENSE](LICENSE) para conocer las condiciones de uso y distribución.
