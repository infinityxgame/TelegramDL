# Acceso remoto a TelegramDL

TelegramDL expone un servidor HTTP/WebSocket local (por defecto en
`127.0.0.1:8000`) que el panel usa tanto dentro de la ventana de escritorio
como si lo abres en un navegador. Desde la versión que incluye este
documento, toda la API (`/api/*`) exige un **token de acceso** (Ajustes →
Acceso remoto, dentro de la app), así que nadie puede controlar tus
descargas ni leer tus credenciales de Telegram sin ese token.

Eso resuelve la autenticación, pero **no** resuelve cómo llegar hasta el
servidor desde fuera de tu red local. Para eso, evita a toda costa exponer
el puerto 8000 directamente a internet (`TGDL_BIND_HOST=0.0.0.0` +
port-forwarding en el router): un puerto así recibe tráfico de escáneres
automáticos en cuestión de minutos. En su lugar, usa una de estas dos
opciones.

## Opción recomendada: Tailscale (VPN de malla)

[Tailscale](https://tailscale.com) crea una red privada cifrada entre tus
dispositivos sin abrir ningún puerto en el router.

1. Instala Tailscale en la PC donde corre TelegramDL y en tu celular (o
   cualquier otro dispositivo desde el que quieras controlarlo).
2. Inicia sesión con la misma cuenta en ambos.
3. En la PC, revisa la IP de Tailscale asignada (suele ser `100.x.y.z`);
   Tailscale la muestra en su propio panel/bandeja del sistema.
4. Desde el celular (con Tailscale activo), abre en el navegador:
   `http://100.x.y.z:8000`
5. La primera vez te pedirá el token de acceso: ábrelo en la app de
   escritorio en **Ajustes → Acceso remoto**, cópialo y pégalo. El
   navegador lo recordará para la próxima vez.

Alternativas equivalentes si prefieres no depender de la infraestructura de
Tailscale: **WireGuard** (autogestionado) o **ZeroTier**.

## Alternativa: túnel público (Cloudflare Tunnel)

Si prefieres una URL pública en vez de instalar un cliente VPN en cada
dispositivo:

1. Instala `cloudflared` en la PC con TelegramDL.
2. Crea un túnel apuntando a `http://127.0.0.1:8000`.
3. En el dashboard de Cloudflare, activa **Cloudflare Access** sobre ese
   túnel y restringe el acceso a tu email (login por código de un solo uso)
   u otro proveedor de identidad.
4. Accede desde cualquier dispositivo a la URL pública que te da Cloudflare
   y, la primera vez, pega el token de acceso de la app.

Esto añade una capa de autenticación *antes* de que la petición llegue
siquiera a TelegramDL, además del token propio de la app.

## Qué hace el token y cómo se gestiona

- Se genera automáticamente la primera vez que arranca la app; no hace
  falta configurar nada para seguir usándola normalmente en el mismo
  equipo (la propia ventana de escritorio lo obtiene por su cuenta).
- Se ve y se puede copiar desde **Ajustes → Acceso remoto**.
- Si sospechas que se filtró (por ejemplo, lo compartiste sin querer),
  pulsa **Regenerar token** ahí mismo: el anterior deja de funcionar de
  inmediato y tendrás que volver a introducir el nuevo en cada dispositivo
  remoto.
- Nunca lo compartas por canales inseguros (chat sin cifrar, capturas de
  pantalla públicas, etc.): quien lo tenga puede controlar la app por
  completo mientras siga siendo válido.

## Qué NO hacer

- No pongas `TGDL_BIND_HOST=0.0.0.0` y abras el puerto 8000 en el router
  para "simplificar" el acceso remoto. Aunque el token protege la API,
  seguirías exponiendo el servicio a escaneos e intentos de fuerza bruta
  de todo internet sin necesidad.
- No compartas el token por el mismo canal que uses para compartir el
  enlace/IP de acceso (si uno se filtra, que el otro no se filtre con él).
