---
title: Connection & Authentication
description: How TelegramDL synchronizes the clock over NTP, offers phone or QR sign-in, and keeps the session stable.
---

**TelegramDL** connects to Telegram over the MTProto protocol (`gotd/td`). This page explains the connection lifecycle, automatic clock synchronization, both sign-in methods, and the safeguards that prevent a network drop or reconnect from forcing you to authenticate again.

---

## Connection Lifecycle

When you save your credentials (`API ID` and `API HASH`), a supervised MTProto client starts in the background:

1. A direct connection to Telegram servers is attempted first.
2. If the first attempt never becomes ready (DPI filtering, firewall, or timeout), TelegramDL automatically retries over **WebSocket** on port 443.
3. Retries use a short delay and a new client generation; in-flight requests always use the current client.
4. Before downloading, listing chats, or authenticating, the backend waits for the client to be ready (`WaitReady`).

If a proxy is enabled and fails, TelegramDL notifies the dashboard and continues with a direct or WebSocket connection. You can force the transport or disable IPv6 from the command line:

```bash
TelegramDL.exe --ws          # Force WebSocket
TelegramDL.exe --no-ipv6     # Force IPv4
```

See [CLI Commands](/TelegramDL/en/cli/) for the remaining network options.

---

## NTP Clock Synchronization

### Why it matters

MTProto signs every request with a message identifier derived from the current time. If the computer clock is offset from Telegram's, the server rejects requests, the connection drops, or sign-in fails with vague session errors. Verification codes and QR code expiry are also based on server timestamps.

### How TelegramDL handles it

When the first MTProto client of each run is created, TelegramDL queries public NTP servers:

| Order | Server | Maximum time per query |
| :--- | :--- | :--- |
| 1 | `pool.ntp.org` | 2 s |
| 2 | `time.google.com` | 2 s |
| 3 | `time.cloudflare.com` | 2 s |

- The response is validated and the offset (`ClockOffset`) from the system clock is kept in memory.
- Creating the first MTProto client **waits** for the synchronization attempt to finish, so the message ID generator never starts with an incorrect time.
- Every Telegram operation uses the corrected clock, including QR code expiry checks.
- If no server responds, the process **does not block**: it continues with the system clock and logs a warning. Authentication then only works if the local time is already correct.

:::note
Synchronization runs once at startup and does not change the Windows, Linux, or macOS clock. The offset is applied only inside TelegramDL. Startup can take a few seconds while the NTP attempts time out.
:::

### Recommendations

- Keep the system clock set automatically (Internet time server).
- On Windows: **Settings → Time & language → Date & time → Set time automatically**.
- On Linux: `timedatectl set-ntp true`.
- If you use a firewall, allow outbound NTP (**UDP 123**) or rely on the operating system's synchronization.
- On virtual machines, enable the hypervisor's time synchronization.
- If you fix the clock while the app is running, restart TelegramDL so it queries NTP again.

Typical log messages:

| Log entry | Meaning |
| :--- | :--- |
| `NTP clock synchronized with ...` | Synchronization succeeded; the applied offset is shown. |
| `Could not query NTP servers...` | No NTP server responded; check date, time, and UDP 123 connectivity. |
| `Local clock needs synchronization` | Telegram detected an offset; fix the system clock and restart the app. |

---

## Sign-in Methods

The sign-in method step provides two tabs: **Phone** and **QR code**. Only one QR flow runs at a time: switching to **Phone** cancels the current QR flow. If you move from **Phone** to **QR code**, the code you already requested stays valid until it expires or you use it.

The sign-in screen also includes a **Change theme** button to pick the dashboard theme before authenticating; the selection is applied and saved instantly.

### Phone number sign-in

1. Enter the number in international format, for example `+12025550123`.
2. Press **Send Code**.
3. Enter the received code. Telegram usually delivers it first to an open session of the official app; SMS is only an alternative method.
4. If Telegram offered another delivery channel, the **Didn't arrive? Try another delivery method** button appears. The alternative request is never sent automatically: only when the user asks for it and Telegram has offered it.
5. If the account has two-step verification enabled, enter the 2FA password.

### QR code sign-in

1. Select the **QR code** tab.
2. In Telegram, open **Settings → Devices → Link Desktop Device** and scan the image.
3. The code renews itself when it expires, as long as the sign-in screen stays open.
4. If the account has 2FA enabled, the wizard moves to the password step after scanning.
5. Once Telegram confirms, the session is saved and the dashboard opens.

States returned by the QR flow:

| State | Meaning |
| :--- | :--- |
| `STARTING` | The first code is being prepared. |
| `WAITING_SCAN` | A code is visible and waiting to be scanned. |
| `WAITING_2FA` | The code was accepted; the two-step password is still required. |
| `LOGGED_IN` | Authorization completed. |
| `EXPIRED` | The code is no longer valid and the screen was idle for too long. |
| `CANCELLED` | The flow was cancelled by switching methods, going back, or closing the wizard. |
| `ERROR` | Telegram rejected the flow; the message is shown on screen. |

:::tip[QR code security]
The QR token is never sent to the browser. The backend renders the PNG image and exposes only the image and its expiry time. The code is single-use and expires within a few minutes.
:::

### Two-step verification (2FA)

The 2FA step is shared by both methods. The password is Telegram's **cloud password**, not the code received by SMS or in the app.

- If you go back from the 2FA step, TelegramDL closes the half-finished attempt and returns to the phone step, because Telegram already consumed the previous code. You must request a new one.
- If you access the dashboard from another computer over plain HTTP, the wizard warns that the password will travel in clear text. Use HTTPS or the local computer to protect it.

---

## Persistent Session and Recovery

TelegramDL stores the MTProto session in `~/.tgdown/tg_session.json` (on Windows, `%USERPROFILE%\.tgdown\tg_session.json`) and records in SQLite that the session was authorized, along with the user's public data. That evidence distinguishes:

- a valid session going through a temporary network outage, from
- a computer that really needs to authenticate again.

Main states returned by `GET /api/auth/status`:

| State | Meaning |
| :--- | :--- |
| `UNCONFIGURED` | No Telegram credentials are saved. |
| `NEED_PHONE` | Credentials exist, but there is no authorized session. |
| `CONNECTING` | A saved session exists and the connection is being re-established. |
| `WAITING_CODE` | A code was sent and is waiting to be verified. |
| `WAITING_2FA` | The code was accepted and the 2FA password is pending. |
| `LOGGED_IN` | Authorized and operational session. |

### Why sign-in no longer resets itself

- The `WAITING_CODE` or `WAITING_2FA` stage survives client reconnects. The sign-in screen does not jump back to the start because of a transient network failure.
- `POST /api/auth/send-code` returns the state (`WAITING_CODE`), the pending phone number, and whether credentials are still configured, so the wizard keeps the correct step.
- Before sending the code, verifying the code, or validating 2FA, the backend waits for the client to be ready and re-reads the active client. A reconnect or the switch to WebSocket therefore never leaves the request pointing at a stale client.
- Authentication operations are serialized: two code requests, or a QR flow and a phone flow, cannot overlap.
- The chat and dialog list is only requested after the session is authorized. It previously competed with `auth.sendCode` and could break the first sign-in.
- If the network fails but the session evidence matches the stored file, the state is reported as `CONNECTING` with the known user, without asking for credentials again.

:::caution[Do not share your session]
The `tg_session.json` file and the `tgdown.sqlite3` database grant access to your account. Do not copy them to other computers or send them to third parties. When you log out from TelegramDL, the local file is deleted and the authorization evidence is cleared.
:::

---

## Troubleshooting

| Symptom | Likely cause | Solution |
| :--- | :--- | :--- |
| The log says `Could not query NTP servers` | No outbound UDP 123 or no Internet | Allow NTP, enable automatic time, and restart TelegramDL. |
| Sign-in fails after connecting, with clock warnings | Offset larger than Telegram tolerates | Synchronize the system clock and restart the app. |
| The QR code expires almost immediately | Local clock is ahead and NTP did not respond | Fix the time; TelegramDL compares expiry against the NTP-corrected clock. |
| The code never arrives by SMS | Telegram delivered it to the official app first | Check the Telegram chat on your devices; use the alternative method button if it appears. |
| `verification code expired` or `invalid verification code` | Old or mistyped code | Go back to the phone step and request a new code. |
| `too many attempts, please wait` (`FLOOD_WAIT`) | Too many attempts in a row | Wait for the time indicated by Telegram before retrying. |
| `invalid 2FA password` | Wrong cloud password | Check Telegram's two-step verification password. |
| `AUTH_KEY_UNREGISTERED` or `SESSION_REVOKED` | The session was closed from another device | Sign in again with phone or QR. |
| `telegram connection is not ready` when opening QR | The client is not connected yet | Check the network, proxy, or try `--ws`; the QR flow can be retried. |
| `telegram session is already authorized` | A session is already active | Open the dashboard; no new sign-in is needed. |

Authentication diagnostic logs (`[TG AUTH]` and `[TG AUTH DIAG]`) never include the phone number, code, password, or code hash. You can safely share those lines with support.
