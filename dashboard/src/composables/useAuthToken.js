// Maneja el token de acceso a la API HTTP local.
//
// - En la app de escritorio (Wails), el token se obtiene automáticamente por
//   el binding nativo `window.go.main.App.GetLocalToken()`: el usuario nunca
//   tiene que escribirlo.
// - En cualquier navegador, incluido el del propio equipo (127.0.0.1 /
//   localhost), se pide una vez y se guarda en localStorage. No se toma del
//   endpoint de loopback: abrir el panel en un navegador siempre exige token.
// - El token guardado se conserva hasta que caduque o se regenere en Ajustes;
//   entonces la pantalla de acceso vuelve a pedirlo.
import { ref } from 'vue'
import { t } from '../i18n'

const token = ref('')
const ready = ref(false)

// isWailsRuntime distingue la ventana nativa del panel abierto en un navegador.
//
// No sirve mirar `window._wails` ni `window.go` a secas: @wailsio/runtime crea
// `window._wails` en cualquier navegador y main.js publica `window.go.main.App`
// siempre, así que ambos existen también en web. Solo la app nativa inyecta el
// objeto `environment` (internal/runtime de Wails) y solo su webview sirve el
// panel desde wails.localhost.
export const isWailsRuntime = () =>
  typeof window !== 'undefined' &&
  (window.location.host.includes('wails') || !!window._wails?.environment?.OS)

export const isLocalView = () => {
  if (typeof window === 'undefined') return false
  const host = (window.location.hostname || '').toLowerCase()
  return (
    host === '127.0.0.1' ||
    host === 'localhost' ||
    host === '::1' ||
    host.includes('wails')
  )
}

const STORAGE_KEY = 'tgdl_api_token'

// hasStoredToken responde en el mismo instante, sin esperar a initToken(). Sirve
// para decidir en el primer pintado si hay que enseñar la animación de arranque
// o ir directos a la pantalla del token, en vez de mostrar una y cambiarla un
// parpadeo después.
export const hasStoredToken = () => {
  if (isWailsRuntime()) return true
  try {
    return !!localStorage.getItem(STORAGE_KEY)
  } catch {
    return false
  }
}

export function useAuthToken() {
  const initToken = async () => {
    if (isWailsRuntime()) {
      try {
        token.value = await window.go.main.App.GetLocalToken()
      } catch (e) {
        console.error(t('console.wailsTokenError'), e)
        token.value = ''
      }
    } else {
      try {
        token.value = localStorage.getItem(STORAGE_KEY) || ''
      } catch {
        token.value = ''
      }
    }

    ready.value = true
    return token.value
  }

  const setToken = (value) => {
    token.value = value || ''
    if (!isWailsRuntime()) {
      try {
        if (token.value) {
          localStorage.setItem(STORAGE_KEY, token.value)
        } else {
          localStorage.removeItem(STORAGE_KEY)
        }
      } catch {
        console.error(t('console.localStorageUnavailable'))
      }
    }
  }

  const clearToken = () => setToken('')

  const authHeaders = () =>
    token.value ? { Authorization: `Bearer ${token.value}` } : {}

  return {
    token,
    ready,
    initToken,
    setToken,
    clearToken,
    authHeaders,
    isWailsRuntime
  }
}
