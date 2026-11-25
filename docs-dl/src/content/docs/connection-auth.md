---
title: Conexión y Autenticación
description: Cómo TelegramDL sincroniza el reloj por NTP, ofrece acceso por teléfono o código QR y mantiene la sesión estable.
---

**TelegramDL** se conecta a Telegram mediante el protocolo MTProto (`gotd/td`). Esta página explica el ciclo de conexión, la sincronización automática del reloj, los dos métodos de inicio de sesión disponibles y las medidas que evitan que un corte de red o una reconexión obliguen a autenticarse de nuevo.

---

## Ciclo de conexión

Al guardar las credenciales (`API ID` y `API HASH`) se crea un cliente MTProto supervisado que se ejecuta en segundo plano:

1. Se intenta primero la conexión directa a los servidores de Telegram.
2. Si el primer intento no llega a estar listo (bloqueo DPI, cortafuegos o *timeout*), se reintenta automáticamente mediante **WebSocket** sobre el puerto 443.
3. Cada reintento se hace con un pequeño retardo y sobre una nueva generación de cliente; las peticiones en curso siempre usan el cliente vigente.
4. Antes de descargar, listar chats o autenticar, el backend espera a que el cliente esté listo (`WaitReady`).

Si el proxy está activo y falla, TelegramDL avisa al panel y continúa con la conexión directa o por WebSocket. Puedes forzar el transporte o desactivar IPv6 desde la consola:

```bash
TelegramDL.exe --ws          # Fuerza WebSocket
TelegramDL.exe --no-ipv6     # Fuerza IPv4
```

Consulta [Comandos CLI](/TelegramDL/cli/) para el resto de parámetros de red.

---

## Sincronización de la hora por NTP

### Por qué es necesaria

MTProto firma cada petición con un identificador de mensaje derivado de la hora. Si el reloj del equipo está desfasado respecto a Telegram, el servidor rechaza las peticiones, la conexión se cae o el inicio de sesión falla con errores de sesión poco descriptivos. Los códigos de verificación y la caducidad de los códigos QR también se calculan con marcas de tiempo del servidor.

### Cómo funciona en TelegramDL

Al crear el primer cliente MTProto de cada ejecución, TelegramDL consulta servidores NTP públicos:

| Orden | Servidor | Tiempo máximo por consulta |
| :--- | :--- | :--- |
| 1 | `pool.ntp.org` | 2 s |
| 2 | `time.google.com` | 2 s |
| 3 | `time.cloudflare.com` | 2 s |

- Se valida la respuesta y se guarda en memoria el desfase (`ClockOffset`) respecto al reloj del sistema.
- La creación del primer cliente MTProto **espera** a que termine el intento de sincronización. Así el generador de identificadores de mensaje no arranca con una hora incorrecta.
- Todas las operaciones de Telegram usan el reloj corregido, incluida la comprobación de caducidad de los códigos QR.
- Si ningún servidor responde, el proceso **no se bloquea**: continúa con el reloj del sistema y deja un aviso en el registro. En ese caso la autenticación solo funcionará si la hora local ya es correcta.

:::note
La sincronización se hace una vez al arrancar y no modifica la hora de Windows, Linux o macOS. El desfase se aplica solo dentro de TelegramDL. El inicio puede tardar unos segundos mientras se agotan los intentos NTP.
:::

### Recomendaciones

- Mantén la hora del sistema en modo automático (servidor de hora de Internet).
- En Windows: **Configuración → Hora e idioma → Fecha y hora → Establecer hora automáticamente**.
- En Linux: `timedatectl set-ntp true`.
- Si usas un cortafuegos, permite NTP saliente (**UDP 123**) o confía en la sincronización del sistema operativo.
- En máquinas virtuales, activa la sincronización de hora del hipervisor.
- Si corriges la hora con la aplicación abierta, reinicia TelegramDL para que vuelva a consultar NTP.

Mensajes habituales en el registro:

| Registro | Significado |
| :--- | :--- |
| `NTP clock synchronized with ...` | Sincronización correcta; se muestra el desfase aplicado. |
| `Could not query NTP servers...` | Ningún servidor NTP respondió; revisa fecha, hora y conectividad UDP 123. |
| `Local clock needs synchronization` | Telegram detectó un desfase; corrige la hora del sistema y reinicia la app. |

---

## Métodos de inicio de sesión

En el paso del método de acceso, el asistente muestra dos pestañas: **Teléfono** y **Código QR**. Solo hay un flujo QR activo a la vez: cambiar a **Teléfono** cancela el QR en curso. Si pasas de **Teléfono** a **Código QR**, el código ya solicitado sigue siendo válido hasta que caduque o lo uses.

La pantalla de acceso incluye además un botón **Cambiar tema** para elegir el tema del panel antes de autenticarte; la selección se aplica y se guarda al instante.

### Acceso por número de teléfono

1. Escribe el número en formato internacional, por ejemplo `+34600000000`.
2. Pulsa **Enviar Código**.
3. Introduce el código recibido. Telegram suele entregarlo primero en una sesión abierta de la aplicación oficial; el SMS es solo un método alternativo.
4. Si Telegram ofreció otra vía de entrega, aparecerá el botón **¿No llegó? Probar otro método de entrega**. La petición alternativa nunca se envía de forma automática: solo cuando el usuario la solicita y el servidor de Telegram la ha ofrecido.
5. Si la cuenta tiene verificación en dos pasos, introduce la contraseña de 2FA.

### Acceso por código QR

1. Selecciona la pestaña **Código QR**.
2. En Telegram, abre **Ajustes → Dispositivos → Vincular dispositivo de escritorio** y escanea la imagen.
3. El código se renueva solo cuando caduca, siempre que la pantalla de acceso siga abierta.
4. Si la cuenta tiene 2FA, el asistente pasa al paso de contraseña después del escaneo.
5. Al confirmar Telegram, la sesión queda guardada y el panel pasa al escritorio.

Estados que puede devolver el flujo QR:

| Estado | Significado |
| :--- | :--- |
| `STARTING` | Se está preparando el primer código. |
| `WAITING_SCAN` | Código visible y pendiente de escaneo. |
| `WAITING_2FA` | Código aceptado; falta la contraseña de dos pasos. |
| `LOGGED_IN` | Autorización completada. |
| `EXPIRED` | El código dejó de ser válido y la pantalla estuvo inactiva demasiado tiempo. |
| `CANCELLED` | El flujo se canceló al cambiar de método, volver atrás o cerrar el asistente. |
| `ERROR` | Telegram rechazó el flujo; el mensaje se muestra en pantalla. |

:::tip[Seguridad del código QR]
El token del código QR nunca se envía al navegador. El backend genera la imagen PNG y solo expone la imagen y su fecha de caducidad. El código es de un solo uso y caduca en pocos minutos.
:::

### Verificación en dos pasos (2FA)

El paso de 2FA es común a ambos métodos. La contraseña es la **contraseña de la nube** de Telegram, no el código recibido por SMS o por la aplicación.

- Si vuelves atrás desde el paso de 2FA, TelegramDL cierra el intento a medias y regresa al teléfono, porque Telegram ya consumió el código anterior. Deberás solicitar uno nuevo.
- Si accedes al panel desde otro equipo por HTTP sin cifrar, el asistente advierte que la contraseña viajará en claro. Usa HTTPS o el equipo local para protegerla.

---

## Sesión persistente y recuperación

TelegramDL guarda la sesión MTProto en `~/.tgdown/tg_session.json` (en Windows, `%USERPROFILE%\.tgdown\tg_session.json`) y registra en SQLite que esa sesión quedó autorizada, junto con los datos públicos del usuario. Esa evidencia permite distinguir entre:

- una sesión válida que está atravesando un corte temporal de red, y
- un equipo que realmente necesita volver a autenticarse.

Estados principales que devuelve `GET /api/auth/status`:

| Estado | Significado |
| :--- | :--- |
| `UNCONFIGURED` | No hay credenciales de Telegram guardadas. |
| `NEED_PHONE` | Hay credenciales, pero no una sesión autorizada. |
| `CONNECTING` | Hay una sesión guardada y se está restableciendo la conexión. |
| `WAITING_CODE` | Se envió un código y se espera su verificación. |
| `WAITING_2FA` | El código fue aceptado y se espera la contraseña de 2FA. |
| `LOGGED_IN` | Sesión autorizada y operativa. |

### Por qué el login ya no se reinicia solo

- El paso `WAITING_CODE` o `WAITING_2FA` se conserva aunque el cliente se reconecte. La pantalla de acceso no vuelve al inicio por un fallo transitorio de red.
- La respuesta de `POST /api/auth/send-code` incluye el estado (`WAITING_CODE`), el teléfono pendiente y si las credenciales siguen configuradas, de modo que el asistente mantiene el paso correcto.
- Antes de enviar el código, verificar el código o validar 2FA, el backend espera a que el cliente esté listo y vuelve a leer el cliente activo. Así una reconexión o el cambio a WebSocket no deja la petición apuntando a un cliente viejo.
- Las operaciones de autenticación se serializan: no se pueden solapar dos envíos de código ni un flujo QR y uno por teléfono.
- La lista de chats y diálogos solo se solicita después de que la sesión esté autorizada. Antes competía con `auth.sendCode` y podía hacer fallar el primer inicio de sesión.
- Si la red falla pero la evidencia de sesión coincide con el archivo guardado, el estado se muestra como `CONNECTING` con el usuario conocido, sin pedir credenciales de nuevo.

:::caution[No compartas la sesión]
El archivo `tg_session.json` y la base de datos `tgdown.sqlite3` permiten acceder a tu cuenta. No los copies a otros equipos ni los envíes a terceros. Al cerrar sesión desde TelegramDL, el archivo local se elimina y la evidencia de autorización se limpia.
:::

---

## Solución de problemas

| Síntoma | Causa probable | Solución |
| :--- | :--- | :--- |
| El registro dice `Could not query NTP servers` | Sin salida UDP 123 o sin Internet | Permite NTP, activa la hora automática y reinicia TelegramDL. |
| El login falla tras conectar, con avisos de reloj | Desfase superior al que tolera Telegram | Sincroniza la hora del sistema y reinicia la aplicación. |
| El QR caduca casi al instante | Reloj local adelantado y NTP sin respuesta | Corrige la hora; TelegramDL compara la caducidad con el reloj corregido por NTP. |
| El código no llega por SMS | Telegram lo entregó primero en la app oficial | Revisa el chat de Telegram en tus dispositivos; usa el botón de método alternativo si aparece. |
| `verification code expired` o `invalid verification code` | Código antiguo o mal escrito | Vuelve al paso del teléfono y solicita un código nuevo. |
| `too many attempts, please wait` (`FLOOD_WAIT`) | Demasiados intentos seguidos | Espera el tiempo indicado por Telegram antes de reintentar. |
| `invalid 2FA password` | Contraseña de la nube incorrecta | Revisa la contraseña de verificación en dos pasos de Telegram. |
| `AUTH_KEY_UNREGISTERED` o `SESSION_REVOKED` | La sesión fue cerrada desde otro dispositivo | Inicia sesión de nuevo con teléfono o QR. |
| `telegram connection is not ready` al abrir QR | El cliente aún no está conectado | Comprueba la red, el proxy o prueba `--ws`; el QR se puede reintentar. |
| `telegram session is already authorized` | Ya hay una sesión activa | Entra al panel; no hace falta volver a autenticarse. |

Los registros de diagnóstico de autenticación (`[TG AUTH]` y `[TG AUTH DIAG]`) nunca incluyen el teléfono, el código, la contraseña ni el hash del código. Si necesitas soporte, puedes compartir esas líneas con seguridad.
