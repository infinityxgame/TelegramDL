import { createApp } from 'vue'
import App from './App.vue'
import './style.css'
// Decorado de los temas especiales (color 16 en adelante). Va todo dentro de
// una clase que solo se pone en el <html> cuando uno de esos temas está
// activo, así que con los colores de Telegram no hay ni una regla aplicada.
import './themes-dl.css'
import { isWailsRuntime } from './composables/useAuthToken'
import * as AppBindings from './bindings/tgdown/app.js'

if (typeof window !== 'undefined') {
  window.go = window.go || {}
  window.go.main = window.go.main || {}
  window.go.main.App = AppBindings
}

// Dentro de la app de escritorio el panel debe comportarse como una ventana
// nativa y no como una página web (ver .app-nativo en style.css). Se comprueba
// dos veces por si el bundle llegara a ejecutarse antes de que Wails inyecte
// sus bindings: la segunda pasada, ya con la página cargada, no falla nunca.
const markNative = () => {
  if (isWailsRuntime()) document.documentElement.classList.add('app-nativo')
}
markNative()
window.addEventListener('load', markNative, { once: true })

createApp(App).mount('#app')
