# Línea de Comandos (CLI) y Modos de Ejecución

Además de la interfaz gráfica de escritorio habitual, el ejecutable de **TelegramDL** puede iniciarse desde la terminal con diversos parámetros y modos de operación.

---

## :material-laptop: Modos de Ejecución

```bash
# Windows
TelegramDL.exe [opciones|URL]

# Linux / macOS
./TelegramDL [opciones|URL]
```

### Parámetros Disponibles:

| Parámetro | Alias | Descripción |
| :--- | :--- | :--- |
| *(sin parámetros)* | — | Inicia la aplicación completa en modo escritorio con interfaz Wails. |
| `<URL>` | — | **Añadir Descarga Directa**: Pasa una URL de Telegram directamente. Si la app está abierta, se añade a la cola activa; si está cerrada, se abre e inicia la descarga. |
| `--download <URL>` | `-d` | **Modo Descarga CLI Pura**: Ejecuta la descarga en la propia consola en modo texto sin abrir la interfaz gráfica. |
| `--server` | `-s`, `/server` | **Modo Servidor Headless**: Inicia el servidor REST y WebSocket sin abrir ninguna ventana gráfica. |
| **`--ws`** | `-ws`, `--use-ws` | **Bypass de Cortafuegos (WebSocket)**: Fuerza la conexión MTProto con Telegram mediante transporte WebSocket sobre HTTPS/WSS (puerto 443). |
| **`--no-ipv6`** | `-no-ipv6`, `--disable-ipv6` | **Desactivar IPv6**: Desactiva las conexiones IPv6 a los servidores de Telegram, forzando tráfico IPv4 únicamente. |
| `--update` | `-u`, `/update` | Comprueba si existe una versión más reciente en GitHub y la instala automáticamente. |
| `--help` | `-h`, `/?` | Muestra el mensaje de ayuda con la sintaxis de uso en consola. |

---

## :material-link: Descarga Directa por Argumento (`<URL>`)

Puedes pasar un enlace de Telegram directamente detrás del ejecutable para encolar o iniciar una descarga automáticamente:

```bash
# Ejemplo con canal público o usuario
TelegramDL.exe https://t.me/nombre_canal/123

# Ejemplo con canal privado y rango de mensajes
TelegramDL.exe https://t.me/c/1794593738/30502-30505
```

### Comportamiento Inteligente:

- **Si la aplicación YA está abierta**: El proceso en consola detecta la instancia en ejecución mediante el sistema de bloqueo IPC y le transmite el enlace de forma transparente a través del puerto local (`/api/ipc/download`). La interfaz gráfica abierta reflejará la nueva descarga en tiempo real en la cola. Si no hay nada en ejecución, empezará de inmediato; si ya hay tareas activas, se mantendrá ordenada en la cola.
- **Si la aplicación está CERRADA**: El proceso abre TelegramDL en modo escritorio, inicializa la interfaz y encola la descarga en segundo plano. Si la cola estaba vacía, la descarga se procesa al momento de arrancar.

---

## :material-download: Modo Descarga por Consola Pura (`--download <URL>`)

Si prefieres realizar una descarga puntual sin abrir la ventana nativa ni mantener un servidor Web/WebSocket en segundo plano, utiliza la opción `--download`:

```bash
TelegramDL.exe --download https://t.me/nombre_canal/123
```

En este modo:
1. Se conecta directamente a la API MTProto de Telegram utilizando tus credenciales guardadas en la base de datos local.
2. Muestra el progreso de la descarga en texto directamente en la terminal.
3. Al finalizar la descarga, se cierra automáticamente devolviendo el control a la consola.

---

## :material-server: Modo Servidor Headless (`--server`)

El modo servidor es ideal para:
- Servidores domésticos o NAS (Synology, TrueNAS, etc.).
- Máquinas virtuales Linux o servidores sin entorno gráfico (headless).
- Despliegues en segundo plano que se controlan exclusivamente desde el teléfono móvil o navegador web mediante [Acceso Remoto](remote-access.md).

```bash
TelegramDL.exe --server
```

Al iniciarse en este modo:
1. Adquiere el bloqueo de instancia única para evitar colisiones.
2. Inicia la conexión MTProto con Telegram.
3. Arranca el servidor HTTP y WebSocket en la IP y puerto configurados (por defecto `127.0.0.1:8000` en instalaciones nuevas; las existentes conservan su dirección guardada).
4. Permanece activo escuchando descargas y eventos del Listener hasta recibir una señal de terminación (`Ctrl+C` / `SIGINT`).

---

## :material-sync: Actualización Automática (`--update`)

Puedes forzar una búsqueda y actualización automática de versión directamente desde la línea de comandos:

```bash
TelegramDL.exe --update
```

El actualizador integrado (`pkg/updater/updater.go`):
1. Consulta la API pública de releases en GitHub de TelegramDL.
2. Compara la versión actual con la última versión publicada.
3. Descarga el binario optimizado correspondiente a tu sistema operativo y arquitectura.
4. Sustituye el ejecutable actual de forma atómica y segura.

---

## :material-lock: Bloqueo de Instancia Única y Comunicación IPC

TelegramDL implementa un mecanismo de bloqueo inteligente y comunicación entre procesos (IPC) para evitar duplicidad de instancias y gestionar el reenvío de tareas:

- Si intentas abrir una segunda instancia sin argumentos, el sistema detecta que la aplicación ya está abierta y finaliza ordenadamente.
- Si pasas una URL como argumento mientras la app está abierta, la segunda instancia no se abre duplicada: se limita a enviar la descarga a la instancia activa vía IPC HTTP local y termina su ejecución notificando el éxito en consola.
- Al actualizarse, el nuevo proceso espera hasta 8 segundos para que el proceso anterior libere el bloqueo antes de tomar el relevo.

---

## :material-shield-network: Bypass de Cortafuegos y Opciones de Red (WebSocket / IPv6)

En entornos de red restrictivos (empresas, universidades o redes corporativas), los cortafuegos con inspección profunda de paquetes (DPI) suelen bloquear las conexiones binarias MTProto estándar. Para solucionar esto, TelegramDL incluye soporte nativo para transporte WebSocket y desactivación de IPv6:

### Opciones por Consola:

```bash
# Forzar conexión por WebSocket (puerto 443 HTTPS/WSS)
TelegramDL.exe --ws

# Desactivar conexiones IPv6 (usar solo IPv4)
TelegramDL.exe --no-ipv6

# Combinar ambos flags
TelegramDL.exe --ws --no-ipv6
```

### Variables de Entorno y `.env`:

```env
# Activar transporte WebSocket para saltar cortafuegos DPI
TGDL_USE_WS=1

# Desactivar IPv6 para evitar timeouts en redes con IPv6 mal configurado
TGDL_NO_IPV6=1
```

### Fallback Automático:

Si ejecutas TelegramDL sin ningún flag especial en una red corporativa que bloquee MTProto plano:
1. El programa intentará primero la conexión MTProto estándar.
2. Si la conexión falla o sufre un *timeout* por bloqueo DPI, TelegramDL captura el error automáticamente.
3. Se reintenta de forma transparente la conexión mediante transporte WebSocket (`dcs.Websocket`), permitiendo iniciar sesión y descargar sin intervención manual ni tener que volver a loguearse.

