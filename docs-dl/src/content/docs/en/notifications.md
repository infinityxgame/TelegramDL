---
title: Notification System
description: Complete guide on how notifications work in TelegramDL, including Telegram Bot alerts, web audio chimes, and System Tray notifications.
---

**TelegramDL** provides a comprehensive notification system to keep you informed about download status, queue completion, or system events, whether you are at your PC, working in the background, or managing downloads remotely.

There are **three independent notification channels**:

1. **Telegram Bot Notifications** (Remote push alerts sent to your phone/Telegram).
2. **Web Panel Audio Chimes** (Audio playback inside the graphical interface).
3. **System Tray Notifications** (Native desktop alerts when the app is minimized to tray).

---

## 1. Telegram Bot Notifications (Remote Alerts & Download Management)

### What it does

- **System Alerts**: Sends instant messages to your Telegram account (or a private group/channel) when a download completes, a failure occurs, or the queue empties.
- **Add Downloads from Telegram**: You can send Telegram links (e.g. `https://t.me/c/123456/789`) or **upload/forward media files** (videos, audio, documents, photos) directly to the bot chat to add them to the download queue automatically.
- **Received Files Naming Mode**: In **Settings**, you can choose how files received with captions are handled:
  - **Automatic**: Adds the file directly to the queue with the detected filename.
  - **Manual**: If the file includes a caption, the bot sends an interactive message showing both names (original filename and caption) with interactive buttons so you can choose which name to use before adding it to the queue.

### How to configure

1. **Create the Bot**: Open Telegram, search for **`@BotFather`**, send `/newbot`, and copy the **HTTP API Access Token**.
2. **Enter Token**: Go to **Settings** -> **Telegram & Download Notifications**, check the box, and paste the Token.
3. **Link Chat**: Start a chat with your new bot in Telegram and click **"Detect chat"** in TelegramDL.
4. **Test**: Click **"Send test"** to verify delivery.

### How to enable or disable

- **From Sidebar (Quick Access)**: Click the **«Bot alerts»** button (bot icon) in the left sidebar to toggle alerts with a single click without opening Settings.

---

## 2. Web Panel Audio Chimes

### What it does

When the TelegramDL desktop interface (or web panel) is open and visible on screen, the application plays an audio chime (`notification.wav`) whenever tasks in the download queue complete successfully.

### How to enable or disable

- **From Sidebar**: Click the **«App sound»** button (speaker/music note icon) in the left sidebar.
  - **Enabled** (armed state): Plays the audio chime when the queue finishes.
  - **Disabled**: The interface stays completely silent.

---

## 3. System Tray Notifications & Minimizing

### What it does

In the desktop application (Wails), when checking **"Minimize to System Tray"** in **Settings**:

1. **Background Running**: When minimizing the main window, the app does not occupy taskbar space; instead, it hides in the system notification area (near the clock) showing the official **Telegram DL** icon.
2. **Minimize Notification**: Displays a native system toast informing you that TelegramDL continues running in the background.
3. **Queue Completion Tray Alert**: If the download queue completes while minimized to the tray, TelegramDL sends a native desktop notification titled **Telegram DL** with the message *"All downloads in the queue have finished successfully."*.
4. **Native Custom Audio**: The tray notification plays the exact same audio chime (`notification.wav`) from the web panel natively in the background.
5. **Anti-Double Sound**: The app tracks tray notification delivery. When you restore or open the window later, the interface recognizes the alert was already delivered and **will not play sound again**, avoiding duplicate audio.

### How to enable or disable

- **Disable hiding to System Tray**:
  - In **Settings** -> **Download Configuration**, uncheck **"Minimize to System Tray"**.
  - When minimized, the window behaves like a standard taskbar window.

- **Disable tray completion notification and sound**:
  - The **«App sound»** button in the left sidebar controls both the web panel chime and the System Tray queue completion notification.
  - If you disable sound with this button, no tray completion notification or audio chime will trigger while minimized.

---

## 4. Notification Controls Summary

| Notification Channel | Where displayed | Audio / Chime | How to enable or disable |
| :--- | :--- | :--- | :--- |
| **Telegram Bot** | Telegram app messages | Telegram tone | **«Bot alerts»** button in sidebar or checkbox in **Settings** |
| **Web Panel Sound** | Panel interface | `notification.wav` chime | **«App sound»** button in sidebar |
| **System Tray Alert** | Desktop notification | `notification.wav` (native) | **«App sound»** button in sidebar |
| **Hide on Minimize** | Tray area (clock) | System chime on hide | **"Minimize to System Tray"** in **Settings** |
