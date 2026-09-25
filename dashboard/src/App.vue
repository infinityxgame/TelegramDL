<script setup>
import {
  computed,
  nextTick,
  onMounted,
  onUnmounted,
  reactive,
  ref,
  watch
} from 'vue'
import DownloadsView from './views/DownloadsView.vue'
import ListenerView from './views/ListenerView.vue'
import LogsView from './views/LogsView.vue'
import SettingsView from './views/SettingsView.vue'
import ConfirmModal from './components/ConfirmModal.vue'
import AuthWizard from './components/AuthWizard.vue'
import RemoteLogin from './components/RemoteLogin.vue'
import { hasStoredToken, useAuthToken } from './composables/useAuthToken'
import { useConfirmModal } from './composables/useConfirmModal'
import {
  applyLoaderTheme,
  applyTheme,
  initTheme,
  loadPublicTheme,
  themeMap
} from './utils/theme.js'
import { useUpdater } from './composables/useUpdater'
import {
  ArrowDownToLine,
  ArrowUpRight,
  CheckCircle2,
  LogOut,
  Menu,
  Radio,
  ScrollText,
  Settings2,
  UserCheck,
  X,
  Zap
} from './icons'

const { token, initToken, setToken, clearToken, authHeaders, isWailsRuntime } =
  useAuthToken()

// Saber ya en el primer pintado si vamos a cargar la aplicación o a pedir el
// token: leer el almacenamiento del navegador es inmediato, así que no hay
// motivo para enseñar la animación de arranque y sustituirla un instante
// después por la pantalla del token.
const startsAuthenticated = hasStoredToken()
const needsRemoteLogin = ref(false)
const remoteLoginError = ref('')
const hasTelegramCredentials = ref(false)

const downloads = ref([])
const listenerItems = ref([])
// Último ID del registro conocido por el servidor. Cambia en cada snapshot y
// es la señal que usa la vista de logs para pedir solo lo nuevo.
const logsSeq = ref(0)
const loading = ref(false)
const message = ref('')
const error = ref('')
const saving = ref(false)
const hydrated = ref(false)
const host = window.location.host
const logoUrl = `${import.meta.env.BASE_URL}telegramdl-android-icon.svg`
const activeView = ref('downloads')
const mobileMenuOpen = ref(false)
const disk = ref(null)

// ── Tema del panel (lógica en utils/theme.js: módulo sin estado reactivo) ──
// Aplica el tema guardado antes del primer pintado para evitar un parpadeo de color.
initTheme()

// Verifica si hay credenciales de Telegram configuradas (API ID y API Hash).
// Es un endpoint público que solo devuelve un booleano sin información sensible.
// Ahora también verifica si hay una sesión activa de Telegram.
const checkTelegramCredentials = async () => {
  try {
    const response = await fetch('/api/auth/has-credentials')
    if (!response.ok) return false
    const data = await response.json()
    // is_configured indica que hay credenciales Y sesión activa
    // Si solo hay credenciales pero no sesión, devolvemos false para mostrar AuthWizard
    hasTelegramCredentials.value = data.is_configured === true
    return hasTelegramCredentials.value
  } catch {
    // Si falla, asumimos que no hay credenciales por seguridad
    hasTelegramCredentials.value = false
    return false
  }
}

const authStatus = ref({
  authenticated: localStorage.getItem('tgdl_auth') === 'true',
  state:
    localStorage.getItem('tgdl_auth') === 'true' ? 'LOGGED_IN' : 'UNCONFIGURED',
  has_credentials: true,
  user: JSON.parse(localStorage.getItem('tgdl_user') || 'null')
})

const settings = reactive({
  max_concurrent_downloads: 2,
  parallel_chunks: true,
  chunk_workers: 8,
  speed_limit: { value: 0, unit: 'MB' },
  color_id: 5,
  loader_color_id: 5,
  download_folder: '',
  organize_by_chat: true
})

const resetColor = () => {
  if (authStatus.value.user && authStatus.value.user.color_id !== undefined) {
    settings.color_id = authStatus.value.user.color_id
  } else {
    settings.color_id = 5
  }
}

const resetLoaderColor = () => {
  settings.loader_color_id = settings.color_id
}

watch(
  () => settings.color_id,
  (newVal) => {
    if (newVal !== undefined) applyTheme(newVal)
  }
)

watch(
  () => settings.loader_color_id,
  (newVal) => {
    if (newVal !== undefined) applyLoaderTheme(newVal)
  }
)

let timer
let socket
let reconnectTimer
let saveTimer
let updateCheckTimer
let disposed = false
let syncingSettings = false
const settingsSavePending = ref(false)
const websocketConnected = ref(false)
const bootstrapping = ref(true)
// La animación de arranque solo se muestra cuando de verdad se va a cargar la
// aplicación. Si ya sabemos que toca pedir el token, se espera en silencio a
// conocer el color y se pinta la pantalla del token directamente.
const showSplash = ref(startsAuthenticated)

// Sin token guardado se pide el color antes de pintar la pantalla del token,
// para que no aparezca primero en azul y cambie de color un instante después.
// Si el servidor tardara en responder, se muestra igualmente pasado un margen
// corto en lugar de dejar la pantalla en blanco.
if (!startsAuthenticated) {
  Promise.race([
    loadPublicTheme(),
    new Promise((resolve) => setTimeout(resolve, 400))
  ]).finally(async () => {
    bootstrapping.value = false
    // Verificar si hay credenciales de Telegram configuradas antes de decidir
    // qué pantalla mostrar. Si no hay credenciales, se debe mostrar el AuthWizard
    // para configurar Telegram primero. Si hay credenciales, se muestra RemoteLogin.
    const hasCreds = await checkTelegramCredentials()
    needsRemoteLogin.value = hasCreds
  })
}
const resolvedFileNames = new Map()
const duplicatePrompted = new Set()

const isPlaceholderFileName = (name) =>
  /^mensaje_\d+$/i.test(String(name || '').trim())
const mergeDownloadNames = (items) =>
  items.map((item) => {
    const currentName = String(item.file_name || '').trim()
    if (currentName && !isPlaceholderFileName(currentName)) {
      resolvedFileNames.set(item.id, currentName)
    } else if (resolvedFileNames.has(item.id)) {
      return { ...item, file_name: resolvedFileNames.get(item.id) }
    }
    return item
  })

const formatSize = (bytes) => {
  if (!bytes) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i]
}

const parseSpeed = (speedStr) => {
  if (!speedStr || typeof speedStr !== 'string') return 0
  const match = speedStr.match(/^([\d.]+)\s*([A-Za-z]+)\/s$/)
  if (!match) return 0
  const multipliers = {
    B: 1,
    KB: 1024,
    MB: 1024 ** 2,
    GB: 1024 ** 3,
    TB: 1024 ** 4
  }
  return parseFloat(match[1]) * (multipliers[match[2].toUpperCase()] || 1)
}

const syncSettings = async (nextSettings) => {
  syncingSettings = true
  Object.assign(settings, nextSettings)
  await nextTick()
  syncingSettings = false
}

const showMessage = (txt, isError = false) => {
  if (isError) {
    if (
      txt.toLowerCase().includes('espacio no es suficiente') ||
      txt.toLowerCase().includes('libera espacio')
    ) {
      openConfirm({
        title: '¡Espacio en disco insuficiente!',
        message: txt,
        confirmText: 'Entendido',
        cancelText: '',
        type: 'danger',
        action: () => {}
      })
      return
    }
    error.value = txt
    setTimeout(() => {
      if (error.value === txt) error.value = ''
    }, 5000)
  } else {
    message.value = txt
    setTimeout(() => {
      if (message.value === txt) message.value = ''
    }, 3500)
  }
}

const { modal, openConfirm, handleConfirm, handleCancel } = useConfirmModal()

const api = async (url, options = {}) => {
  const response = await fetch(url, {
    ...options,
    headers: { ...(options.headers || {}), ...authHeaders() }
  })
  if (response.status === 401) {
    if (!isWailsRuntime()) {
      clearToken()
      // Verificar si hay credenciales de Telegram configuradas antes de decidir
      // qué pantalla mostrar. Si no hay credenciales, se debe mostrar el AuthWizard
      // para configurar Telegram primero. Si hay credenciales, se muestra RemoteLogin.
      const hasCreds = await checkTelegramCredentials()
      needsRemoteLogin.value = hasCreds
      loadPublicTheme()
    }
    throw new Error('No autorizado: token de acceso inválido o ausente')
  }
  const data = await response.json().catch(() => ({}))
  if (!response.ok)
    throw new Error(data.detail || data.error || 'Error en el servidor')
  return data
}

const fetchAuthStatus = async () => {
  try {
    const data = await api('/api/auth/status')
    authStatus.value = data
    if (data.authenticated) {
      localStorage.setItem('tgdl_auth', 'true')
      localStorage.setItem('tgdl_user', JSON.stringify(data.user || null))
      if (data.user && data.user.color_id !== undefined)
        applyTheme(data.user.color_id)
    } else {
      localStorage.removeItem('tgdl_auth')
      localStorage.removeItem('tgdl_user')
      applyTheme(5)
    }
  } catch (err) {
    console.error('Error verificando estado de sesión:', err)
  }
}

const logoutTelegram = async () => {
  openConfirm({
    title: 'Cerrar sesión de Telegram',
    message:
      '¿Estás seguro de que deseas cerrar sesión? Tendrás que volver a autenticarte desde la web.',
    confirmText: 'Sí, cerrar sesión',
    type: 'danger',
    action: async () => {
      try {
        await api('/api/auth/logout', { method: 'POST' })
        localStorage.removeItem('tgdl_auth')
        localStorage.removeItem('tgdl_user')
        await fetchAuthStatus()
        showMessage('Sesión cerrada con éxito')
      } catch (err) {
        showMessage(err.message, true)
      }
    }
  })
}

const onAuthSuccess = async (newStatus) => {
  authStatus.value = newStatus
  if (
    newStatus &&
    (newStatus.authenticated || newStatus.state === 'LOGGED_IN')
  ) {
    localStorage.setItem('tgdl_auth', 'true')
    localStorage.setItem('tgdl_user', JSON.stringify(newStatus.user))
    if (newStatus.user && newStatus.user.color_id !== undefined)
      applyTheme(newStatus.user.color_id)
    await fetchAuthStatus()
    await fetchSettings()
    await fetchDownloads()
  }
}

const fetchDownloads = async () => {
  try {
    const data = await api('/api/downloads')
    if (Array.isArray(data)) {
      const normalizedDownloads = mergeDownloadNames(data)
      downloads.value = normalizedDownloads
      normalizedDownloads
        .filter((item) => item.status === 'duplicate')
        .forEach(promptDuplicateDownload)
      websocketConnected.value = true
    }
    error.value = ''
  } catch {
    /* No mostramos error en poll constante */
  }
}

// El WebSocket (o los eventos nativos de Wails) ya empujan el estado en
// tiempo real; este polling es solo el respaldo para cuando esa conexión
// está caída. Mientras esté sana, lo espaciamos mucho (chequeo de vida)
// en vez de repetir cada segundo la misma información que ya llegó por
// el canal push, que era el mayor costo de red/CPU en historiales grandes.
const scheduleDownloadsPoll = () => {
  if (disposed) return
  fetchDownloads().finally(() => {
    if (disposed) return
    const delay = websocketConnected.value ? 15000 : 1000
    timer = setTimeout(scheduleDownloadsPoll, delay)
  })
}

const fetchSettings = async () => {
  try {
    const data = await api('/api/settings')
    await syncSettings(data.settings)
    hydrated.value = true
  } catch (err) {
    showMessage(err.message, true)
  }
}

const saveSettings = async () => {
  saving.value = true
  try {
    const data = await api('/api/settings', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(settings)
    })
    await syncSettings(data.settings)
    showMessage('Configuración guardada')
  } catch (err) {
    showMessage(err.message, true)
  } finally {
    saving.value = false
    settingsSavePending.value = false
  }
}

const regenerateToken = () => {
  openConfirm({
    title: 'Regenerar token de acceso',
    message:
      'El token actual dejará de funcionar de inmediato. Cualquier otro dispositivo (celular, otra PC) que lo esté usando para acceso remoto necesitará que le pases el nuevo token.',
    confirmText: 'Sí, regenerar',
    type: 'danger',
    action: async () => {
      try {
        let newToken = ''
        if (isWailsRuntime() && window.go?.main?.App?.RegenerateLocalToken) {
          newToken = await window.go.main.App.RegenerateLocalToken()
        } else {
          const data = await api('/api/auth/token/regenerate', {
            method: 'POST'
          })
          newToken = data.token
        }
        setToken(newToken)
        showMessage('Token regenerado')
      } catch (err) {
        showMessage(err.message, true)
      }
    }
  })
}

const clearDownloadHistory = () => {
  openConfirm({
    title: 'Limpiar estadísticas',
    message:
      '¿Quieres eliminar del historial todas las descargas completadas, omitidas, fallidas y canceladas? Los archivos del disco no se borrarán.',
    confirmText: 'Sí, limpiar historial',
    type: 'danger',
    action: async () => {
      try {
        const data = await api('/api/downloads/history', { method: 'DELETE' })
        await fetchDownloads()
        showMessage(`${data.removed || 0} registros eliminados`)
      } catch (err) {
        showMessage(err.message, true)
      }
    }
  })
}

watch(
  settings,
  () => {
    if (!hydrated.value || syncingSettings) return
    settingsSavePending.value = true
    clearTimeout(saveTimer)
    saveTimer = setTimeout(saveSettings, 450)
  },
  { deep: true }
)

const startDownload = async (url) => {
  const targetUrl = typeof url === 'string' ? url.trim() : ''
  if (!targetUrl) return
  loading.value = true
  try {
    await api('/api/download', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url: targetUrl })
    })
    showMessage('Descarga añadida a la cola')
    await fetchDownloads()
  } catch (err) {
    showMessage(err.message, true)
  } finally {
    loading.value = false
  }
}

const cancelDownload = async (id) => {
  try {
    await api('/api/cancel', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id })
    })
    await fetchDownloads()
  } catch (err) {
    showMessage(err.message, true)
  }
}

const setDownloadPause = async (id, paused) => {
  try {
    await api(`/api/${paused ? 'pause' : 'resume'}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id })
    })
    showMessage(paused ? 'Descarga pausada' : 'Descarga reanudada')
    await fetchDownloads()
  } catch (err) {
    showMessage(err.message, true)
  }
}

const retryDownload = async (item) => {
  try {
    await api('/api/retry', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: item.id })
    })
    showMessage('Reintentando descarga…')
    await fetchDownloads()
  } catch (err) {
    showMessage(err.message, true)
  }
}

const pauseAllDownloads = async () => {
  try {
    await api('/api/downloads/pause-all', { method: 'POST' })
    showMessage('Todas las descargas pausadas')
    await fetchDownloads()
  } catch (err) {
    showMessage(err.message, true)
  }
}

const resumeAllDownloads = async () => {
  try {
    await api('/api/downloads/resume-all', { method: 'POST' })
    showMessage('Todas las descargas reanudadas')
    await fetchDownloads()
  } catch (err) {
    showMessage(err.message, true)
  }
}

const cancelAllDownloads = async () => {
  openConfirm({
    title: 'Cancelar todo',
    message:
      '¿Estás seguro de que quieres cancelar todas las descargas activas y en cola?',
    confirmText: 'Sí, cancelar todo',
    type: 'danger',
    action: async () => {
      try {
        await api('/api/downloads/cancel-all', { method: 'POST' })
        showMessage('Todas las descargas canceladas')
        await fetchDownloads()
      } catch (err) {
        showMessage(err.message, true)
      }
    }
  })
}

const deleteDownload = async (item) => {
  openConfirm({
    title: 'Borrar archivo',
    message: `¿Estás seguro de que quieres eliminar "${item.file_name}" del servidor? Esta acción no se puede deshacer.`,
    confirmText: 'Sí, borrar archivo',
    type: 'danger',
    action: async () => {
      try {
        await api(
          `/api/downloads/${encodeURIComponent(item.id)}?delete_file=true`,
          { method: 'DELETE' }
        )
        showMessage('Archivo borrado')
        await fetchDownloads()
      } catch (err) {
        showMessage(err.message, true)
      }
    }
  })
}

const openFile = async (item) => {
  try {
    await api('/api/downloads/open', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: item.id })
    })
  } catch (err) {
    showMessage(err.message, true)
  }
}

const {
  version,
  updateInfo,
  isUpdating,
  isUpdateForced,
  updateProgress,
  installUpdate,
  checkForUpdates
} = useUpdater({ api, showMessage, openConfirm })

const viewTitle = computed(
  () =>
    ({
      downloads: 'Descargas',
      listener: 'Escucha',
      logs: 'Logs',
      settings: 'Ajustes'
    })[activeView.value] || 'Descargas'
)

const speedText = computed(() =>
  settings.speed_limit.value > 0
    ? `${settings.speed_limit.value} ${settings.speed_limit.unit}/s`
    : 'Sin límite'
)
const totalSpeed = computed(() => {
  const totalBytes = downloads.value.reduce(
    (acc, item) =>
      item.status === 'downloading' ? acc + parseSpeed(item.speed) : acc,
    0
  )
  return totalBytes > 0 ? formatSize(totalBytes) + '/s' : '0 B/s'
})

const wasSpaceCritical = ref(false)
const trackedQueueIds = new Set()
let queueNotificationArmed = false

const isDownloadFinished = (status) =>
  ['completed', 'skipped', 'failed', 'cancelled'].includes(status)
const isDownloadSuccessful = (status) =>
  ['completed', 'skipped'].includes(status)

const promptDuplicateDownload = (item) => {
  if (duplicatePrompted.has(item.id) || modal.show) return

  duplicatePrompted.add(item.id)
  openConfirm({
    title: 'Archivo ya descargado',
    message: `El archivo "${item.file_name}" ya existe en la carpeta de descargas. ¿Quieres descargarlo nuevamente?`,
    confirmText: 'Descargar de nuevo',
    cancelText: 'Cancelar',
    type: 'primary',
    action: async () => {
      try {
        await api('/api/retry', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id: item.id })
        })
        await fetchDownloads()
      } catch (err) {
        duplicatePrompted.delete(item.id)
        showMessage(err.message, true)
      }
    },
    cancelAction: async () => {
      // Cancelar el aviso de duplicado solo descarta la entrada del historial:
      // el archivo existente en disco (que pertenece a la descarga original)
      // no se toca.
      try {
        await api(
          `/api/downloads/${encodeURIComponent(item.id)}?delete_file=false`,
          { method: 'DELETE' }
        )
        await fetchDownloads()
      } catch (err) {
        showMessage(err.message, true)
      }
    }
  })
}

const maybeNotifyQueueFinished = (currentDownloads) => {
  if (!queueNotificationArmed || trackedQueueIds.size === 0) return

  const byID = new Map(currentDownloads.map((item) => [item.id, item]))
  for (const id of trackedQueueIds) {
    if (!byID.has(id)) trackedQueueIds.delete(id)
  }
  const trackedItems = [...trackedQueueIds]
    .map((id) => byID.get(id))
    .filter(Boolean)
  if (trackedItems.length === 0) {
    queueNotificationArmed = false
    return
  }
  if (!trackedItems.every((item) => isDownloadFinished(item.status))) return

  const successful = trackedItems.every((item) =>
    isDownloadSuccessful(item.status)
  )
  trackedQueueIds.clear()
  queueNotificationArmed = false

  if (successful) {
    new Audio(`${import.meta.env.BASE_URL}notification.wav`)
      .play()
      .catch((e) => console.log('Audio blocked', e))
  }
}

watch(
  () => disk.value,
  (newDisk) => {
    if (!newDisk) return
    if (newDisk.projected_free < 0) {
      if (!wasSpaceCritical.value && !modal.show) {
        wasSpaceCritical.value = true
        const needed = formatSize(Math.abs(newDisk.projected_free))
        openConfirm({
          title: '¡Alerta de Espacio Crítico!',
          message: `Debido a cambios externos en tu disco, ya no hay espacio suficiente para completar las descargas en cola. \n\nNecesitas liberar al menos ${needed} o cancelar algunas tareas para evitar errores.`,
          confirmText: 'Entendido',
          cancelText: '',
          type: 'danger',
          action: () => {}
        })
      }
    } else {
      wasSpaceCritical.value = false
    }
  },
  { deep: true }
)

const handleStateUpdate = (data) => {
  if (!data) return
  if (Array.isArray(data.downloads)) {
    const normalizedDownloads = mergeDownloadNames(data.downloads)
    downloads.value = normalizedDownloads
    normalizedDownloads
      .filter((item) => item.status === 'duplicate')
      .forEach(promptDuplicateDownload)
    data.downloads.forEach((item) => {
      if (['queued', 'downloading'].includes(item.status)) {
        trackedQueueIds.add(item.id)
        queueNotificationArmed = true
      }
    })
    maybeNotifyQueueFinished(data.downloads)
  }
  if (Array.isArray(data.listener)) {
    listenerItems.value = data.listener
  }
  if (typeof data.logs_seq === 'number') {
    logsSeq.value = data.logs_seq
  }
  if (data.disk) {
    disk.value = data.disk
  }
  if (data.settings && !settingsSavePending.value && !saving.value)
    syncSettings(data.settings)
}

const connectWebSocket = async () => {
  if (window.runtime && window.runtime.EventsOn) {
    websocketConnected.value = true
    window.runtime.EventsOn('tgdl:state', handleStateUpdate)
  }

  // El token viaja como subprotocolo, no en la URL. Las URL completas quedan
  // registradas en cualquier proxy o túnel por el que pase la conexión, que es
  // justo lo que se usa para el acceso remoto; la cabecera del subprotocolo no.
  const protocolos = ['tgdl-v1']
  if (token.value) {
    protocolos.push(token.value)
  }

  const wsUrl =
    window.location.host && !window.location.host.includes('wails')
      ? `${window.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${window.location.host}/api/ws`
      : 'ws://127.0.0.1:8000/api/ws'

  try {
    socket = new WebSocket(wsUrl, protocolos)
    socket.onopen = () => {
      websocketConnected.value = true
    }
    socket.onmessage = (event) => {
      try {
        const data = JSON.parse(event.data)
        if (data.type === 'state') {
          handleStateUpdate(data)
        }
      } catch (e) {
        console.error(e)
      }
    }
    socket.onclose = () => {
      if (!window.runtime) websocketConnected.value = false
      if (!disposed) reconnectTimer = setTimeout(connectWebSocket, 2000)
    }
    socket.onerror = () => {
      socket?.close()
    }
  } catch {
    if (!window.runtime) websocketConnected.value = false
    if (!disposed) reconnectTimer = setTimeout(connectWebSocket, 2000)
  }
}

const startApp = async () => {
  disposed = false

  // Iniciar servicios y comprobación inmediata
  connectWebSocket()
  scheduleDownloadsPoll()

  // Iniciamos el ciclo de actualizaciones de fondo inmediatamente para evitar esperas
  setTimeout(() => checkForUpdates(false), 1000)
  // Cada 5 minutos: la API de GitHub solo admite 60 peticiones por hora y por
  // IP, y a 2 minutos se agotaba la cuota sin necesidad.
  updateCheckTimer = setInterval(() => checkForUpdates(false), 5 * 60 * 1000)

  const startBoot = Date.now()
  try {
    await Promise.all([fetchAuthStatus(), checkForUpdates(true)])
    if (authStatus.value.authenticated) {
      await Promise.all([fetchSettings(), fetchDownloads()]).catch(() => {})
    }
  } catch (err) {
    console.error('Error durante el arranque:', err)
  } finally {
    const elapsed = Date.now() - startBoot
    const minTime = 6500 // 4.5s animación + 2s de reposo con puntos
    const remaining = Math.max(0, minTime - elapsed)
    setTimeout(() => {
      bootstrapping.value = false
    }, remaining)
  }
}

// Valida un token recién introducido en la pantalla de login remoto antes de
// arrancar el resto de la app con él.
const handleRemoteLogin = async (value) => {
  remoteLoginError.value = ''
  setToken(value)
  try {
    await api('/api/system/info')
    needsRemoteLogin.value = false
    // Token aceptado: a partir de aquí sí se carga la aplicación, así que la
    // animación de arranque vuelve a tener sentido.
    showSplash.value = true
    bootstrapping.value = true
    await startApp()
  } catch {
    clearToken()
    remoteLoginError.value =
      'Token inválido. Verifica que lo copiaste completo desde Ajustes → Acceso remoto.'
  }
}

onMounted(async () => {
  // Resolver el token de acceso antes de llamar a cualquier endpoint /api/*:
  // en la app de escritorio llega solo (binding nativo de Wails); en un
  // navegador remoto, si no hay uno guardado, pedimos la pantalla de login.
  await initToken()
  if (isWailsRuntime() || token.value) {
    await startApp()
  } else {
    bootstrapping.value = false
    // Verificar si hay credenciales de Telegram configuradas antes de decidir
    // qué pantalla mostrar. Si no hay credenciales, se debe mostrar el AuthWizard
    // para configurar Telegram primero. Si hay credenciales, se muestra RemoteLogin.
    const hasCreds = await checkTelegramCredentials()
    needsRemoteLogin.value = hasCreds
    // Solo si creíamos tener token y resultó no haberlo: en el otro caso el
    // color ya se pidió antes de pintar nada.
    if (startsAuthenticated) {
      await loadPublicTheme()
    }
  }
})

onUnmounted(() => {
  disposed = true
  clearTimeout(timer)
  clearInterval(updateCheckTimer)
  clearTimeout(saveTimer)
  clearTimeout(reconnectTimer)
  socket?.close()
})
</script>

<template>
  <!-- Pantalla de arranque -->
  <transition name="fade">
    <div v-if="bootstrapping && showSplash" class="boot-screen">
      <div class="boot-container">
        <svg viewBox="0 0 500 150" class="hello-svg">
          <text
            x="50%"
            y="50%"
            text-anchor="middle"
            dominant-baseline="middle"
            class="hello-text"
          >
            <tspan class="c1">T</tspan>
            <tspan class="c2">e</tspan>
            <tspan class="c3">l</tspan>
            <tspan class="c4">e</tspan>
            <tspan class="c5">g</tspan>
            <tspan class="c6">r</tspan>
            <tspan class="c7">a</tspan>
            <tspan class="c8">m</tspan>
            <tspan class="c9">D</tspan>
            <tspan class="c10">L</tspan>
            <tspan class="dots" dx="20" dy="8">
              <tspan class="dot1">●</tspan>
              <tspan class="dot2">●</tspan>
              <tspan class="dot3">●</tspan>
            </tspan>
          </text>
        </svg>
      </div>
    </div>
  </transition>

  <template v-if="!bootstrapping">
    <!-- Diálogo de actualización obligatoria -->
    <div v-if="updateInfo && isUpdateForced" class="update-required-overlay">
      <div class="update-card">
        <Zap :size="48" class="update-icon" />
        <h2>Actualización Obligatoria</h2>
        <p v-if="!isUpdating">
          Hay una nueva versión disponible ({{ updateInfo.latest }}). Es
          necesario actualizar para continuar.
        </p>

        <div class="update-action-area" :class="{ 'is-loading': isUpdating }">
          <button
            v-if="!isUpdating"
            class="primary-button update-btn"
            @click="installUpdate"
          >
            <span>Actualizar ahora</span>
            <ArrowUpRight :size="18" />
          </button>

          <div v-else class="update-progress-container">
            <div class="update-status-text">
              {{
                updateProgress.status === 'downloading'
                  ? 'Descargando actualización...'
                  : updateProgress.status === 'extracting'
                    ? 'Extrayendo archivos...'
                    : updateProgress.status === 'finishing'
                      ? 'Finalizando e iniciando...'
                      : 'Iniciando...'
              }}
            </div>
            <div class="update-progress-bar">
              <div
                class="update-progress-fill"
                :style="{ width: updateProgress.percentage + '%' }"
              >
                <div class="nitro-wind">
                  <span></span><span></span><span></span><span></span>
                </div>
              </div>
            </div>
            <div class="update-progress-stats">
              <span
                >{{ formatSize(updateProgress.downloaded) }} /
                {{ formatSize(updateProgress.total) }}</span
              >
              <span>{{ updateProgress.percentage }}%</span>
            </div>
          </div>
        </div>

        <div v-if="isUpdating" class="update-warning">
          Por favor, no cierres la aplicación.
        </div>

        <small>Versión actual: {{ updateInfo.current }}</small>
      </div>
    </div>

    <!-- Login remoto: token de acceso a la API (solo en navegador/remoto sin token guardado) -->
    <RemoteLogin
      v-if="needsRemoteLogin"
      :error="remoteLoginError"
      @submit="handleRemoteLogin"
    />

    <!-- Asistente de Autenticación -->
    <AuthWizard
      v-else-if="!authStatus.authenticated"
      :authStatus="authStatus"
      @auth-success="onAuthSuccess"
    />

    <!-- Estructura Principal de la Aplicación -->
    <div v-else class="app-shell">
      <aside class="sidebar" :class="{ 'mobile-open': mobileMenuOpen }">
        <div class="sidebar-main">
          <div class="brand">
            <span class="brand-mark"><img :src="logoUrl" alt="" /></span>
            <div class="brand-text">
              <svg viewBox="0 0 130 45" class="brand-svg">
                <!-- Nombre de la App animado letra a letra -->
                <text x="0" y="18" class="brand-logo-text">
                  <tspan class="b1">T</tspan>
                  <tspan class="b2">e</tspan>
                  <tspan class="b3">l</tspan>
                  <tspan class="b4">e</tspan>
                  <tspan class="b5">g</tspan>
                  <tspan class="b6">r</tspan>
                  <tspan class="b7">a</tspan>
                  <tspan class="b8">m</tspan>
                  <tspan class="brand-accent b9">D</tspan>
                  <tspan class="brand-accent b10">L</tspan>
                </text>
                <!-- Versión animada sincronizada con el texto de arriba -->
                <text x="0" y="38" class="brand-version-text" v-if="version">
                  <tspan
                    v-for="(char, i) in ('v' + version).split('')"
                    :key="i"
                    :class="'b' + (i + 1)"
                  >
                    {{ char }}
                  </tspan>
                </text>
              </svg>
            </div>
            <button
              class="mobile-menu-toggle"
              type="button"
              :aria-expanded="mobileMenuOpen"
              aria-label="Abrir menú"
              @click="mobileMenuOpen = !mobileMenuOpen"
            >
              <X v-if="mobileMenuOpen" :size="20" />
              <Menu v-else :size="20" />
            </button>
          </div>
          <p class="sidebar-copy">Centro de descargas personal</p>
          <nav class="sidebar-nav">
            <button
              :class="{ selected: activeView === 'downloads' }"
              @click="
                () => {
                  activeView = 'downloads'
                  mobileMenuOpen = false
                }
              "
            >
              <ArrowDownToLine :size="16" /> Descargas
            </button>
            <button
              :class="{ selected: activeView === 'listener' }"
              @click="
                () => {
                  activeView = 'listener'
                  mobileMenuOpen = false
                }
              "
            >
              <Radio :size="16" /> Escucha
            </button>
            <button
              :class="{ selected: activeView === 'logs' }"
              @click="
                () => {
                  activeView = 'logs'
                  mobileMenuOpen = false
                }
              "
            >
              <ScrollText :size="16" /> Logs
            </button>
            <button
              :class="{ selected: activeView === 'settings' }"
              @click="
                () => {
                  activeView = 'settings'
                  mobileMenuOpen = false
                }
              "
            >
              <Settings2 :size="16" /> Ajustes
            </button>
          </nav>

          <div v-if="authStatus.user" class="sidebar-user-badge">
            <div class="user-info">
              <UserCheck :size="14" class="user-icon" />
              <span class="user-name">{{ authStatus.user.first_name }}</span>
            </div>
            <button
              class="logout-btn"
              title="Cerrar sesión de Telegram"
              @click="logoutTelegram"
            >
              <LogOut :size="13" />
            </button>
          </div>

          <div
            class="sidebar-status"
            :class="{ 'is-disconnected': !websocketConnected }"
          >
            <span
              class="status-dot"
              :class="{ disconnected: !websocketConnected }"
            ></span>
            <span>{{
              websocketConnected
                ? 'Servicio conectado'
                : 'Servicio desconectado'
            }}</span>
          </div>
        </div>
        <div class="sidebar-bottom">
          <span class="mini-label">LÍMITE ACTUAL</span>
          <strong>{{ settings.max_concurrent_downloads }} descargas</strong>
          <span>{{ speedText }}</span>
        </div>
      </aside>

      <main class="main-content">
        <header class="topbar">
          <div>
            <span class="eyebrow">PANEL DE CONTROL</span>
            <h1>{{ viewTitle }}</h1>
          </div>
          <div class="topbar-meta">Velocidad total: {{ totalSpeed }}</div>
        </header>

        <div v-if="message" class="toast success">
          <CheckCircle2 :size="15" /> {{ message }}
        </div>
        <div v-if="error" class="toast danger">{{ error }}</div>

        <!-- Contenedor Modular de Vistas -->
        <div class="view-container">
          <DownloadsView
            v-show="activeView === 'downloads'"
            :downloads="downloads"
            :disk="disk"
            :settings="settings"
            :loading="loading"
            @start-download="startDownload"
            @pause-download="(id) => setDownloadPause(id, true)"
            @resume-download="(id) => setDownloadPause(id, false)"
            @cancel-download="cancelDownload"
            @retry-download="retryDownload"
            @delete-download="deleteDownload"
            @open-file="openFile"
            @pause-all="pauseAllDownloads"
            @resume-all="resumeAllDownloads"
            @cancel-all="cancelAllDownloads"
          />

          <ListenerView
            v-show="activeView === 'listener'"
            :notify="showMessage"
            :disk="disk"
            :initialItems="listenerItems"
            :settings="settings"
          />

          <LogsView
            v-show="activeView === 'logs'"
            :notify="showMessage"
            :logsSeq="logsSeq"
            :active="activeView === 'logs'"
          />

          <SettingsView
            v-show="activeView === 'settings'"
            :settings="settings"
            :saving="saving"
            :themeMap="themeMap"
            :api-token="token"
            :notify="showMessage"
            @save-settings="saveSettings"
            @clear-history="clearDownloadHistory"
            @reset-color="resetColor"
            @reset-loader-color="resetLoaderColor"
            @regenerate-token="regenerateToken"
          />
        </div>

        <ConfirmModal
          :show="modal.show"
          :title="modal.title"
          :message="modal.message"
          :confirmText="modal.confirmText"
          :cancelText="modal.cancelText"
          :type="modal.type"
          @confirm="handleConfirm"
          @cancel="handleCancel"
        />

        <footer>
          TelegramDL · Configuración persistida localmente en SQLite ·
          {{ host }}
        </footer>
      </main>
    </div>
  </template>
</template>

<style>
@font-face {
  font-family: 'LoaderFont';
  src:
    url('/fonts/loader.ttf') format('truetype'),
    url('/fonts/loader.otf') format('opentype');
  font-weight: normal;
  font-style: normal;
}

.boot-screen {
  position: fixed;
  inset: 0;
  background: var(--user-bg-base);
  background-image: radial-gradient(
    circle at 70% -10%,
    var(--user-bg-top) 0,
    var(--user-bg-base) 40%
  );
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 10000;
}
.boot-container {
  width: 100%;
  display: flex;
  justify-content: center;
  align-items: center;
}
.hello-svg {
  width: 90%;
  max-width: 600px;
  overflow: visible;
}
.hello-text {
  font-family: 'LoaderFont', cursive;
  font-weight: 700;
  font-size: 86px;
  filter: drop-shadow(0 0 25px var(--loader-glow));
}

.hello-text tspan:not(.dots):not(.dot1):not(.dot2):not(.dot3) {
  fill: var(--loader-primary);
  fill-opacity: 0;
  stroke: var(--loader-primary);
  stroke-width: 1.2;
  stroke-dasharray: 400;
  stroke-dashoffset: 400;
  animation: writeAndFill 1.8s cubic-bezier(0.445, 0.05, 0.55, 0.95) forwards;
}

/* Retrasos escalonados equilibrados para Dancing Script */
.c1 {
  animation-delay: 0.1s;
}
.c2 {
  animation-delay: 0.5s;
}
.c3 {
  animation-delay: 0.9s;
}
.c4 {
  animation-delay: 1.3s;
}
.c5 {
  animation-delay: 1.7s;
}
.c6 {
  animation-delay: 2.1s;
}
.c7 {
  animation-delay: 2.5s;
}
.c8 {
  animation-delay: 2.9s;
}
.c9 {
  animation-delay: 3.3s;
}
.c10 {
  animation-delay: 3.7s;
}

.dots {
  font-family: Arial, sans-serif;
  font-weight: bold;
  font-size: 24px;
  stroke: none !important;
  stroke-width: 0 !important;
  vertical-align: middle;
}

.dot1,
.dot2,
.dot3 {
  opacity: 0;
  fill: var(--loader-primary) !important;
  animation: dotFade 1.5s infinite;
}

.dot1 {
  animation-delay: 4.2s;
}
.dot2 {
  animation-delay: 4.4s;
}
.dot3 {
  animation-delay: 4.6s;
}

@keyframes dotFade {
  0%,
  100% {
    opacity: 0;
  }
  50% {
    opacity: 1;
  }
}

@keyframes writeAndFill {
  0% {
    stroke-dashoffset: 400;
    fill-opacity: 0;
  }
  40% {
    fill-opacity: 0.3;
  }
  100% {
    stroke-dashoffset: 0;
    fill-opacity: 1;
  }
}

@keyframes write {
  to {
    stroke-dashoffset: 0;
  }
}

@keyframes fillText {
  from {
    fill: transparent;
  }
  to {
    fill: var(--user-primary);
  }
}

.fade-enter-active,
.fade-leave-active {
  transition: opacity 0.8s ease;
}
.fade-enter-from,
.fade-leave-to {
  opacity: 0;
}

/* Animación del Logo en el Sidebar */
.brand-svg {
  width: 120px;
  height: 40px;
  overflow: visible;
  display: block;
}
.brand-logo-text {
  font-family: 'Space Grotesk', sans-serif;
  font-weight: 700;
  font-size: 19px;
  fill: #f3f8ff;
}
.brand-version-text {
  font-family: 'DM Sans', sans-serif;
  font-size: 11px;
  fill: var(--user-text-dim);
  font-weight: 500;
  letter-spacing: 0.05em;
}
.brand-logo-text .brand-accent {
  fill: var(--user-accent);
}
.brand-logo-text tspan,
.brand-version-text tspan {
  opacity: 0;
  fill-opacity: 0;
  stroke: #f3f8ff;
  stroke-width: 0.4;
  stroke-dasharray: 80;
  stroke-dashoffset: 80;
  display: inline-block;
  animation: brandSweepWritingLoop 8s ease-in-out infinite;
}
.brand-version-text tspan {
  stroke: var(--user-text-dim);
  fill: var(--user-text-dim);
}
.brand-logo-text .brand-accent {
  stroke: var(--user-accent);
}

/* Stagger compartido: la letra N del nombre y la letra N de la versión aparecen juntas */
.b1 {
  animation-delay: 1s;
}
.b2 {
  animation-delay: 1.12s;
}
.b3 {
  animation-delay: 1.24s;
}
.b4 {
  animation-delay: 1.36s;
}
.b5 {
  animation-delay: 1.48s;
}
.b6 {
  animation-delay: 1.6s;
}
.b7 {
  animation-delay: 1.72s;
}
.b8 {
  animation-delay: 1.84s;
}
.b9 {
  animation-delay: 1.96s;
}
.b10 {
  animation-delay: 2.08s;
}

@keyframes brandSweepWritingLoop {
  0% {
    opacity: 0;
    fill-opacity: 0;
    stroke-dashoffset: 80;
    transform: translateX(-15px);
  }
  25% {
    /* Entrada: se escribe y se llena mientras se desliza */
    opacity: 1;
    fill-opacity: 1;
    stroke-dashoffset: 0;
    transform: translateX(0);
  }
  75% {
    /* Pausa: texto fijo y sólido */
    opacity: 1;
    fill-opacity: 1;
    stroke-dashoffset: 0;
    transform: translateX(0);
  }
  90% {
    /* Salida: se desliza a la derecha y se desvanece */
    opacity: 0;
    fill-opacity: 0;
    transform: translateX(15px);
  }
  100% {
    opacity: 0;
  }
}

.update-required-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: color-mix(in srgb, var(--user-bg-base), transparent 5%);
  backdrop-filter: blur(8px);
  z-index: 9999;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
}
.update-card {
  background: var(--user-surface);
  border: 1px solid var(--user-border);
  border-radius: 24px;
  padding: 40px;
  max-width: 400px;
  text-align: center;
  box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.5);
}
.update-icon {
  color: var(--user-primary);
  margin-bottom: 20px;
}
.update-card h2 {
  color: #f8fafc;
  margin-bottom: 12px;
  font-size: 24px;
}
.update-card p {
  color: var(--user-text-dim);
  margin-bottom: 30px;
  line-height: 1.6;
}
.update-action-area {
  transition: all 0.6s cubic-bezier(0.34, 1.56, 0.64, 1);
  min-height: 80px;
  display: flex;
  flex-direction: column;
  justify-content: center;
  position: relative;
}
.update-action-area.is-loading {
  transform: translateY(-5px);
}
.update-progress-container {
  width: 100%;
  text-align: left;
  animation: morphReveal 0.8s cubic-bezier(0.19, 1, 0.22, 1);
}
.update-status-text {
  color: #f8fafc;
  font-size: 14px;
  margin-bottom: 12px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
}
.update-progress-bar {
  height: 16px;
  background: var(--user-bg-base);
  border-radius: 20px;
  overflow: hidden;
  margin-bottom: 10px;
  position: relative;
  border: 1px solid var(--user-border);
  box-shadow: inset 0 2px 8px rgba(0, 0, 0, 0.5);
}
.update-progress-fill {
  height: 100%;
  background: linear-gradient(
    90deg,
    var(--user-bg-top),
    var(--user-primary),
    var(--user-accent)
  );
  transition: width 0.4s cubic-bezier(0.1, 0.7, 0.1, 1);
  position: relative;
  display: flex;
  align-items: center;
  justify-content: flex-end;
}

/* Efecto Nitro-Wind */
.nitro-wind {
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  overflow: hidden;
  pointer-events: none;
}
.nitro-wind span {
  position: absolute;
  background: linear-gradient(
    90deg,
    transparent,
    rgba(255, 255, 255, 0.6),
    transparent
  );
  height: 2px;
  border-radius: 2px;
  animation: windBackwards 0.6s linear infinite;
}
.nitro-wind span:nth-child(1) {
  top: 25%;
  width: 40px;
  animation-duration: 0.4s;
}
.nitro-wind span:nth-child(2) {
  top: 50%;
  width: 60px;
  animation-duration: 0.7s;
  animation-delay: 0.1s;
}
.nitro-wind span:nth-child(3) {
  top: 75%;
  width: 30px;
  animation-duration: 0.5s;
  animation-delay: 0.2s;
}
.nitro-wind span:nth-child(4) {
  top: 40%;
  width: 50px;
  animation-duration: 0.8s;
  animation-delay: 0.3s;
}

.update-progress-stats {
  display: flex;
  justify-content: space-between;
  color: var(--user-text-dim);
  font-size: 12px;
  font-family: 'Space Grotesk', monospace;
  font-weight: 600;
}
.update-warning {
  color: #f87171;
  font-size: 13px;
  margin-top: 15px;
  font-weight: 500;
  text-align: center;
  animation: pulseWarning 2s infinite;
}

.update-btn {
  width: fit-content !important;
  min-width: 240px !important;
  height: 60px !important;
  padding: 0 40px !important;
  font-size: 17px !important;
  font-weight: 700 !important;
  margin: 0 auto !important;
  display: flex !important;
  align-items: center !important;
  justify-content: center !important;
  border-radius: 30px !important;
  gap: 12px !important;
  background: linear-gradient(
    135deg,
    var(--user-primary) 0%,
    var(--user-bg-top) 100%
  ) !important;
  color: white !important;
  border: none !important;
  cursor: pointer !important;
  transition: all 0.4s cubic-bezier(0.175, 0.885, 0.32, 1.275) !important;
}
.update-btn:hover {
  transform: scale(1.05) translateY(-3px) !important;
  box-shadow: 0 15px 30px var(--user-glow) !important;
}
.update-btn:active {
  transform: scale(0.98) !important;
}

@keyframes windBackwards {
  from {
    transform: translateX(300px);
    opacity: 0;
  }
  50% {
    opacity: 1;
  }
  to {
    transform: translateX(-100px);
    opacity: 0;
  }
}
@keyframes morphReveal {
  from {
    opacity: 0;
    transform: scaleX(0.5);
    filter: blur(5px);
  }
  to {
    opacity: 1;
    transform: scaleX(1);
    filter: blur(0);
  }
}
.update-card small {
  color: var(--user-text-dim);
  opacity: 0.8;
  display: block;
}

@keyframes nitro {
  from {
    transform: translateX(-100%);
  }
  to {
    transform: translateX(100%);
  }
}
@keyframes morphIn {
  from {
    opacity: 0;
    transform: translateY(10px) scale(0.95);
  }
  to {
    opacity: 1;
    transform: translateY(0) scale(1);
  }
}
@keyframes pulseWarning {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.6;
  }
}
.sidebar-nav {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-bottom: 24px;
}
.sidebar-user-badge {
  display: flex;
  align-items: center;
  justify-content: space-between;
  background: var(--user-bg-base);
  border: 1px solid var(--user-border);
  border-radius: 10px;
  padding: 8px 10px;
  margin-bottom: 14px;
  font-size: 12px;
  color: #dbe7f5;
}
.sidebar-user-badge .user-info {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  overflow: hidden;
}
.sidebar-user-badge .user-icon {
  color: #39db9a;
  flex: none;
}
.sidebar-user-badge .user-name {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  font-weight: 600;
}
.sidebar-user-badge .logout-btn {
  background: transparent;
  border: 0;
  color: #e88888;
  cursor: pointer;
  display: grid;
  place-items: center;
  padding: 4px;
  border-radius: 6px;
  flex: none;
}
.sidebar-user-badge .logout-btn:hover {
  background: #3d171d;
  color: #ff9e9e;
}

/* Estilos para Ajustes y Selector de Color */
.content-grid-single {
  display: grid;
  grid-template-columns: 1fr;
  gap: 18px;
  animation: riseIn 0.55s ease both;
}
.settings-panel-full {
  padding: 30px;
}
.settings-group {
  display: flex;
  flex-direction: column;
  gap: 15px;
}
.settings-actions {
  display: flex;
  gap: 12px;
  margin-top: 22px;
}
.settings-actions .clear-history-button,
.settings-actions .save-button {
  width: 100%;
  margin-top: 0;
  flex: 1;
}
.clear-history-button {
  width: 100%;
  margin-top: 22px;
  padding: 11px;
  border: 1px solid #6e3942;
  border-radius: 10px;
  background: rgba(125, 48, 61, 0.16);
  color: #ffadb5;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  font-size: 12px;
}
.clear-history-button:hover {
  background: rgba(125, 48, 61, 0.3);
  border-color: #a95663;
}
/* Los dos bloques de color (acento y loader) van ahora en columnas dentro de
   la seccion plegable de temas, asi que ya no necesitan abarcar dos filas. */
.color-group {
  padding-top: 5px;
}
/* El grupo de colores lleva sus propios subtítulos, así que el título del
   bloque no necesita el hueco de abajo que usan los demás ajustes. */
.color-group .setting-label {
  margin-bottom: 0;
}
.color-selector-container {
  display: flex;
  flex-direction: column;
  gap: 12px;
  margin: 0;
}
.color-row {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
}
.color-dot {
  width: 28px;
  height: 28px;
  border-radius: 50%;
  border: none;
  cursor: pointer;
  padding: 0;
  transition: all 0.2s cubic-bezier(0.175, 0.885, 0.32, 1.275);
  position: relative;
  overflow: hidden;
}
.color-dot:hover {
  transform: scale(1.15);
}
.color-dot.active {
  transform: scale(1.1);
  box-shadow:
    0 0 0 3px var(--user-bg-base),
    0 0 0 5px var(--user-primary),
    0 0 15px var(--user-glow);
  z-index: 2;
}
.gradient-dot {
  position: relative;
}

/* Punto de un tema del panel (ids 16+) en la fila del loader. Lleva el mismo
   aro interior que la muestra de los botones de tema, que es lo que hace que
   el degradado se lea como una moneda y no como una mancha. Va después de
   .color-dot.active para poder sumar el aro al resalte del seleccionado. */
.tema-dot {
  box-shadow: inset 0 0 0 1px rgba(255, 255, 255, 0.12);
}
.tema-dot.active {
  box-shadow:
    inset 0 0 0 1px rgba(255, 255, 255, 0.12),
    0 0 0 3px var(--user-bg-base),
    0 0 0 5px var(--user-primary),
    0 0 15px var(--user-glow);
}

/* Separa un bloque entero de ajustes del anterior (el color del loader del
   color de acento). */
.seccion-separada {
  margin-top: 24px;
  border-top: 1px solid var(--user-border);
  padding-top: 18px;
}

/* Título de cada grupo de colores dentro de un bloque: "Telegram" para los
   0-15 y "Colores temáticos" para los propios del panel. Es una etiqueta de
   procedencia, más discreta que el título del bloque. El margen de abajo es
   negativo para recortar parte del gap de 15px del grupo: el subtítulo tiene
   que quedar pegado a SUS colores, no a mitad de camino entre los dos bloques. */
.grupo-color-titulo {
  display: block;
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.05em;
  text-transform: uppercase;
  color: var(--user-text-dim);
  margin: 4px 0 -9px;
}
.temas-lista {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  margin: 0;
}
.tema-chip {
  display: flex;
  align-items: center;
  gap: 9px;
  padding: 7px 13px 7px 8px;
  border-radius: 999px;
  border: 1px solid var(--user-border);
  background: var(--user-surface-light);
  color: var(--user-text-dim);
  font: 600 12px 'DM Sans';
  cursor: pointer;
  transition: all 0.2s cubic-bezier(0.175, 0.885, 0.32, 1.275);
}
.tema-chip:hover {
  transform: translateY(-2px);
  border-color: var(--user-border-light);
  color: #eef7ff;
}
.tema-chip.active {
  color: #eef7ff;
  border-color: var(--user-primary);
  box-shadow:
    0 0 0 1px var(--user-primary),
    0 6px 18px var(--user-glow);
}
.tema-muestra {
  width: 22px;
  height: 22px;
  border-radius: 50%;
  flex: none;
  box-shadow: inset 0 0 0 1px rgba(255, 255, 255, 0.12);
}
.reset-button-alt {
  background: var(--user-surface-light);
  border: 1px solid var(--user-border);
  color: var(--user-text-dim);
  padding: 10px 16px;
  border-radius: 10px;
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
  display: flex;
  align-items: center;
  gap: 8px;
  width: fit-content;
  transition: all 0.2s;
}
.reset-button-alt:hover {
  background: var(--user-icon-bg);
  color: var(--user-accent);
  border-color: var(--user-primary);
}

.action-btn-mini {
  background: var(--user-surface-light);
  border: 1px solid var(--user-border);
  color: var(--user-text-dim);
  padding: 6px 10px;
  border-radius: 8px;
  font-size: 11px;
  font-weight: 600;
  cursor: pointer;
  display: flex;
  align-items: center;
  gap: 6px;
  transition: all 0.2s;
}
.action-btn-mini:hover {
  background: var(--user-icon-bg);
  color: var(--user-accent);
  border-color: var(--user-primary);
}
.action-btn-mini.danger:hover {
  background: rgba(125, 48, 61, 0.3);
  border-color: #a95663;
  color: #ffadb5;
}

/* --- Ajustes plegables ---------------------------------------------------- */
/* Cada apartado de Ajustes es un <section> con su cabecera-botón. El cuerpo va
   con v-show, no con v-if: se oculta sin desmontar nada, así abrir y cerrar no
   vuelve a montar los selectores de color (que son muchos botones). */
.ajustes-acordeon {
  display: flex;
  flex-direction: column;
  gap: 12px;
  margin-top: 4px;
}
.ajuste-bloque {
  border: 1px solid var(--user-border);
  border-radius: 14px;
  background: var(--user-bg-base);
  overflow: hidden;
  transition: border-color 0.2s;
}
.ajuste-bloque.abierto {
  border-color: var(--user-border-light);
}
.ajuste-cabecera {
  display: flex;
  align-items: center;
  gap: 12px;
  width: 100%;
  padding: 14px 16px;
  background: transparent;
  border: 0;
  cursor: pointer;
  text-align: left;
  color: #dbe7f5;
  transition: background 0.2s;
}
.ajuste-cabecera:hover {
  background: var(--user-surface-light);
}
.ajuste-cabecera:focus-visible {
  outline: 2px solid var(--user-accent);
  outline-offset: -2px;
}
.ajuste-icono {
  display: grid;
  place-items: center;
  width: 30px;
  height: 30px;
  border-radius: 9px;
  background: var(--user-icon-bg);
  color: var(--user-primary);
  flex: none;
}
.ajuste-titulo {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
  flex: 1;
}
.ajuste-titulo strong {
  font:
    600 13px 'Space Grotesk',
    sans-serif;
}
.ajuste-titulo small {
  font-size: 11px;
  color: var(--user-text-dim);
}
.ajuste-flecha {
  color: var(--user-text-dim);
  flex: none;
  transition:
    transform 0.2s ease,
    color 0.2s;
}
.ajuste-bloque.abierto .ajuste-flecha {
  transform: rotate(90deg);
  color: var(--user-accent);
}
.ajuste-cuerpo {
  padding: 16px 16px 20px;
  border-top: 1px solid var(--user-border);
  animation: ajusteAbrir 0.18s ease both;
}
/* Mismo reparto en columnas que tenía el bloque entero, pero ahora dentro de
   cada apartado: dos columnas si hay sitio, una sola si no. */
.ajuste-columnas {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(290px, 1fr));
  gap: 34px;
  align-items: start;
}
@keyframes ajusteAbrir {
  from {
    opacity: 0;
    transform: translateY(-4px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}

/* Fila de exportar / importar / limpiar historial: botones del ancho de su
   texto, no estirados como estaban cuando eran las acciones del panel. */
.acciones-datos {
  margin-top: 0;
  flex-wrap: wrap;
}
.acciones-datos .clear-history-button {
  width: auto;
  flex: 0 0 auto;
  margin-top: 0;
  padding: 10px 16px;
  font-weight: 600;
}

/* Botón de guardar: círculo flotante abajo a la derecha, solo con el icono.
   Los ajustes se guardan solos de todas formas; esto es para forzarlo. Conserva
   la clase .save-button para que los temas lo sigan pintando como suyo. */
.save-button.save-fab {
  position: fixed;
  right: 26px;
  bottom: 26px;
  width: 52px;
  height: 52px;
  padding: 0;
  margin: 0;
  gap: 0;
  border-radius: 50%;
  display: grid;
  place-items: center;
  z-index: 90;
  box-shadow: 0 10px 26px var(--user-glow);
}
.save-button.save-fab:hover:not(:disabled) {
  transform: translateY(-2px) scale(1.05);
}
.save-button.save-fab:active:not(:disabled) {
  transform: translateY(0) scale(1);
}
/* Hueco para que el botón flotante no tape el final del panel. */
.settings-view {
  padding-bottom: 78px;
}

@media (max-width: 720px) {
  .ajuste-cabecera {
    padding: 12px;
    gap: 10px;
  }
  .ajuste-cuerpo {
    padding: 14px 12px 16px;
  }
  .ajuste-columnas {
    gap: 24px;
  }
  .save-button.save-fab {
    right: 16px;
    bottom: 16px;
    width: 48px;
    height: 48px;
  }
}
</style>
