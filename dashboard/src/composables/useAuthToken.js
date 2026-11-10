// Maneja el token de acceso a la API HTTP local.
//
// - En la app de escritorio (Wails), el token se obtiene automáticamente por
//   el binding nativo `window.go.main.App.GetLocalToken()`: el usuario nunca
//   tiene que escribirlo.
// - En un navegador local (127.0.0.1 / localhost), el token se obtiene
//   automáticamente del endpoint de loopback `/api/auth/local-token`.
// - En un navegador remoto (celular, otra PC vía Tailscale/túnel) no existe
//   ese acceso local, así que se pide una vez y se guarda en localStorage.
import { ref } from 'vue'
import { t } from '../i18n'

const token = ref('')
const ready = ref(false)

export const isWailsRuntime = () =>
  typeof window !== 'undefined' &&
  (window.location.host.includes('wails') || !!window._wails || !!window.wails)

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
  if (isWailsRuntime() || isLocalView()) return true
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

    if (!token.value && isLocalView()) {
      try {
        const response = await fetch('/api/auth/local-token')
        if (response.ok) {
          const data = await response.json()
          if (data.token) {
            token.value = data.token
          }
        }
      } catch (e) {
        console.error(t('console.wailsTokenError'), e)
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
