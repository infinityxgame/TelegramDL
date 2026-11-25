---
title: Remote Access & Security
description: Secure remote control configuration, token management, and external network access.
---

TelegramDL features an integrated HTTP and WebSocket server (defaulting to port `8000`), allowing you to control your downloads both from the native desktop GUI and from web browsers on smartphones, tablets, or remote computers.

---

## Security: Bearer Token Authentication

Almost every REST endpoint (`/api/*`) and the WebSocket flow require an **Access Token**, sent in the `Authorization: Bearer <TOKEN>` header (over WebSocket it travels as the second subprotocol: `Sec-WebSocket-Protocol: tgdl-v1, <TOKEN>`). No unauthorized party on your local network or the internet can view downloads, trigger transfers, or inspect your credentials without this token.

The only exceptions without a token: the public `/api/theme`, `/api/auth/has-credentials`, `/api/auth/token/send` (each with its own rate limits) and `/api/ipc/download` (local machine only, requiring the `X-TGDL-Request: 1` and `Content-Type: application/json` headers); plus the first-time Telegram setup (`/api/auth/status`, `credentials`, `send-code`, `verify-code`, `verify-2fa`, `qr/start`, `qr/status`, `qr/cancel`, and `logout`), which responds without a token **only while no Telegram session is stored**, with `POST` for mutations (`status` and `qr/status` also accept `GET`), the `X-TGDL-Request: 1` header, `Content-Type: application/json`, and a limit of 5/min per origin and 30/min globally. With a stored session it responds `401`. The wizard lets you choose between phone and QR code during this first access as well; see [Connection & Authentication](/TelegramDL/en/connection-auth/).

### Managing the Token

- **Automatic Generation**: Randomly created during initial setup and stored securely in SQLite.
- **Viewing the Token**: Inspect or copy it anytime under **Settings ➔ Remote Access**.
- **Instant Revocation**: If your token was compromised, click **"Regenerate token"**. The previous token is immediately invalidated.

---

## "Send to Telegram" (Saved Messages Delivery)

If you are away from your PC and opening the web dashboard from your smartphone:

1. Open `http://<server-ip>:8000` in your mobile browser.
2. On the remote access login prompt, tap **"Send it to my Telegram"**.
3. TelegramDL dispatches a direct private message containing the token to your **Saved Messages** in Telegram.
4. Switch to your official Telegram app, copy the token, and paste it into your browser to unlock full control.

:::tip
**Anti-Spam Throttling**: The token delivery endpoint enforces a strict rate limit of **1 request per minute** and logs the requester IP address.
:::

---

## Local Network Access (LAN / Wi-Fi)

On new installations, TelegramDL binds to `127.0.0.1` by default (local machine only); existing installations keep whatever address they already have. To let any device on your home Wi-Fi access the panel, bind to `0.0.0.0`:

```env
TGDL_BIND_HOST=0.0.0.0
TGDL_PORT=8000
```

To connect from your phone or secondary computer:

1. Find your machine's local IP via `ipconfig` (Windows) or `ip a` / `ifconfig` (Linux/macOS) (e.g. `192.168.1.50`).
2. Open in your phone browser: `http://192.168.1.50:8000`.
3. Provide your Access Token when prompted.

:::warning
If you are connected to a public network (university, office, coffee shop), keep or set `bind_host` (a configuration key in the `app_config` table) — or the `TGDL_BIND_HOST` environment variable — to `127.0.0.1` so that the port only responds to requests from the local machine.
:::

---

## Remote Access over the Internet

:::caution
**Never expose port 8000 directly via router port-forwarding** without an encrypted tunnel or firewall, as automated internet scanners will probe it within minutes.
:::

Instead, use one of these secure architectures:

### Option 1 (Recommended): Tailscale (Mesh VPN)

[Tailscale](https://tailscale.com) forms an encrypted point-to-point mesh network between your devices without port forwarding:

1. Install Tailscale on the host PC and your mobile phone.
2. Sign in using the same account on both devices.
3. Locate your host PC's Tailscale IP (typically starts with `100.x.y.z`).
4. In your phone's browser, visit `http://100.x.y.z:8000` and enter your token.

### Option 2: Cloudflare Tunnel + Zero Trust Access

For a public URL without client software:

1. Install `cloudflared` on your PC or server.
2. Configure a tunnel targeting `http://127.0.0.1:8000`.
3. Enable **Cloudflare Access** on the tunnel, requiring one-time passcode email login prior to accessing the service.
