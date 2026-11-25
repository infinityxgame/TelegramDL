---
title: Getting Started & Installation
description: System requirements and initial setup guide for TelegramDL.
---

This guide walks you through the system requirements, initial setup, and methods to run and compile **TelegramDL**.

---

## System Requirements

TelegramDL is optimized for cross-platform desktop environments:

- **Operating Systems**: Windows 10/11, macOS 11+, Linux (Ubuntu, Debian, Fedora, Arch).
- **Go**: Version 1.22 or higher (to build from source).
- **Node.js**: Version 18 or higher with `npm` (to build the web dashboard).
- **Wails v2 CLI**: Required for desktop compilation and packaging.
- **WebView**: Requirements by operating system:
    - **macOS**: It already includes its own native WebView..
    - **Windows**: Requires installation [Microsoft Edge WebView2 Runtime](https://developer.microsoft.com/en-us/microsoft-edge/webview2).
    - **Linux**: Requires WebKitGTK, QT WebEngine o WPE WebKit.

#### Command to install Wails :

If you wish to compile the application

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@latest
```

---

## Obtaining Telegram Credentials

To connect to the Telegram MTProto network, you need personal **API ID** and **API HASH** keys:

1. Sign in to [my.telegram.org](https://my.telegram.org/) with your phone number.
2. Navigate to **API development tools**.
3. Fill out the application registration form (any app name or short title is valid).
4. Copy the generated values: `App api_id` and `App api_hash`.

:::caution[IMPORTANT]
Never share your `API ID`, `API HASH`, or the generated session file (`~/.tgdown/tg_session.json`) with third parties for your own safety.
:::

---

## Initial Configuration

### Graphical wizard on first startup
When opening TelegramDL for the first time—whether via the native application or the browser using `--server` mode—the interface displays an interactive wizard that asks you to:
1. `API ID` and `API HASH`.
2. The sign-in method: **Phone** or **QR code**.
3. If you choose phone, the international number (e.g. `+12025550123`) and the verification code. If you choose QR, the wizard shows a code to scan from **Telegram → Settings → Devices → Link Desktop Device**.
4. Two-Factor Authentication (2FA) password, if enabled on your account.

:::tip[Proxy Connection]
If you are connecting from a country or network with Telegram restrictions, the startup wizard includes a quick **Proxy** button to test and enable your connection before or during authentication.
:::

:::note[Synchronized clock]
TelegramDL queries NTP servers at startup and corrects the clock offset internally. No configuration is required, but keeping the system clock on automatic time is recommended. See [Connection & Authentication](/TelegramDL/en/connection-auth/) for details and troubleshooting.
:::

Once finished, you can start using the program.

---

## Data Directory & Storage

TelegramDL stores all application state, sessions, and SQLite databases in your user directory:

- **Windows**: `%USERPROFILE%\.tgdown\tgdown.sqlite3`
- **Linux / macOS**: `~/.tgdown/tgdown.sqlite3`

Downloads default to your system's `Downloads/TelegramDL` folder, which can be modified at any time in **Settings**.

---

## Development & Building

### 1. Install Dashboard Dependencies (Vue 3)

```bash
npm --prefix dashboard install
```

### 2. Run in Development Mode (Live Reload)

```bash
wails dev
```

This runs the Go backend and Vite dev server concurrently, opening the desktop window with hot module replacement.

### 3. Build Web Dashboard

```bash
npm --prefix dashboard run build
```

:::note
The `dashboard/dist/` folder is produced by Vite and must exist prior to compiling Go or running `go vet`, as `main.go` embeds it via `//go:embed all:dashboard/dist`.
:::

### 4. Build Desktop Binary

```bash
wails build
```

The compiled binary will be placed inside `build/bin/` (`TelegramDL.exe` on Windows, or the desktop executable on macOS/Linux).
