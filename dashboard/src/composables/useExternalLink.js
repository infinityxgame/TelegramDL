// Abre enlaces externos fuera de la aplicación.
//
// Dentro de la app de escritorio el panel se pinta en un webview, y un
// <a target="_blank"> no se comporta como en un navegador: el webview no sabe
// abrir ventanas nuevas. En macOS (WKWebView) el clic directamente no hace
// nada, y en Windows (WebView2) el sitio acabaría cargándose dentro de la
// propia ventana de TelegramDL, que tampoco es lo que se quiere.
//
// Wails expone window.runtime.BrowserOpenURL para justo esto: le pasa la URL
// al navegador predeterminado del sistema. Ese runtime solo existe dentro de
// la app; cuando el panel se ve desde un navegador remoto (celular, otra PC)
// no está, y ahí el target="_blank" del propio enlace ya funciona solo.
import { Browser } from '@wailsio/runtime'
import { isWailsRuntime } from './useAuthToken'

export const canOpenExternally = () => isWailsRuntime()

// openExternal se engancha al @click del enlace. Fuera de la app no toca nada
// y deja que el navegador siga el href como siempre, así que el enlace sigue
// siendo un enlace de verdad: se puede copiar la dirección, abrirlo con el
// botón central o con Ctrl/Cmd + clic.
export const openExternal = (url, event) => {
  if (!canOpenExternally()) return
  if (event) event.preventDefault()
  Browser.OpenURL(url)
}

export function useExternalLink() {
  return { openExternal, canOpenExternally }
}
