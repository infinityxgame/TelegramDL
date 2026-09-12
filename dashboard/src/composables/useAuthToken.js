// Maneja el token de acceso a la API HTTP local.
//
// - En la app de escritorio (Wails), el token se obtiene automáticamente por
//   el binding nativo `window.go.main.App.GetLocalToken()`: el usuario nunca
//   tiene que escribirlo.
// - En un navegador remoto (celular, otra PC vía Tailscale/túnel) no existe
//   ese binding, así que se pide una vez y se guarda en localStorage de ese
//   navegador.
import { ref } from 'vue'

const token = ref('')
const ready = ref(false)

export const isWailsRuntime = () =>
  typeof window !== 'undefined' && !!(window.go && window.go.main && window.go.main.App && window.go.main.App.GetLocalToken)

const STORAGE_KEY = 'tgdl_api_token'

export function useAuthToken () {
  const initToken = async () => {
    if (isWailsRuntime()) {
      try {
        token.value = await window.go.main.App.GetLocalToken()
      } catch (e) {
        console.error('No se pudo obtener el token local de Wails:', e)
        token.value = ''
      }
    } else {
      try {
        token.value = localStorage.getItem(STORAGE_KEY) || ''
      } catch (e) {
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
      } catch (e) { /* localStorage no disponible: seguimos solo en memoria */ }
    }
  }

  const clearToken = () => setToken('')

  const authHeaders = () => (token.value ? { Authorization: `Bearer ${token.value}` } : {})

  return { token, ready, initToken, setToken, clearToken, authHeaders, isWailsRuntime }
}
