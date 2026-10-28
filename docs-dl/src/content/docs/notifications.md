---
title: Sistema de Notificaciones
description: Guía completa sobre cómo funcionan las notificaciones en TelegramDL, incluyendo alertas por Bot de Telegram, avisos sonoros de la web y notificaciones del System Tray.
---

**TelegramDL** ofrece un sistema integral de notificaciones para mantenerte informado sobre el estado de tus descargas, la finalización de la cola de trabajo o cualquier eventualidad del sistema, tanto si estás delante del equipo como si trabajas en segundo plano o de forma remota.

Existen **tres canales independientes de notificación**:

1. **Notificaciones por Bot de Telegram** (Alertas remotas push a tu teléfono/Telegram).
2. **Alertas Sonoras del Panel Web** (Reproducción de audio en la interfaz gráfica).
3. **Notificaciones de la Bandeja del Sistema (System Tray)** (Avisos nativos de escritorio cuando la app está minimizada).

---

## 1. Notificaciones por Bot de Telegram (Alertas Remotas y Gestión de Descargas)

### ¿Qué hace?

- **Alertas del Sistema**: Envía mensajes instantáneos a tu cuenta de Telegram (o a un grupo/canal privado) cuando se completa una descarga, ocurre un fallo o se vacía la cola.
- **Añadir Descargas desde Telegram**: Puedes enviarle enlaces de Telegram (ej. `https://t.me/c/123456/789`) o **subir/reenviar archivos multimedia** (vídeos, audios, documentos, fotos) directamente al chat del bot para agregarlos automáticamente a la cola de descargas.
- **Modo de Renombrado de Archivos Recibidos**: En **Ajustes** puedes seleccionar el comportamiento ante archivos recibidos con pie de foto (*caption*):
  - **Automático**: Agrega la descarga directamente a la cola con el nombre detectado.
  - **Manual**: Si el archivo trae un pie de foto (*caption*), el bot te enviará un mensaje interactivo con ambos nombres (el nombre original y el caption) para que elijas cuál utilizar mediante botones interactivos antes de agregarlo a la cola.

### ¿Cómo se configura?

1. **Crear el Bot**: Habla con **`@BotFather`** en Telegram, envía `/newbot` y copia el **Token de acceso HTTP API**.
2. **Ingresar el Token**: Ve a **Ajustes** -> **Notificaciones por Telegram y Descarga**, activa la casilla y pega el Token.
3. **Vincular el Chat**: Inicia el chat con tu nuevo bot en Telegram y haz clic en **«Detectar chat»** en TelegramDL.
4. **Probar**: Haz clic en **«Enviar prueba»** para validar el envío.

### ¿Cómo se activa o desactiva?

- **Desde la Barra Lateral (Acceso Rápido)**: Haz clic en el botón **«Avisos del bot»** (icono de bot) en la barra lateral izquierda. Este botón permite alternar en un solo clic el envío de alertas sin necesidad de entrar a la pantalla de Ajustes.

---

## 2. Alertas Sonoras del Panel Web

### ¿Qué hace?

Cuando la interfaz gráfica de TelegramDL (o el panel web) se encuentra abierta y visible en pantalla, la aplicación reproduce una alerta sonora (`notification.wav`) cada vez que finalizan con éxito las tareas de la cola de descargas.

### ¿Cómo se activa o desactiva?

- **Desde la Barra Lateral**: Haz clic en el botón **«Sonido de la app»** (icono de altavoz/nota musical) ubicado en la barra lateral izquierda del panel.
  - **Activado** (estado armado): Se reproducirá el tono de aviso al finalizar la cola.
  - **Desactivado**: La interfaz permanecerá completamente en silencio.

---

## 3. Notificaciones y Minimizado en la Bandeja del Sistema (System Tray)

### ¿Qué hace?

En la aplicación de escritorio (Wails), al activar la casilla **«Minimizar al System Tray»** en **Ajustes**:

1. **Segundo Plano**: Al minimizar la ventana principal, la aplicación no ocupa espacio en la barra de tareas de Windows/escritorio, sino que se oculta en el área de notificación (junto al reloj) manteniendo el icono oficial de **Telegram DL**.
2. **Notificación al Minimizar**: Muestra una alerta nativa del sistema informando que la aplicación continúa ejecutándose en segundo plano.
3. **Aviso de Fin de Cola en Tray**: Si la cola de descargas finaliza mientras la aplicación está minimizada en el System Tray, TelegramDL envía una notificación nativa de escritorio con el título **Telegram DL** y el cuerpo *"Todas las descargas de la cola han finalizado con éxito."*.
4. **Sonido Integrado Nativo**: La notificación nativa del tray reproduce en segundo plano el mismo tono de audio (`notification.wav`) del panel web.
5. **Mecanismo Anti-Doble Sonido**: La aplicación registra la entrega de la notificación en el tray. Cuando restauras o abres de nuevo la ventana del programa, la interfaz reconoce que el aviso ya se emitió y **no vuelve a sonar**, evitando la molestia del sonido doble.

### ¿Cómo se activa o desactiva?

- **Desactivar el ocultado en el System Tray**:
  - En **Ajustes** -> **Configuración de Descarga**, desmarca la casilla **«Minimizar al System Tray»**.
  - Al minimizar la ventana, esta se comportará como cualquier otra ventana normal de la barra de tareas.

- **Desactivar la notificación sonora y de fin de cola en el System Tray**:
  - El botón **«Sonido de la app»** de la barra lateral izquierda controla tanto el sonido del panel web como las notificaciones de fin de cola en el System Tray.
  - Si desactivas el sonido con este botón, no se enviará la notificación del tray ni sonará el aviso al finalizar las descargas mientras la app esté minimizada.

---

## 4. Resumen de Controles de Notificación

| Canal de Notificación | Dónde se muestra | Sonido / Tono | Cómo activarlo o desactivarlo |
| :--- | :--- | :--- | :--- |
| **Bot de Telegram** | Mensajes en tu app de Telegram | Tono de Telegram | Botón **«Avisos del bot»** en barra lateral o casilla en **Ajustes** |
| **Sonido del Panel Web** | Interfaz del panel | Tono `notification.wav` | Botón **«Sonido de la app»** en barra lateral |
| **System Tray (Fin de Cola)** | Notificación de escritorio | Tono `notification.wav` (nativo) | Botón **«Sonido de la app»** en barra lateral |
| **Ocultar al Minimizar** | Área de reloj (System Tray) | Tono de sistema al ocultar | Casilla **«Minimizar al System Tray»** en **Ajustes** |
