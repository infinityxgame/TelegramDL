---
title: Acceso Remoto y Seguridad
description: Configuración segura de control remoto, gestión de tokens y acceso desde redes externas.
---

TelegramDL incorpora un servidor HTTP y WebSocket interno (por defecto en el puerto `8000`) que permite controlar el gestor tanto desde la propia aplicación de escritorio como desde el navegador web de cualquier teléfono móvil, tablet u otro ordenador.

---

## Seguridad: Autenticación por Token Bearer

Casi toda la API REST (`/api/*`) y el flujo WebSocket exigen un **Token de Acceso**, enviado en la cabecera `Authorization: Bearer <TOKEN>` (en WebSocket, como segundo subprotocolo: `Sec-WebSocket-Protocol: tgdl-v1, <TOKEN>`). Nadie en tu red local ni en internet puede consultar tus descargas, iniciar transferencias ni acceder a tus credenciales de Telegram sin este token.

Las únicas excepciones sin token: los públicos `/api/theme`, `/api/auth/has-credentials`, `/api/auth/token/send` (con sus propios límites de frecuencia) y `/api/ipc/download` (solo desde el propio equipo, con cabecera `X-TGDL-Request: 1` y `Content-Type: application/json`); y la primera configuración de Telegram (`/api/auth/status`, `credentials`, `send-code`, `verify-code`, `verify-2fa`, `qr/start`, `qr/status`, `qr/cancel` y `logout`), que responde sin token **solo mientras no exista sesión de Telegram guardada**, con método `POST` para las mutaciones (`status` y `qr/status` también admiten `GET`), cabecera `X-TGDL-Request: 1`, `Content-Type: application/json` y límite de 5/min por origen y 30/min global. Con sesión guardada responde `401`. El asistente permite elegir entre teléfono y código QR también en este primer acceso; consulta [Conexión y Autenticación](/TelegramDL/connection-auth/).

### Gestión del Token

- **Generación Automática**: Se genera aleatoriamente en el primer arranque y se almacena en SQLite.
- **Visualización**: Puedes verlo y copiarlo en cualquier momento desde **Ajustes ➔ Acceso Remoto**.
- **Regeneración Instantánea**: Si sospechas que tu token fue comprometido, pulsa **"Regenerar token"** en Ajustes. El token anterior quedará invalidado de inmediato.

---

## Botón «Enviármelo a Telegram» (Saved Messages)

Si intentas acceder a TelegramDL desde tu smartphone fuera del ordenador y no recuerdas el token:

1. Abre `http://<ip-servidor>:8000` en el navegador de tu móvil.
2. En la pantalla de bloqueo de acceso remoto, pulsa el botón **"Enviármelo a Telegram"**.
3. TelegramDL enviará un mensaje directo con el token a tus **Mensajes Guardados** en Telegram.
4. Entra a tu app oficial de Telegram, copia el token y pégalo en el navegador para desbloquear el panel.

:::tip
**Protección contra Spam**: El endpoint de envío del token a Telegram implementa un límite estricto de **1 envío por minuto** y registra la dirección IP solicitante para evitar abusos.
:::

---

## Acceso en Red Local (LAN / Wi-Fi)

En las instalaciones nuevas, TelegramDL enlaza por defecto a `127.0.0.1` (solo el propio equipo); las instalaciones existentes conservan la dirección que ya tuvieran. Para que cualquier dispositivo de tu red Wi-Fi doméstica acceda al panel, enlaza a `0.0.0.0`:

```env
TGDL_BIND_HOST=0.0.0.0
TGDL_PORT=8000
```

Para conectarte desde tu móvil o portátil:

1. Averigua la IP local de tu PC ejecutando `ipconfig` (Windows) o `ip a` / `ifconfig` (Linux/Mac) (ejemplo: `192.168.1.50`).
2. Abre en el navegador del móvil: `http://192.168.1.50:8000`.
3. Introduce el Token de acceso cuando te lo solicite.

:::warning
Si estás conectado a una red pública (universidad, oficina, cafetería), mantén o cambia `bind_host` (clave de la configuración, tabla `app_config`) —o la variable de entorno `TGDL_BIND_HOST`— a `127.0.0.1` para que el puerto solo responda a peticiones del propio equipo.
:::

---

## Acceso desde Fuera de Casa (Internet)

:::caution
**Nunca abras el puerto 8000 en tu router (port-forwarding directo)** sin cifrado ni cortafuegos, ya que los escáneres automáticos de internet lo atacarán en minutos.
:::

En su lugar, utiliza uno de los siguientes métodos seguros:

### Opción 1 (Recomendada): Tailscale (VPN de Malla)

[Tailscale](https://tailscale.com) crea una red privada segura punto a punto entre tus dispositivos sin abrir puertos en el router:

1. Instala Tailscale en el ordenador donde corre TelegramDL y en tu móvil.
2. Inicia sesión con la misma cuenta en ambos.
3. Copia la IP de Tailscale de tu PC (suele empezar por `100.x.y.z`).
4. Abre desde el navegador del móvil: `http://100.x.y.z:8000` e introduce tu token.

### Opción 2: Cloudflare Tunnel + Zero Trust Access

Si prefieres una URL pública sin instalar clientes VPN:

1. Instala `cloudflared` en tu servidor o PC.
2. Crea un túnel apuntando a `http://127.0.0.1:8000`.
3. Configura una regla de **Cloudflare Access** que obligue a iniciar sesión con tu correo electrónico mediante código de un solo uso antes de llegar a la aplicación.
