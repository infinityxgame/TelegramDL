# Contribuir a TelegramDL (TGDown)

Gracias por tu interés en mejorar TelegramDL. Esta guía resume cómo preparar
el entorno, qué convenciones sigue el proyecto y cómo se espera que se
propongan los cambios.

## Antes de empezar

- Revisa los issues y pull requests abiertos para evitar duplicar trabajo.
- Para cambios grandes (refactors, nuevas dependencias, cambios de
  arquitectura), abre primero un issue describiendo la propuesta antes de
  invertir tiempo en la implementación.
- Este proyecto está pensado exclusivamente para recibir, vigilar y
  descargar contenido multimedia de Telegram; no se aceptan cambios que lo
  conviertan en un cliente de mensajería general.

## Preparar el entorno de desarrollo

Requisitos: Go 1.27 o superior, Node.js 18 o superior y npm, y la CLI de
Wails:

```powershell
go install github.com/wailsapp/wails/v2/cmd/wails@latest
npm --prefix dashboard install
```

Para probar la integración con Telegram necesitas un `API ID` y un
`API HASH` propios, obtenidos desde [my.telegram.org](https://my.telegram.org/).
Nunca subas credenciales, archivos de sesión (`*.session`) ni bases de datos
(`*.sqlite3`) al repositorio.

Ejecutar la app en modo desarrollo (recarga automática del panel):

```powershell
wails dev
```

## Antes de abrir un pull request

Ejecuta lo siguiente y confirma que todo pasa:

```powershell
go vet ./...
go test ./...
npm --prefix dashboard run build
```

El CI del repositorio (`.github/workflows/ci.yml`) corre exactamente estos
mismos pasos en cada push y pull request a `main`; un PR que no los pase no
se podrá fusionar.

## Estilo y convenciones de código

- **Go**: sigue el formato estándar (`gofmt`); evita introducir
  dependencias nuevas sin justificarlas en la descripción del PR. Los
  errores de escritura a la base de datos u operaciones en segundo plano no
  deben descartarse en silencio (`_ = err`); regístralos con `log.Printf`
  salvo que exista una razón explícita para ignorarlos (coméntala en el
  código).
- **Vue/Frontend**: usa la Composition API con `<script setup>`. Si una
  pieza de estado y lógica es autocontenida y reutilizable, considera
  extraerla a un composable en `dashboard/src/composables/` en vez de
  seguir creciendo `App.vue`.
- **Comentarios**: el código existente comenta el *porqué* de las
  decisiones no evidentes (por ejemplo, por qué se limita el tamaño de un
  mapa, o por qué se pospone una actualización); sigue ese mismo criterio
  en vez de comentar lo obvio.
- **Idioma**: los mensajes de commit, comentarios y textos de la interfaz
  están en español, igual que el resto del proyecto.

## Estructura del proyecto

```text
TelegramDL/
├── main.go, app.go, sys_*.go   Entrada de la app y bindings de Wails
├── pkg/
│   ├── config/       Configuración y valores predeterminados
│   ├── downloader/   Motor de descargas: engine.go (ciclo de vida),
│   │                 queue.go (cola/planificación), progress.go
│   │                 (progreso/throttle), persistence.go (persistencia en
│   │                 BD), retry.go (reintentos), download.go (ejecución)
│   ├── listener/     Escucha de mensajes y filtros multimedia
│   ├── server/       API HTTP, WebSocket y snapshots de estado
│   ├── storage/      SQLite y migraciones
│   ├── telegram/     Cliente MTProto y autenticación
│   └── updater/      Comprobación e instalación de actualizaciones
└── dashboard/        Panel Vue 3 (src/views, src/components,
                       src/composables)
```

## Enviar cambios

1. Crea una rama descriptiva a partir de `main`.
2. Haz commits pequeños y enfocados; el mensaje debe explicar el porqué del
   cambio, no solo el qué.
3. Si el cambio toca comportamiento visible para el usuario (UI, API,
   formato de configuración), actualiza el `README.md` o `REMOTE_ACCESS.md`
   correspondiente en el mismo PR.
4. Abre el pull request contra `main` describiendo el problema que resuelve
   y cómo lo probaste.

## Reportar bugs y proponer funcionalidades

Abre un issue incluyendo, si aplica: sistema operativo, versión de
TelegramDL, pasos para reproducir el problema y los logs relevantes (nunca
incluyas tu `API_HASH`, teléfono o archivos de sesión en un reporte
público).

## Licencia

Al contribuir aceptas que tu aporte se distribuya bajo la misma licencia
del proyecto (ver [LICENSE](LICENSE)).
