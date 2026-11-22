---
title: Primeros Pasos e Instalación
description: Guía de configuración inicial y requisitos del sistema para TelegramDL.
---

Esta guía te guiará en la configuración inicial, los requisitos del sistema y los métodos para ejecutar y compilar **TelegramDL**.

---

## Requisitos del Sistema

TelegramDL está optimizado para funcionar en múltiples plataformas:

- **Sistemas Operativos**: Windows 10/11, macOS 11+, Linux (Ubuntu, Debian, Fedora, Arch).
- **Go**: Versión 1.22 o superior (para compilar desde código fuente).
- **Node.js**: Versión 18 o superior y `npm` (para compilar el panel web).
- **Wails v2 CLI**: Necesario para el empaquetado de escritorio.
- **WebView**: Requisitos según el sistema operativo:
    - **macOS**: Ya incluye su propio WebView nativo.
    - **Windows**: Requiere instalar [Microsoft Edge WebView2 Runtime](https://developer.microsoft.com/en-us/microsoft-edge/webview2).
    - **Linux**: Requiere WebKitGTK, QT WebEngine o WPE WebKit.

#### Comando para instalar Wails:

En caso de que desees compilar la aplicación

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@latest
```

---

## Obtención de Credenciales de Telegram

Para conectarte a la red MTProto de Telegram necesitas un **API ID** y un **API HASH** personales:

1. Inicia sesión en [my.telegram.org](https://my.telegram.org/) con tu número de teléfono.
2. Ve a la sección **API development tools**.
3. Completa el formulario de registro de aplicación (puedes poner cualquier nombre y título).
4. Copia los valores generados: `App api_id` y `App api_hash`.

:::caution[IMPORTANTE]
Nunca compartas tu `API ID`, `API HASH`, ni los archivos de sesión generados (`tg_session.jsontg_session.json`) con terceros por seguridad propia.
:::

---

## Configuración Inicial

### Asistente gráfico en el primer inicio.

Al abrir TelegramDL por primera vez ya sea por la aplicación nativa o por el navegador mediante el modo `--server` la interfaz mostrará un asistente interactivo que te solicitará:
1. `API ID` y `API HASH`.
2. Número de teléfono internacional (ejemplo: `+34600000000`).
3. Código de verificación (enviado por la app oficial de Telegram o SMS).
4. Contraseña de verificación en dos pasos (2FA), en caso de tenerla activada.

:::tip[Acceso mediante Proxy]
Si te encuentras en un país o red con restricciones de acceso a Telegram, la pantalla del asistente inicial incluye un botón rápido de **Proxy** para probar y activar tu conexión antes o durante la autenticación.
:::

Al terminar puede comenzar a usar el programa

---

## Ubicación de Datos y Almacenamiento

TelegramDL guarda la configuración, sesiones y base de datos en una ruta dedicada en tu directorio de usuario:

- **Windows**: `%USERPROFILE%\.tgdown\tgdown.sqlite3`
- **Linux / macOS**: `~/.tgdown/tgdown.sqlite3`

Las descargas se guardan por defecto en la carpeta del sistema `Descargas/TelegramDL` o en la ruta que configures desde la pestaña de **Ajustes**.

---

## Desarrollo y Compilación

### 1. Instalar dependencias del panel (Vue 3)

```bash
npm --prefix dashboard install
```

### 2. Ejecución en Modo Desarrollo (con Live Reload)

```bash
wails dev
```

Este comando inicia el backend en Go y el servidor de desarrollo de Vite en paralelo, abriendo la ventana de la aplicación y actualizando los cambios al guardar.

### 3. Compilar el Panel Web

```bash
npm --prefix dashboard run build
```

:::note
La carpeta `dashboard/dist/` es generada por Vite y debe existir antes de compilar Go o ejecutar `go vet`, ya que `main.go` utiliza la directiva `//go:embed all:dashboard/dist`.
:::

### 4. Compilar el Ejecutable de Escritorio

```bash
wails build
```

El binario ejecutable final se genera dentro de `build/bin/` (`TelegramDL.exe` en Windows, o binario ejecutable en macOS/Linux).
