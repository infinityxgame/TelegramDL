<script setup>
import { ref, computed, onMounted } from 'vue'
import {
  Settings2,
  Zap,
  Trash2,
  Copy,
  Eye,
  EyeOff,
  RefreshCw,
  Download,
  Upload,
  ArrowDownToLine,
  Image as Palette,
  KeyRound,
  HardDrive,
  Languages,
  Bell,
  Send,
  AlertCircle,
  Radio,
  CheckCircle2,
  Loader2,
  Plus,
  Info,
  Share2,
  Save,
  X
} from '../icons'
import FolderPicker from '../components/FolderPicker.vue'
import { useAuthToken } from '../composables/useAuthToken'
import { useExternalLink } from '../composables/useExternalLink'
import { useI18n } from '../i18n'
import AppAccordion from '../components/AppAccordion.vue'
import { isLoopbackHost } from '../utils/loopback.js'

const { authHeaders } = useAuthToken()
const { openExternal } = useExternalLink()
const { t, locale, availableLocales, splitOn } = useI18n()

// La descripción del acceso remoto menciona Tailscale con enlace; se parte por
// el marcador {vpn} para mantener el <a> entre las dos mitades traducidas.
const remoteDescParts = computed(() => splitOn('settings.remoteDesc', 'vpn'))
// La descripción de notificaciones menciona @BotFather con enlace; se parte por
// el marcador {botfather} para mantener el <a> entre las dos mitades traducidas.
const notifDescParts = computed(() =>
  splitOn('settings.notificationsDesc', 'botfather')
)

const props = defineProps({
  settings: {
    type: Object,
    required: true
  },
  saving: {
    type: Boolean,
    default: false
  },
  themeMap: {
    type: Object,
    required: true
  },
  apiToken: {
    type: String,
    default: ''
  },
  notify: {
    type: Function,
    default: () => {}
  }
})

const emit = defineEmits([
  'save-settings',
  'update:settings',
  'clear-history',
  'reset-color',
  'reset-loader-color',
  'regenerate-token'
])

// Los ajustes pertenecen al padre (App.vue): aquí nunca se muta la prop
// `settings` directamente. Cada control emite el cambio con patch() y el
// padre lo aplica, lo que dispara su autoguardado como antes.
const patch = (partial) => emit('update:settings', partial)

// Apartado de Ajustes abierto. Solo puede haber uno: abrir un acordeón cierra
// el que ya estaba, como hacía el menú antiguo antes del componente.
const openSection = ref('')

// Aviso de exposición (SEC-05): si el servidor escucha en una dirección que no
// es de loopback, el panel queda expuesto a la red local sin cifrar. Se consulta
// una única vez al montar la vista (sin sondeos ni watchers) y, si el servidor
// no contesta, simplemente no se muestra el aviso.
const serverInfo = ref(null)
const networkExposed = computed(
  () => serverInfo.value !== null && !isLoopbackHost(serverInfo.value.host)
)

onMounted(async () => {
  fetchSavedProxies()
  try {
    // Mismo patrón de petición que listenerApi en esta vista: fetch con
    // authHeaders() y parseo de JSON, lanzando Error si la respuesta no es OK.
    const response = await fetch('/api/system/info', {
      headers: { ...authHeaders() }
    })
    const data = await response.json().catch(() => ({}))
    if (!response.ok) {
      throw new Error(data.detail || data.error || t('common.serverError'))
    }
    serverInfo.value = data
  } catch (err) {
    // Regla 5: nunca en silencio. Sin datos no se pinta el aviso, pero el
    // fallo queda registrado para poder diagnosticarlo.
    console.error('Could not load /api/system/info:', err)
  }
})

// --- Formulario de añadir nuevo proxy e importación por enlace ---
const proxyLinkInput = ref('')
const testingLinkProxy = ref(false)
const linkTestResult = ref(null)

const newProxyForm = ref({
  proxy_type: 'socks5',
  proxy_host: '',
  proxy_port: 1080,
  proxy_username: '',
  proxy_password: '',
  proxy_secret: ''
})
const testingNewProxy = ref(false)
const newProxyTestResult = ref(null)

const parseProxyLinkPayload = (rawLink) => {
  const raw = String(rawLink || '').trim()
  if (!raw) return null

  try {
    const urlStr = raw
      .replace(/^telegramdl:\/\//i, 'http://')
      .replace(/^tg:\/\//i, 'http://')
      .replace(/^https:\/\/t\.me\//i, 'http://')
      .replace(/^https:\/\/telegram\.me\//i, 'http://')
    const url = new URL(urlStr)
    const hostName = url.hostname || url.host
    const pathName = url.pathname || ''

    let pType = 'socks5'
    if (hostName === 'socks' || raw.toLowerCase().includes('socks')) {
      pType = 'socks5'
    } else if (
      hostName === 'mtproto' ||
      hostName === 'proxy' ||
      pathName.includes('proxy') ||
      raw.toLowerCase().includes('mtproto')
    ) {
      pType = 'mtproto'
    } else if (
      hostName === 'web' ||
      hostName === 'http' ||
      raw.toLowerCase().includes('web') ||
      raw.toLowerCase().includes('http')
    ) {
      pType = 'http'
    }

    const params = new URLSearchParams(url.search)
    const server = params.get('server') || params.get('host') || ''
    const port = Number(params.get('port')) || 1080
    const user = params.get('user') || params.get('username') || ''
    const pass = params.get('pass') || params.get('password') || ''
    const secret = params.get('secret') || ''

    if (!server) return null

    return {
      proxy_type: pType,
      proxy_host: server,
      proxy_port: port,
      proxy_username: user,
      proxy_password: pass,
      proxy_secret: secret
    }
  } catch {
    return null
  }
}

const addProxyFromLink = async () => {
  const payload = parseProxyLinkPayload(proxyLinkInput.value)
  if (!payload) return
  testingLinkProxy.value = true
  linkTestResult.value = null

  try {
    // 1. Comprobar disponibilidad del proxy por enlace
    const testRes = await fetch('/api/proxy/test', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...authHeaders()
      },
      body: JSON.stringify(payload)
    })
    const testData = await testRes.json()

    if (testData.status === 'ok' && testData.active) {
      linkTestResult.value = {
        active: true,
        ping_ms: testData.ping_ms
      }

      // 2. Si está disponible, guardarlo en la lista en DB
      const res = await fetch('/api/proxies', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          ...authHeaders()
        },
        body: JSON.stringify(payload)
      })
      const data = await res.json()
      if (data.status === 'ok') {
        await fetchSavedProxies()
        proxyLinkInput.value = ''
      }
    } else {
      linkTestResult.value = {
        active: false,
        ping_ms: 0,
        error: testData.error || t('settings.proxyNoResponse')
      }
    }
  } catch (err) {
    linkTestResult.value = {
      active: false,
      ping_ms: 0,
      error: err.message || t('settings.proxyNoResponse')
    }
  } finally {
    testingLinkProxy.value = false
  }
}

// --- Gestión Multi-Proxy ---
const savedProxies = ref([])
const proxyPings = ref({})
const editingProxyModal = ref(false)
const editingProxyForm = ref({
  id: 0,
  proxy_type: 'socks5',
  proxy_host: '',
  proxy_port: 1080,
  proxy_username: '',
  proxy_password: '',
  proxy_secret: ''
})
const shareNotice = ref('')

const fetchSavedProxies = async () => {
  try {
    const res = await fetch('/api/proxies', {
      headers: { ...authHeaders() }
    })
    const data = await res.json()
    if (data.status === 'ok') {
      savedProxies.value = data.proxies || []
      testAllSavedProxies()
    }
  } catch (err) {
    console.error('Error fetching saved proxies:', err)
  }
}

const testAllSavedProxies = () => {
  for (const p of savedProxies.value) {
    proxyPings.value[p.id] = {
      testing: true,
      active: false,
      ping_ms: 0,
      error: ''
    }
    testSingleSavedProxy(p)
  }
}

const testSingleSavedProxy = async (p) => {
  try {
    const res = await fetch('/api/proxy/test', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...authHeaders()
      },
      body: JSON.stringify({
        proxy_type: p.proxy_type || 'socks5',
        proxy_host: p.proxy_host,
        proxy_port: Number(p.proxy_port),
        proxy_username: p.proxy_username || '',
        proxy_password: p.proxy_password || '',
        proxy_secret: p.proxy_secret || ''
      })
    })
    const data = await res.json()
    if (data.status === 'ok') {
      proxyPings.value[p.id] = {
        testing: false,
        active: data.active,
        ping_ms: data.ping_ms,
        error: data.error || ''
      }
    } else {
      proxyPings.value[p.id] = {
        testing: false,
        active: false,
        ping_ms: 0,
        error: data.error || t('settings.proxyNoResponse')
      }
    }
  } catch (err) {
    proxyPings.value[p.id] = {
      testing: false,
      active: false,
      ping_ms: 0,
      error: err.message || t('settings.proxyNoResponse')
    }
  }
}

const formatProxyError = (rawError) => {
  if (!rawError) return t('settings.proxyNoResponse')
  const lower = String(rawError).toLowerCase()
  if (lower.includes('timeout') || lower.includes('deadline')) {
    return t('settings.errTimeout')
  }
  if (lower.includes('refused')) {
    return t('settings.errRefused')
  }
  if (lower.includes('lookup') || lower.includes('no such host')) {
    return t('settings.errHostNotFound')
  }
  if (lower.includes('not supported') || lower.includes('unsupported')) {
    return t('settings.errUnsupported')
  }
  if (
    lower.includes('auth') ||
    lower.includes('407') ||
    lower.includes('user')
  ) {
    return t('settings.errAuth')
  }
  return t('settings.proxyNoResponse')
}

const proxyPingText = (p) => {
  const info = proxyPings.value[p.id]
  if (!info || info.testing) return t('settings.proxyTesting')
  if (info.active) return t('settings.proxyPingMs', { ping: info.ping_ms })
  return formatProxyError(info.error)
}

const proxyPingClass = (p) => {
  const info = proxyPings.value[p.id]
  if (!info || info.testing) return 'testing'
  if (info.active) return 'active'
  return 'error'
}

const isProxySelected = (p) => {
  return (
    props.settings.proxy_enabled &&
    props.settings.proxy_host === p.proxy_host &&
    Number(props.settings.proxy_port) === Number(p.proxy_port) &&
    (props.settings.proxy_type || 'socks5') === (p.proxy_type || 'socks5')
  )
}

const selectProxy = (p) => {
  patch({
    proxy_enabled: true,
    proxy_type: p.proxy_type || 'socks5',
    proxy_host: p.proxy_host,
    proxy_port: Number(p.proxy_port),
    proxy_username: p.proxy_username || '',
    proxy_password: p.proxy_password || '',
    proxy_secret: p.proxy_secret || '',
    proxy_notice: ''
  })
  emit('save-settings')
}

const addCurrentProxy = async () => {
  if (!newProxyForm.value.proxy_host || !newProxyForm.value.proxy_port) return
  testingNewProxy.value = true
  newProxyTestResult.value = null

  const payload = {
    proxy_type: newProxyForm.value.proxy_type || 'socks5',
    proxy_host: newProxyForm.value.proxy_host.trim(),
    proxy_port: Number(newProxyForm.value.proxy_port) || 1080,
    proxy_username: newProxyForm.value.proxy_username || '',
    proxy_password: newProxyForm.value.proxy_password || '',
    proxy_secret: newProxyForm.value.proxy_secret || ''
  }

  try {
    // 1. Comprobar disponibilidad del proxy antes de agregarlo
    const testRes = await fetch('/api/proxy/test', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...authHeaders()
      },
      body: JSON.stringify(payload)
    })
    const testData = await testRes.json()

    if (testData.status === 'ok' && testData.active) {
      newProxyTestResult.value = {
        active: true,
        ping_ms: testData.ping_ms
      }

      // 2. Si está disponible, agregarlo a la lista en DB
      const res = await fetch('/api/proxies', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          ...authHeaders()
        },
        body: JSON.stringify(payload)
      })
      const data = await res.json()
      if (data.status === 'ok') {
        await fetchSavedProxies()
        // Limpiar el formulario para agregar otro proxy
        newProxyForm.value = {
          proxy_type: newProxyForm.value.proxy_type,
          proxy_host: '',
          proxy_port: 1080,
          proxy_username: '',
          proxy_password: '',
          proxy_secret: ''
        }
      }
    } else {
      // Si dio error o no respondió, mostrar el aviso y NO agregarlo
      newProxyTestResult.value = {
        active: false,
        ping_ms: 0,
        error: testData.error || t('settings.proxyNoResponse')
      }
    }
  } catch (err) {
    newProxyTestResult.value = {
      active: false,
      ping_ms: 0,
      error: err.message || t('settings.proxyNoResponse')
    }
  } finally {
    testingNewProxy.value = false
  }
}

const openEditModal = (p) => {
  editingProxyForm.value = { ...p }
  shareNotice.value = ''
  editingProxyModal.value = true
}

const saveEditingProxy = async () => {
  const p = editingProxyForm.value
  if (!p.proxy_host || !p.proxy_port) return
  try {
    const res = await fetch('/api/proxies', {
      method: 'PUT',
      headers: {
        'Content-Type': 'application/json',
        ...authHeaders()
      },
      body: JSON.stringify(p)
    })
    const data = await res.json()
    if (data.status === 'ok') {
      editingProxyModal.value = false
      if (isProxySelected(p)) {
        patch({
          proxy_type: p.proxy_type || 'socks5',
          proxy_host: p.proxy_host,
          proxy_port: Number(p.proxy_port),
          proxy_username: p.proxy_username || '',
          proxy_password: p.proxy_password || '',
          proxy_secret: p.proxy_secret || '',
          proxy_notice: ''
        })
        emit('save-settings')
      }
      await fetchSavedProxies()
    }
  } catch (err) {
    console.error('Error updating proxy:', err)
  }
}

const deleteEditingProxy = async () => {
  const p = editingProxyForm.value
  if (!p.id) return
  const wasSelected = isProxySelected(p)
  try {
    const res = await fetch(`/api/proxies?id=${p.id}`, {
      method: 'DELETE',
      headers: { ...authHeaders() }
    })
    const data = await res.json()
    if (data.status === 'ok') {
      editingProxyModal.value = false
      if (wasSelected || savedProxies.value.length <= 1) {
        patch({
          proxy_enabled: false,
          proxy_host: '',
          proxy_port: 1080,
          proxy_username: '',
          proxy_password: '',
          proxy_secret: '',
          proxy_notice: ''
        })
        emit('save-settings')
      }
      await fetchSavedProxies()
    }
  } catch (err) {
    console.error('Error deleting proxy:', err)
  }
}

const shareEditingProxy = async () => {
  const p = editingProxyForm.value
  const host = p.proxy_host || ''
  const port = p.proxy_port || 1080
  const type = p.proxy_type || 'socks5'
  let url = ''

  if (type === 'socks5') {
    url = `telegramdl://socks?server=${encodeURIComponent(host)}&port=${port}`
    if (p.proxy_username) url += `&user=${encodeURIComponent(p.proxy_username)}`
    if (p.proxy_password) url += `&pass=${encodeURIComponent(p.proxy_password)}`
  } else if (type === 'mtproto') {
    url = `telegramdl://mtproto?server=${encodeURIComponent(host)}&port=${port}`
    if (p.proxy_secret) url += `&secret=${encodeURIComponent(p.proxy_secret)}`
  } else if (type === 'http') {
    url = `telegramdl://web?server=${encodeURIComponent(host)}&port=${port}`
    if (p.proxy_username) url += `&user=${encodeURIComponent(p.proxy_username)}`
    if (p.proxy_password) url += `&pass=${encodeURIComponent(p.proxy_password)}`
  }

  try {
    await navigator.clipboard.writeText(url)
    shareNotice.value = t('settings.proxyCopied')
    setTimeout(() => {
      shareNotice.value = ''
    }, 3000)
  } catch (err) {
    console.error('Could not copy proxy link:', err)
  }
}

const showToken = ref(false)
// Estado del botón de copiar ('' = normal, 'ok', 'fail'): la etiqueta se deriva
// así sigue la traducción si el idioma cambia mientras el aviso está en pantalla.
const copyState = ref('')
const copyLabel = computed(() =>
  copyState.value === 'ok'
    ? t('settings.copied')
    : copyState.value === 'fail'
      ? t('settings.copyFail')
      : t('settings.copy')
)
const maskedToken = computed(() =>
  props.apiToken ? '•'.repeat(Math.min(props.apiToken.length, 40)) : ''
)

// Los colores 0-15 son los de Telegram Premium (el panel toma el de la cuenta
// al iniciar sesión). Del 16 en adelante son temas propios del panel, que
// además del color traen decorado. Se sacan del propio themeMap en vez de una
// lista fija, para que añadir uno nuevo no obligue a tocar esta vista.
const specialThemes = computed(() =>
  Object.keys(props.themeMap)
    .map(Number)
    .filter((id) => id >= 16)
    .sort((a, b) => a - b)
)

const copyToken = async () => {
  if (!props.apiToken) return
  try {
    await navigator.clipboard.writeText(props.apiToken)
    copyState.value = 'ok'
  } catch {
    copyState.value = 'fail'
  }
  setTimeout(() => {
    copyState.value = ''
  }, 2000)
}

// Exportar / importar la configuración de escucha (los chats del listener con
// sus filtros). Trabaja contra /api/listener/settings, el mismo endpoint que
// usa la vista de Escucha, así que no hace falta nada nuevo en el backend.
const EXPORT_FORMAT = 'tgdown-listener'
const EXPORT_VERSION = 1
const importing = ref(false)
const exporting = ref(false)
const importInput = ref(null)

const listenerApi = async (options = {}) => {
  const response = await fetch('/api/listener/settings', {
    ...options,
    headers: { ...(options.headers || {}), ...authHeaders() }
  })
  const data = await response.json().catch(() => ({}))
  if (!response.ok) {
    throw new Error(data.detail || data.error || t('common.serverError'))
  }
  return data
}

const normalizeChat = (raw) => {
  if (!raw || typeof raw !== 'object') return null
  const id = Number(raw.id ?? raw.chat_id)
  if (!Number.isInteger(id) || id === 0) return null
  const flag = (value) =>
    value === undefined || value === null ? true : !!value
  return {
    id,
    name: String(raw.name ?? id),
    auto_download: !!raw.auto_download,
    listen_self: !!raw.listen_self,
    f_photos: flag(raw.f_photos),
    f_videos: flag(raw.f_videos),
    f_audios: flag(raw.f_audios),
    f_docs: flag(raw.f_docs),
    f_stickers: flag(raw.f_stickers),
    name_mode: raw.name_mode || 'manual'
  }
}

const triggerBlobDownload = (content, filename, mime) => {
  const blob = new Blob([content], { type: mime })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

const exportListener = async () => {
  if (exporting.value) return
  exporting.value = true
  try {
    const data = await listenerApi()
    const chats = (data.chats || []).map(normalizeChat).filter(Boolean)
    if (!chats.length) {
      props.notify(t('settings.listenerExportEmpty'), true)
      return
    }
    const payload = {
      format: EXPORT_FORMAT,
      version: EXPORT_VERSION,
      exported_at: new Date().toISOString(),
      listener_enabled: !!data.enabled,
      chats
    }
    const stamp = new Date().toISOString().slice(0, 19).replace(/[:T]/g, '-')
    triggerBlobDownload(
      JSON.stringify(payload, null, 2),
      `tgdown-listener-${stamp}.json`,
      'application/json'
    )
    props.notify(
      chats.length === 1
        ? t('settings.listenerExportedOne')
        : t('settings.listenerExported', { n: chats.length })
    )
  } catch (err) {
    props.notify(err.message || t('settings.listenerExportFail'), true)
  } finally {
    exporting.value = false
  }
}

const pickImportFile = () => {
  if (importing.value) return
  importInput.value?.click()
}

// El import es aditivo: conserva los chats que ya están configurados y añade o
// actualiza los del archivo (gana el archivo si el mismo chat_id ya existía).
const onImportFile = async (event) => {
  const file = event.target.files?.[0]
  event.target.value = ''
  if (!file || importing.value) return
  importing.value = true
  try {
    const text = await file.text()
    let parsed
    try {
      parsed = JSON.parse(text)
    } catch {
      throw new Error(t('settings.importInvalidJson'))
    }

    const rawChats = Array.isArray(parsed)
      ? parsed
      : parsed?.chats || parsed?.listener_chats
    if (!Array.isArray(rawChats)) {
      throw new Error(t('settings.importNoChats'))
    }

    const incoming = rawChats.map(normalizeChat).filter(Boolean)
    if (!incoming.length) {
      throw new Error(t('settings.importNoValid'))
    }

    const current = await listenerApi()
    const merged = new Map()
    for (const chat of (current.chats || [])
      .map(normalizeChat)
      .filter(Boolean)) {
      merged.set(chat.id, chat)
    }
    let added = 0
    let updated = 0
    for (const chat of incoming) {
      if (merged.has(chat.id)) updated++
      else added++
      merged.set(chat.id, chat)
    }

    const body = {
      enabled: !!current.enabled,
      chats: Array.from(merged.values())
    }
    if (typeof parsed?.listener_enabled === 'boolean') {
      body.enabled = parsed.listener_enabled
    }

    await listenerApi({
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body)
    })

    if (added === 1 && updated === 1) {
      props.notify(t('settings.listenerImportedOneEach'))
    } else if (added === 1) {
      props.notify(t('settings.listenerImportedOne', { updated }))
    } else if (updated === 1) {
      props.notify(t('settings.listenerImportedOneUpdated', { added }))
    } else {
      props.notify(t('settings.listenerImported', { added, updated }))
    }
  } catch (err) {
    props.notify(err.message || t('settings.listenerImportFail'), true)
  } finally {
    importing.value = false
  }
}

// Exportar e importar descargas e historial con chunks
const exportingDownloads = ref(false)
const importingDownloads = ref(false)
const importDownloadsInput = ref(null)

const exportDownloads = async () => {
  if (exportingDownloads.value) return
  exportingDownloads.value = true
  try {
    const response = await fetch('/api/downloads/export', {
      method: 'POST',
      headers: { ...authHeaders() }
    })
    const data = await response.json().catch(() => ({}))
    if (!response.ok) {
      throw new Error(
        data.detail || data.error || t('settings.downloadsExportFail')
      )
    }
    const count = data.downloads_count || data.payload?.downloads?.length || 0
    if (count === 0) {
      props.notify(t('settings.downloadsExportEmpty'), true)
      return
    }

    const path = data.export_path || props.settings.download_folder

    props.notify(
      count === 1
        ? t('settings.downloadsExportedOne', { path })
        : t('settings.downloadsExported', { n: count, path })
    )
  } catch (err) {
    props.notify(err.message || t('settings.downloadsExportFail'), true)
  } finally {
    exportingDownloads.value = false
  }
}

const pickImportDownloadsFile = () => {
  if (importingDownloads.value) return
  importDownloadsInput.value?.click()
}

const onImportDownloadsFile = async (event) => {
  const file = event.target.files?.[0]
  event.target.value = ''
  if (!file || importingDownloads.value) return
  importingDownloads.value = true
  try {
    const text = await file.text()
    let parsed
    try {
      parsed = JSON.parse(text)
    } catch {
      throw new Error(t('settings.importInvalidJson'))
    }

    const response = await fetch('/api/downloads/import', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...authHeaders()
      },
      body: JSON.stringify(parsed)
    })
    const data = await response.json().catch(() => ({}))
    if (!response.ok) {
      throw new Error(
        data.detail || data.error || t('settings.downloadsImportFail')
      )
    }

    const imported = data.imported_count || 0
    if (imported === 0) {
      props.notify(t('settings.importNoDownloads'), true)
    } else {
      props.notify(
        imported === 1
          ? t('settings.downloadsImportedOne')
          : t('settings.downloadsImported', { n: imported })
      )
    }
  } catch (err) {
    props.notify(err.message || t('settings.downloadsImportFail'), true)
  } finally {
    importingDownloads.value = false
  }
}

// Estado y funciones para notificaciones del bot
const showBotToken = ref(false)
const detectingChat = ref(false)
const sendingTest = ref(false)
const detectedChatName = ref('')

const detectChat = async () => {
  if (!props.settings.notification_bot_token) {
    props.notify(t('settings.invalidToken'), true)
    return
  }
  detectingChat.value = true
  try {
    const response = await fetch('/api/notifications/detect', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...authHeaders() },
      body: JSON.stringify({
        token: props.settings.notification_bot_token,
        chat_id: props.settings.notification_chat_id || 0,
        topic_id: props.settings.notification_topic_id || null
      })
    })
    const data = await response.json().catch(() => ({}))
    if (!response.ok) {
      throw new Error(data.detail || data.error || t('settings.detectError'))
    }
    patch({
      notification_chat_id: data.chat_id
    })
    detectedChatName.value = data.chat_name || ''
    props.notify(`${t('settings.detectedChat')}: ${data.chat_name}`)
  } catch (err) {
    props.notify(err.message || t('settings.detectError'), true)
  } finally {
    detectingChat.value = false
  }
}

const sendTestNotification = async () => {
  if (
    !props.settings.notification_bot_token ||
    !props.settings.notification_chat_id
  ) {
    props.notify(t('settings.invalidToken'), true)
    return
  }
  sendingTest.value = true
  try {
    const response = await fetch('/api/notifications/test', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...authHeaders() },
      body: JSON.stringify({
        token: props.settings.notification_bot_token,
        chat_id: props.settings.notification_chat_id,
        topic_id: props.settings.notification_topic_id
      })
    })
    const data = await response.json().catch(() => ({}))
    if (!response.ok) {
      throw new Error(data.detail || data.error || t('settings.testError'))
    }
    props.notify(t('settings.testSent'))
  } catch (err) {
    props.notify(err.message || t('settings.testError'), true)
  } finally {
    sendingTest.value = false
  }
}
</script>

<template>
  <div class="settings-view">
    <div class="content-grid-single">
      <aside class="panel settings-panel-full">
        <div class="panel-heading">
          <div>
            <span class="eyebrow">
              <Settings2 :size="12" /> {{ t('settings.kicker') }}
            </span>
            <h2>{{ t('settings.title') }}</h2>
          </div>
          <span class="save-state">
            {{ saving ? t('settings.saving') : t('settings.autosaved') }}
          </span>
        </div>

        <!-- Aviso de exposición (SEC-05): solo aparece cuando el servidor
             escucha en una dirección que no es de loopback, en cuyo caso el
             panel queda abierto a la red local sin cifrar. -->
        <div v-if="networkExposed" class="exposure-banner" role="alert">
          <AlertCircle :size="18" class="banner-icon" />
          <div class="exposure-banner-text">
            <strong>{{ t('settings.exposureTitle') }}</strong>
            <p>{{ t('settings.exposureText', { host: serverInfo.host }) }}</p>
          </div>
        </div>

        <div class="settings-accordion">
          <AppAccordion
            :title="t('settings.downloadsTitle')"
            :description="t('settings.downloadsSub')"
            :model-value="openSection === 'downloads'"
            @update:model-value="openSection = $event ? 'downloads' : ''"
          >
            <template #icon>
              <ArrowDownToLine :size="15" />
            </template>

            <div class="settings-columns">
              <!-- Concurrencia y Workers -->
              <div class="settings-group">
                <label class="setting-label">
                  {{ t('settings.concurrent') }}
                  <output>
                    {{ props.settings.max_concurrent_downloads }}
                  </output>
                </label>
                <input
                  :value="props.settings.max_concurrent_downloads"
                  type="range"
                  min="1"
                  max="32"
                  class="range-input"
                  @input="
                    patch({
                      max_concurrent_downloads: Number($event.target.value)
                    })
                  "
                />
                <div class="range-hints"><span>1</span><span>32</span></div>

                <div class="setting-line">
                  <div>
                    <strong>{{ t('settings.chunks') }}</strong>
                    <small>{{ t('settings.chunksSub') }}</small>
                  </div>
                  <label class="switch">
                    <input
                      :checked="props.settings.parallel_chunks"
                      type="checkbox"
                      @change="
                        patch({ parallel_chunks: $event.target.checked })
                      "
                    />
                    <span></span>
                  </label>
                </div>

                <label class="setting-label compact">
                  {{ t('settings.workers') }}
                  <output>{{ props.settings.chunk_workers }}</output>
                </label>
                <input
                  :value="props.settings.chunk_workers"
                  :disabled="!props.settings.parallel_chunks"
                  type="range"
                  min="1"
                  max="8"
                  class="range-input"
                  @input="patch({ chunk_workers: Number($event.target.value) })"
                />
                <div class="range-hints"><span>1</span><span>8</span></div>
              </div>

              <!-- Velocidad y Directorio -->
              <div class="settings-group">
                <div class="speed-setting">
                  <label class="setting-label compact">
                    {{ t('settings.speedLimit') }}
                  </label>
                  <div class="speed-row">
                    <input
                      :value="props.settings.speed_limit.value"
                      type="number"
                      min="0"
                      step="0.5"
                      @input="
                        patch({
                          speed_limit: {
                            ...props.settings.speed_limit,
                            value: Number($event.target.value)
                          }
                        })
                      "
                    />
                    <select
                      :value="props.settings.speed_limit.unit"
                      @change="
                        patch({
                          speed_limit: {
                            ...props.settings.speed_limit,
                            unit: $event.target.value
                          }
                        })
                      "
                    >
                      <option>KB</option>
                      <option>MB</option>
                      <option>GB</option>
                    </select>
                    <span>/s</span>
                  </div>
                  <small>{{ t('settings.speedHint') }}</small>
                </div>

                <FolderPicker
                  :model-value="props.settings.download_folder"
                  @update:model-value="patch({ download_folder: $event })"
                />

                <div class="setting-line">
                  <div>
                    <strong>{{ t('settings.organizeByChat') }}</strong>
                    <small>{{ t('settings.organizeByChatSub') }}</small>
                  </div>
                  <label class="switch">
                    <input
                      :checked="props.settings.organize_by_chat"
                      type="checkbox"
                      @change="
                        patch({ organize_by_chat: $event.target.checked })
                      "
                    />
                    <span></span>
                  </label>
                </div>

                <div class="setting-line">
                  <div>
                    <strong>{{ t('settings.minimizeToTrayTitle') }}</strong>
                    <small>{{ t('settings.minimizeToTraySub') }}</small>
                  </div>
                  <label class="switch">
                    <input
                      :checked="props.settings.minimize_to_tray"
                      type="checkbox"
                      @change="
                        patch({ minimize_to_tray: $event.target.checked })
                      "
                    />
                    <span></span>
                  </label>
                </div>
              </div>
            </div>
          </AppAccordion>

          <!-- 2. Temas -->
          <AppAccordion
            :title="t('settings.themesTitle')"
            :description="t('settings.themesSub')"
            :model-value="openSection === 'themes'"
            @update:model-value="openSection = $event ? 'themes' : ''"
          >
            <template #icon>
              <Palette :size="15" />
            </template>

            <div class="settings-columns">
              <!-- Paleta de Color y Tema -->
              <div class="settings-group color-group">
                <span class="setting-label">{{
                  t('settings.accentTitle')
                }}</span>

                <!-- Los 0-15 vienen de Telegram Premium; del 16 en adelante son las
                       paletas propias del panel. Van separados con su propio título
                       para que se vea de dónde sale cada grupo. -->
                <span class="color-group-title">{{
                  t('settings.telegramGroup')
                }}</span>
                <div class="color-selector-container">
                  <div class="color-row">
                    <button
                      v-for="id in [0, 1, 2, 3, 4, 5, 6, 7]"
                      :key="id"
                      type="button"
                      class="color-dot"
                      :class="{ active: props.settings.color_id === id }"
                      :style="{
                        background: themeMap[id]?.gradient || '#38a7ff'
                      }"
                      :title="t('settings.colorN', { n: id })"
                      @click="patch({ color_id: id })"
                    ></button>
                  </div>
                  <div class="color-row">
                    <button
                      v-for="id in [8, 9, 10, 11, 12, 13, 14, 15]"
                      :key="id"
                      type="button"
                      class="color-dot gradient-dot"
                      :class="{ active: props.settings.color_id === id }"
                      :style="{
                        background: `linear-gradient(135deg, ${themeMap[id]?.primary || '#38a7ff'} 49.8%, ${themeMap[id]?.secondary || '#b48bf2'} 50.2%)`
                      }"
                      :title="t('settings.gradientN', { n: id })"
                      @click="patch({ color_id: id })"
                    ></button>
                  </div>
                </div>

                <!-- Colores temáticos: paletas propias del panel. Se guardan en el
                       mismo ajuste (color_id) que los de arriba: elegir uno sustituye
                       al color de la cuenta, y "Restablecer color de la cuenta"
                       vuelve a dejar el de Telegram. -->
                <span class="color-group-title">{{
                  t('settings.thematicGroup')
                }}</span>
                <div class="themes-list">
                  <button
                    v-for="id in specialThemes"
                    :key="'theme-' + id"
                    type="button"
                    class="theme-chip"
                    :class="{ active: props.settings.color_id === id }"
                    :title="
                      themeMap[id]?.name || t('settings.themeN', { n: id })
                    "
                    @click="patch({ color_id: id })"
                  >
                    <span
                      class="theme-swatch"
                      :style="{ background: themeMap[id]?.gradient }"
                    ></span>
                    <span class="theme-name">{{
                      themeMap[id]?.name || t('settings.themeN', { n: id })
                    }}</span>
                  </button>
                </div>

                <button
                  type="button"
                  class="reset-button-alt"
                  @click="emit('reset-color')"
                >
                  <Zap :size="14" /> {{ t('settings.resetAccountColor') }}
                </button>
              </div>

              <!-- Color del Loader -->
              <div class="settings-group color-group">
                <span class="setting-label">{{
                  t('settings.loaderTitle')
                }}</span>

                <span class="color-group-title">{{
                  t('settings.telegramGroup')
                }}</span>
                <div class="color-selector-container">
                  <div class="color-row">
                    <button
                      v-for="id in [0, 1, 2, 3, 4, 5, 6, 7]"
                      :key="'loader-' + id"
                      type="button"
                      class="color-dot"
                      :class="{
                        active: props.settings.loader_color_id === id
                      }"
                      :style="{
                        background: themeMap[id]?.gradient || '#38a7ff'
                      }"
                      :title="t('settings.colorLoaderN', { n: id })"
                      @click="patch({ loader_color_id: id })"
                    ></button>
                  </div>
                  <div class="color-row">
                    <button
                      v-for="id in [8, 9, 10, 11, 12, 13, 14, 15]"
                      :key="'loader-grad-' + id"
                      type="button"
                      class="color-dot gradient-dot"
                      :class="{
                        active: props.settings.loader_color_id === id
                      }"
                      :style="{
                        background: `linear-gradient(135deg, ${themeMap[id]?.primary || '#38a7ff'} 49.8%, ${themeMap[id]?.secondary || '#b48bf2'} 50.2%)`
                      }"
                      :title="t('settings.gradientLoaderN', { n: id })"
                      @click="patch({ loader_color_id: id })"
                    ></button>
                  </div>
                </div>

                <!-- Aquí solo va el círculo con el degradado del tema, sin nombre:
                       del tema especial el loader únicamente toma el color, no el
                       decorado, así que no hay nada más que enseñar. -->
                <span class="color-group-title">{{
                  t('settings.thematicGroupShort')
                }}</span>
                <div class="color-selector-container">
                  <div class="color-row">
                    <button
                      v-for="id in specialThemes"
                      :key="'loader-theme-' + id"
                      type="button"
                      class="color-dot theme-dot"
                      :class="{
                        active: props.settings.loader_color_id === id
                      }"
                      :style="{ background: themeMap[id]?.gradient }"
                      :title="
                        themeMap[id]?.name || t('settings.themeN', { n: id })
                      "
                      @click="patch({ loader_color_id: id })"
                    ></button>
                  </div>
                </div>

                <button
                  type="button"
                  class="reset-button-alt"
                  @click="emit('reset-loader-color')"
                >
                  <Zap :size="14" /> {{ t('settings.resetLoaderColor') }}
                </button>
              </div>
            </div>
          </AppAccordion>

          <!-- 3. Idioma -->
          <AppAccordion
            :title="t('settings.langTitle')"
            :description="t('settings.langSub')"
            :model-value="openSection === 'language'"
            @update:model-value="openSection = $event ? 'language' : ''"
          >
            <template #icon>
              <Languages :size="15" />
            </template>

            <div class="settings-group">
              <!-- El idioma se aplica al instante y se guarda como un ajuste
                     más (settings.language). El chip activo sigue a `locale`,
                     que es el idioma realmente en uso, no al valor bruto del
                     ajuste (vacío la primera vez, antes de autodetectar). -->
              <div class="themes-list">
                <button
                  v-for="lang in availableLocales"
                  :key="lang.id"
                  type="button"
                  class="theme-chip language-chip"
                  :class="{ active: locale === lang.id }"
                  :aria-pressed="locale === lang.id"
                  @click="patch({ language: lang.id })"
                >
                  <span class="theme-name">{{ lang.label }}</span>
                </button>
              </div>
              <small>{{ t('settings.langNote') }}</small>
            </div>
          </AppAccordion>

          <!-- Proxy -->
          <AppAccordion
            :title="t('settings.proxyTitle')"
            :description="t('settings.proxySub')"
            :model-value="openSection === 'proxy'"
            @update:model-value="openSection = $event ? 'proxy' : ''"
          >
            <template #icon>
              <Radio :size="15" />
            </template>

            <div class="settings-group proxy-section-group">
              <div class="setting-line clean-line">
                <div>
                  <strong>{{ t('settings.proxyEnabled') }}</strong>
                  <small>{{ t('settings.proxySub') }}</small>
                </div>
                <label class="switch">
                  <input
                    type="checkbox"
                    :checked="props.settings.proxy_enabled"
                    @change="
                      patch({
                        proxy_enabled: $event.target.checked,
                        ...($event.target.checked ? { proxy_notice: '' } : {})
                      })
                    "
                  />
                  <span></span>
                </label>
              </div>

              <div
                class="proxy-inline-fields"
                :class="{ disabled: !props.settings.proxy_enabled }"
              >
                <!-- Importar proxy mediante enlace telegramdl:// -->
                <div class="proxy-link-import-row">
                  <div class="field-col link-col">
                    <label class="field-label">{{
                      t('settings.proxyLinkImport')
                    }}</label>
                    <div class="link-input-group">
                      <input
                        v-model="proxyLinkInput"
                        type="text"
                        class="proxy-input"
                        :placeholder="t('settings.proxyLinkPlaceholder')"
                        @keyup.enter="addProxyFromLink"
                      />
                      <button
                        type="button"
                        class="add-proxy-btn"
                        :title="t('settings.proxyAdd')"
                        :disabled="testingLinkProxy || !proxyLinkInput"
                        @click="addProxyFromLink"
                      >
                        <Loader2
                          v-if="testingLinkProxy"
                          class="spin"
                          :size="16"
                        />
                        <Plus v-else :size="16" />
                      </button>
                    </div>
                  </div>
                </div>

                <!-- Resultado de comprobación por enlace -->
                <div
                  v-if="linkTestResult"
                  class="test-section link-test-section"
                >
                  <div
                    class="test-result"
                    :class="{
                      active: linkTestResult.active,
                      inactive: !linkTestResult.active
                    }"
                  >
                    <CheckCircle2 v-if="linkTestResult.active" :size="14" />
                    <AlertCircle v-else :size="14" />
                    <span v-if="linkTestResult.active">
                      {{
                        t('settings.proxyActive', {
                          ping: linkTestResult.ping_ms
                        })
                      }}
                    </span>
                    <span v-else>
                      {{ t('settings.proxyInactive') }}:
                      {{ linkTestResult.error }}
                    </span>
                  </div>
                </div>

                <div class="proxy-type-row">
                  <span class="field-label-inline">{{
                    t('settings.proxyType')
                  }}</span>
                  <div class="radio-group">
                    <label
                      class="radio-option"
                      :class="{
                        active:
                          (newProxyForm.proxy_type || 'socks5') === 'socks5'
                      }"
                      @click="newProxyForm.proxy_type = 'socks5'"
                    >
                      <span class="radio-circle">
                        <span class="radio-dot"></span>
                      </span>
                      <span class="radio-label">SOCKS5</span>
                    </label>

                    <label
                      class="radio-option"
                      :class="{
                        active: newProxyForm.proxy_type === 'mtproto'
                      }"
                      @click="newProxyForm.proxy_type = 'mtproto'"
                    >
                      <span class="radio-circle">
                        <span class="radio-dot"></span>
                      </span>
                      <span class="radio-label">MTProto</span>
                    </label>

                    <label
                      class="radio-option"
                      :class="{ active: newProxyForm.proxy_type === 'http' }"
                      @click="newProxyForm.proxy_type = 'http'"
                    >
                      <span class="radio-circle">
                        <span class="radio-dot"></span>
                      </span>
                      <span class="radio-label">HTTP</span>
                    </label>
                  </div>
                </div>

                <div class="proxy-inputs-single-line">
                  <div class="field-col host-col">
                    <label class="field-label">{{
                      t('settings.proxyHost')
                    }}</label>
                    <input
                      v-model="newProxyForm.proxy_host"
                      type="text"
                      class="proxy-input"
                      placeholder="127.0.0.1"
                    />
                  </div>
                  <div class="field-col port-col">
                    <label class="field-label">{{
                      t('settings.proxyPort')
                    }}</label>
                    <input
                      v-model.number="newProxyForm.proxy_port"
                      type="number"
                      class="proxy-input"
                      placeholder="1080"
                    />
                  </div>
                  <template
                    v-if="
                      (newProxyForm.proxy_type || 'socks5') === 'socks5' ||
                      newProxyForm.proxy_type === 'http'
                    "
                  >
                    <div class="field-col user-col">
                      <label class="field-label">{{
                        t('settings.proxyUsername')
                      }}</label>
                      <input
                        v-model="newProxyForm.proxy_username"
                        type="text"
                        class="proxy-input"
                      />
                    </div>
                    <div class="field-col pass-col">
                      <label class="field-label">{{
                        t('settings.proxyPassword')
                      }}</label>
                      <input
                        v-model="newProxyForm.proxy_password"
                        type="password"
                        class="proxy-input"
                      />
                    </div>
                  </template>
                  <template v-else-if="newProxyForm.proxy_type === 'mtproto'">
                    <div class="field-col secret-col">
                      <label class="field-label">{{
                        t('settings.proxySecret')
                      }}</label>
                      <input
                        v-model="newProxyForm.proxy_secret"
                        type="text"
                        class="proxy-input"
                        placeholder="ee0123456789..."
                      />
                    </div>
                  </template>
                  <div class="field-col add-col">
                    <label class="field-label">&nbsp;</label>
                    <button
                      type="button"
                      class="add-proxy-btn"
                      :title="t('settings.proxyAdd')"
                      :disabled="
                        testingNewProxy ||
                        !newProxyForm.proxy_host ||
                        !newProxyForm.proxy_port
                      "
                      @click="addCurrentProxy"
                    >
                      <Loader2 v-if="testingNewProxy" class="spin" :size="16" />
                      <Plus v-else :size="16" />
                    </button>
                  </div>
                </div>

                <div v-if="newProxyTestResult" class="test-section">
                  <div
                    class="test-result"
                    :class="{
                      active: newProxyTestResult.active,
                      inactive: !newProxyTestResult.active
                    }"
                  >
                    <CheckCircle2 v-if="newProxyTestResult.active" :size="14" />
                    <AlertCircle v-else :size="14" />
                    <span v-if="newProxyTestResult.active">
                      {{
                        t('settings.proxyActive', {
                          ping: newProxyTestResult.ping_ms
                        })
                      }}
                    </span>
                    <span v-else>
                      {{ t('settings.proxyInactive') }}:
                      {{ newProxyTestResult.error }}
                    </span>
                  </div>
                </div>

                <!-- Lista de proxies guardados -->
                <div
                  v-if="savedProxies.length > 0"
                  class="saved-proxies-container"
                >
                  <span class="color-group-title">{{
                    t('settings.proxySavedList')
                  }}</span>
                  <div class="saved-proxies-list">
                    <div
                      v-for="p in savedProxies"
                      :key="p.id"
                      class="saved-proxy-item"
                      :class="{ active: isProxySelected(p) }"
                      @click="selectProxy(p)"
                    >
                      <div class="proxy-item-left">
                        <span class="radio-circle">
                          <span class="radio-dot"></span>
                        </span>
                        <div class="proxy-item-info">
                          <span class="proxy-item-name">{{
                            p.proxy_host
                          }}</span>
                          <span
                            class="proxy-item-ping"
                            :class="proxyPingClass(p)"
                          >
                            {{ proxyPingText(p) }}
                          </span>
                        </div>
                      </div>
                      <button
                        type="button"
                        class="proxy-info-btn"
                        :title="t('settings.proxyInfoTitle')"
                        @click.stop="openEditModal(p)"
                      >
                        <Info :size="16" />
                      </button>
                    </div>
                  </div>
                </div>
              </div>
            </div>
          </AppAccordion>

          <!-- 4. Acceso remoto -->
          <AppAccordion
            :title="t('settings.remoteTitle')"
            :description="t('settings.remoteSub')"
            :model-value="openSection === 'remote'"
            @update:model-value="openSection = $event ? 'remote' : ''"
          >
            <template #icon>
              <KeyRound :size="15" />
            </template>

            <div class="settings-group">
              <small>
                {{ remoteDescParts[0]
                }}<a
                  class="inline-link"
                  href="https://tailscale.com"
                  target="_blank"
                  rel="noopener noreferrer"
                  @click="openExternal('https://tailscale.com', $event)"
                  >Tailscale</a
                >{{ remoteDescParts[1] }}
              </small>

              <div class="token-row">
                <input
                  :value="showToken ? apiToken : maskedToken"
                  type="text"
                  readonly
                />
              </div>

              <div
                class="settings-actions"
                style="margin-top: 0; padding: 0; flex-wrap: wrap"
              >
                <button
                  type="button"
                  class="reset-button-alt"
                  @click="showToken = !showToken"
                >
                  <component :is="showToken ? EyeOff : Eye" :size="14" />
                  {{ showToken ? t('settings.hide') : t('settings.show') }}
                </button>
                <button
                  type="button"
                  class="reset-button-alt"
                  @click="copyToken"
                >
                  <Copy :size="14" /> {{ copyLabel }}
                </button>
                <button
                  type="button"
                  class="reset-button-alt"
                  @click="emit('regenerate-token')"
                >
                  <RefreshCw :size="14" /> {{ t('settings.regenerate') }}
                </button>
              </div>
            </div>
          </AppAccordion>

          <!-- 5. Notificaciones Telegram -->
          <AppAccordion
            :title="t('settings.notificationsTitle')"
            :description="t('settings.notificationsSub')"
            :model-value="openSection === 'notifications'"
            @update:model-value="openSection = $event ? 'notifications' : ''"
          >
            <template #icon>
              <Bell :size="15" />
            </template>

            <div class="settings-group">
              <!-- Activar notificaciones -->
              <div
                class="setting-line"
                style="
                  border-top: none;
                  margin-top: 0;
                  padding-top: 0;
                  border-bottom: none;
                "
              >
                <div>
                  <strong>{{ t('settings.notificationsEnabled') }}</strong>
                  <small>
                    {{ notifDescParts[0]
                    }}<a
                      class="inline-link"
                      href="https://t.me/BotFather"
                      target="_blank"
                      rel="noopener noreferrer"
                      @click="openExternal('https://t.me/BotFather', $event)"
                      >@BotFather</a
                    >{{ notifDescParts[1] }}
                  </small>
                </div>
                <label class="switch">
                  <input
                    type="checkbox"
                    :checked="settings.notification_bot_enabled"
                    @change="
                      patch({ notification_bot_enabled: $event.target.checked })
                    "
                  />
                  <span></span>
                </label>
              </div>

              <template v-if="settings.notification_bot_enabled">
                <div class="notif-grid">
                  <!-- Token del bot -->
                  <div class="notif-field">
                    <label class="notif-label">{{
                      t('settings.botToken')
                    }}</label>
                    <div class="notif-input-row">
                      <input
                        :type="showBotToken ? 'text' : 'password'"
                        :value="settings.notification_bot_token"
                        :placeholder="t('settings.botTokenPlaceholder')"
                        @input="
                          patch({ notification_bot_token: $event.target.value })
                        "
                      />
                      <button
                        type="button"
                        class="reset-button-alt"
                        style="padding: 10px 12px"
                        @click="showBotToken = !showBotToken"
                        :title="
                          showBotToken ? t('settings.hide') : t('settings.show')
                        "
                      >
                        <Eye v-if="!showBotToken" :size="15" />
                        <EyeOff v-else :size="15" />
                      </button>
                    </div>
                  </div>

                  <!-- ID del Chat -->
                  <div class="notif-field">
                    <label class="notif-label">{{
                      t('settings.chatID')
                    }}</label>
                    <div class="notif-input-row">
                      <input
                        type="number"
                        :value="settings.notification_chat_id || ''"
                        placeholder="123456789"
                        @input="
                          patch({
                            notification_chat_id:
                              Number($event.target.value) || 0
                          })
                        "
                      />
                    </div>
                  </div>

                  <!-- ID del Tema (opcional) -->
                  <div class="notif-field">
                    <label class="notif-label">{{
                      t('settings.topicID')
                    }}</label>
                    <div class="notif-input-row">
                      <input
                        type="number"
                        :value="settings.notification_topic_id || ''"
                        :placeholder="t('settings.topicIDHint')"
                        @input="
                          patch({
                            notification_topic_id: $event.target.value
                              ? Number($event.target.value)
                              : null
                          })
                        "
                      />
                    </div>
                  </div>

                  <!-- Modo de renombrado de archivos -->
                  <div class="notif-field">
                    <label class="notif-label">{{
                      t('settings.botFileNamingMode')
                    }}</label>
                    <div class="notif-input-row">
                      <select
                        :value="settings.bot_file_naming_mode || 'auto'"
                        @change="
                          patch({ bot_file_naming_mode: $event.target.value })
                        "
                      >
                        <option value="auto">
                          {{ t('settings.botFileNamingAuto') }}
                        </option>
                        <option value="manual">
                          {{ t('settings.botFileNamingManual') }}
                        </option>
                      </select>
                    </div>
                  </div>

                  <!-- Botones de Acción: Detectar chat y Enviar prueba -->
                  <div class="notif-field notif-actions-col">
                    <div class="settings-actions" style="margin-top: 0">
                      <button
                        type="button"
                        class="reset-button-alt"
                        :disabled="
                          detectingChat || !settings.notification_bot_token
                        "
                        @click="detectChat"
                      >
                        <RefreshCw
                          :size="14"
                          :class="{ spinning: detectingChat }"
                        />
                        {{
                          detectingChat
                            ? t('settings.detectingChat')
                            : t('settings.detectChat')
                        }}
                      </button>
                      <button
                        type="button"
                        class="reset-button-alt"
                        :disabled="
                          sendingTest ||
                          !settings.notification_bot_token ||
                          !settings.notification_chat_id
                        "
                        @click="sendTestNotification"
                      >
                        <Send v-if="!sendingTest" :size="14" />
                        <RefreshCw
                          v-else
                          :size="14"
                          :class="{ spinning: sendingTest }"
                        />
                        {{
                          sendingTest
                            ? t('settings.sendingTest')
                            : t('settings.sendTest')
                        }}
                      </button>
                    </div>
                  </div>
                </div>

                <!-- Info Chat Detectado -->
                <div v-if="detectedChatName" class="notif-chat-badge">
                  <small>
                    {{ t('settings.detectedChat') }}:
                    <strong>{{ detectedChatName }}</strong>
                  </small>
                </div>

                <!-- Los avisos automáticos (cola terminada y errores) ya no se
                     configuran aquí: comparten el botón del panel lateral. -->
                <div class="notif-hint">
                  {{ t('settings.notifySidebarHint') }}
                </div>
              </template>
            </div>
          </AppAccordion>

          <!-- 6. Datos: escucha e historial -->
          <AppAccordion
            :title="t('settings.dataTitle')"
            :description="t('settings.dataSub')"
            :model-value="openSection === 'data'"
            @update:model-value="openSection = $event ? 'data' : ''"
          >
            <template #icon>
              <HardDrive :size="15" />
            </template>

            <div class="settings-group">
              <div class="settings-actions data-actions">
                <button
                  type="button"
                  class="reset-button-alt"
                  :disabled="exportingDownloads"
                  @click="exportDownloads"
                >
                  <Download :size="14" />
                  {{
                    exportingDownloads
                      ? t('settings.exportingDownloads')
                      : t('settings.exportDownloads')
                  }}
                </button>
                <button
                  type="button"
                  class="reset-button-alt"
                  :disabled="importingDownloads"
                  @click="pickImportDownloadsFile"
                >
                  <Upload :size="14" />
                  {{
                    importingDownloads
                      ? t('settings.importingDownloads')
                      : t('settings.importDownloads')
                  }}
                </button>

                <button
                  type="button"
                  class="reset-button-alt"
                  :disabled="exporting"
                  @click="exportListener"
                >
                  <Download :size="14" />
                  {{
                    exporting
                      ? t('settings.exportingListener')
                      : t('settings.exportListener')
                  }}
                </button>
                <button
                  type="button"
                  class="reset-button-alt"
                  :disabled="importing"
                  @click="pickImportFile"
                >
                  <Upload :size="14" />
                  {{
                    importing
                      ? t('settings.importingListener')
                      : t('settings.importListener')
                  }}
                </button>
                <button
                  class="clear-history-button history-button"
                  type="button"
                  @click="emit('clear-history')"
                >
                  <Trash2 :size="15" /> {{ t('settings.clearHistory') }}
                </button>
                <input
                  ref="importInput"
                  type="file"
                  accept="application/json,.json"
                  style="display: none"
                  @change="onImportFile"
                />
                <input
                  ref="importDownloadsInput"
                  type="file"
                  accept="application/json,.json"
                  style="display: none"
                  @change="onImportDownloadsFile"
                />
              </div>
            </div>
          </AppAccordion>
        </div>
      </aside>
    </div>

    <!-- Los ajustes se guardan solos; este botón es para forzar el guardado en
         el momento. Va flotando en la esquina para no ocupar sitio dentro del
         panel ahora que las secciones se pliegan. -->
    <!-- Botón de guardado manual ocultado ya que la vista cuenta con autoguardado:
    <button
      class="save-button save-fab"
      type="button"
      :disabled="saving"
      :title="saving ? t('settings.saving') : t('settings.saveNow')"
      :aria-label="t('settings.saveNow')"
      @click="emit('save-settings')"
    >
      <Save :size="19" />
    </button>
    -->
    <!-- Modal para ver y editar detalles del proxy -->
    <Teleport to="body">
      <Transition name="fade">
        <div
          v-if="editingProxyModal"
          class="modal-overlay"
          @click.self="editingProxyModal = false"
        >
          <div class="modal-content proxy-detail-modal">
            <div class="modal-header">
              <h3>{{ t('settings.proxyInfoTitle') }}</h3>
              <button
                type="button"
                class="close-btn"
                @click="editingProxyModal = false"
              >
                <X :size="18" />
              </button>
            </div>

            <div class="modal-body">
              <!-- Tipo de Proxy -->
              <div class="proxy-type-row">
                <span class="field-label-inline">{{
                  t('settings.proxyType')
                }}</span>
                <div class="radio-group">
                  <label
                    class="radio-option"
                    :class="{
                      active: editingProxyForm.proxy_type === 'socks5'
                    }"
                    @click="editingProxyForm.proxy_type = 'socks5'"
                  >
                    <span class="radio-circle"
                      ><span class="radio-dot"></span
                    ></span>
                    <span class="radio-label">SOCKS5</span>
                  </label>
                  <label
                    class="radio-option"
                    :class="{
                      active: editingProxyForm.proxy_type === 'mtproto'
                    }"
                    @click="editingProxyForm.proxy_type = 'mtproto'"
                  >
                    <span class="radio-circle"
                      ><span class="radio-dot"></span
                    ></span>
                    <span class="radio-label">MTProto</span>
                  </label>
                  <label
                    class="radio-option"
                    :class="{
                      active: editingProxyForm.proxy_type === 'http'
                    }"
                    @click="editingProxyForm.proxy_type = 'http'"
                  >
                    <span class="radio-circle"
                      ><span class="radio-dot"></span
                    ></span>
                    <span class="radio-label">HTTP</span>
                  </label>
                </div>
              </div>

              <!-- Servidor y Puerto -->
              <div class="proxy-modal-row">
                <div class="field-col host-col">
                  <label class="field-label">{{
                    t('settings.proxyHost')
                  }}</label>
                  <input
                    v-model="editingProxyForm.proxy_host"
                    type="text"
                    class="proxy-input"
                    placeholder="127.0.0.1"
                  />
                </div>
                <div class="field-col port-col">
                  <label class="field-label">{{
                    t('settings.proxyPort')
                  }}</label>
                  <input
                    v-model.number="editingProxyForm.proxy_port"
                    type="number"
                    class="proxy-input"
                    placeholder="1080"
                  />
                </div>
              </div>

              <!-- Credenciales / Secreto -->
              <div
                v-if="
                  editingProxyForm.proxy_type === 'socks5' ||
                  editingProxyForm.proxy_type === 'http'
                "
                class="proxy-modal-row"
              >
                <div class="field-col user-col">
                  <label class="field-label">{{
                    t('settings.proxyUsername')
                  }}</label>
                  <input
                    v-model="editingProxyForm.proxy_username"
                    type="text"
                    class="proxy-input"
                  />
                </div>
                <div class="field-col pass-col">
                  <label class="field-label">{{
                    t('settings.proxyPassword')
                  }}</label>
                  <input
                    v-model="editingProxyForm.proxy_password"
                    type="password"
                    class="proxy-input"
                  />
                </div>
              </div>
              <div
                v-else-if="editingProxyForm.proxy_type === 'mtproto'"
                class="proxy-modal-row"
              >
                <div class="field-col secret-col">
                  <label class="field-label">{{
                    t('settings.proxySecret')
                  }}</label>
                  <input
                    v-model="editingProxyForm.proxy_secret"
                    type="text"
                    class="proxy-input"
                    placeholder="ee0123456789..."
                  />
                </div>
              </div>

              <div v-if="shareNotice" class="proxy-share-notice">
                {{ shareNotice }}
              </div>
            </div>

            <div class="modal-footer proxy-modal-footer">
              <button
                type="button"
                class="reset-button-alt share-btn"
                @click="shareEditingProxy"
              >
                <Share2 :size="14" />
                <span>{{ t('settings.proxyShare') }}</span>
              </button>

              <div class="footer-right-actions">
                <button
                  type="button"
                  class="reset-button-alt delete-btn"
                  @click="deleteEditingProxy"
                >
                  <Trash2 :size="14" />
                  <span>{{ t('settings.proxyDelete') }}</span>
                </button>
                <button
                  type="button"
                  class="save-button"
                  @click="saveEditingProxy"
                >
                  <Save :size="14" />
                  <span>{{ t('settings.proxySave') }}</span>
                </button>
              </div>
            </div>
          </div>
        </div>
      </Transition>
    </Teleport>
  </div>
</template>

<style scoped>
/* El aviso de exposición reutiliza el aspecto de los avisos que ya existen en
   el panel (.auth-alert de AuthWizard y .logs-error de Registro): esa base vive
   con scope dentro de cada componente y no hay una clase de aviso global, así
   que aquí se reproduce con una única clase local la misma paleta existente,
   sin montar un sistema de estilos nuevo. */
.exposure-banner {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 11px 14px;
  margin-bottom: 18px;
  border: 1px solid #732a36;
  border-radius: 10px;
  background: #3d171d;
  color: #ff9e9e;
  font-size: 13px;
  line-height: 1.45;
}

.exposure-banner .banner-icon {
  flex: 0 0 auto;
  margin-top: 1px;
  color: #ffc764;
}

.exposure-banner-text strong {
  display: block;
  font-size: 13px;
  margin-bottom: 2px;
}

.exposure-banner-text p {
  margin: 0;
}

.proxy-section-group .setting-line.clean-line {
  border-top: none !important;
  border-bottom: none !important;
  padding-top: 0 !important;
  padding-bottom: 0 !important;
  margin-top: 0 !important;
  margin-bottom: 0 !important;
}

.proxy-inline-fields {
  display: flex;
  flex-direction: column;
  gap: 14px;
  margin-top: 14px;
  transition: opacity 0.2s;
}

.proxy-inline-fields.disabled {
  opacity: 0.5;
  pointer-events: none;
}

.proxy-type-row {
  display: flex;
  align-items: center;
  gap: 16px;
}

.field-label-inline {
  font-size: 0.9rem;
  font-weight: 600;
  color: var(--user-text);
  min-width: 90px;
}

.radio-group {
  display: flex;
  align-items: center;
  gap: 20px;
}

.radio-option {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  user-select: none;
  transition: opacity 0.2s;
}

.radio-option:hover {
  opacity: 0.85;
}

.radio-circle {
  width: 18px;
  height: 18px;
  border-radius: 50%;
  border: 2px solid var(--user-border);
  display: flex;
  align-items: center;
  justify-content: center;
  transition: all 0.2s ease;
  box-sizing: border-box;
}

.radio-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background-color: transparent;
  transform: scale(0);
  transition:
    transform 0.2s ease,
    background-color 0.2s ease;
}

.radio-option.active .radio-circle {
  border-color: var(--user-primary);
  box-shadow: 0 0 8px var(--user-glow);
}

.radio-option.active .radio-dot {
  background-color: var(--user-primary);
  transform: scale(1);
}

.radio-label {
  font-size: 0.9rem;
  font-weight: 600;
  color: var(--user-text);
}

.radio-option.active .radio-label {
  color: var(--user-text);
}

.proxy-link-import-row {
  margin-bottom: 6px;
  width: 100%;
}

.link-test-section {
  margin-top: 1px;
  margin-bottom: 16px;
}

.link-col {
  width: 100%;
}

.link-input-group {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
}

.proxy-inputs-single-line {
  display: flex;
  align-items: flex-end;
  gap: 10px;
  width: 100%;
}

.field-col {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.field-col.host-col {
  flex: 1;
  min-width: 140px;
}

.field-col.port-col {
  width: 80px;
  flex: 0 0 80px;
}

.field-col.user-col {
  width: 170px;
  flex: 0 0 170px;
}

.field-col.pass-col {
  width: 170px;
  flex: 0 0 170px;
}

.field-col.secret-col {
  flex: 1;
  min-width: 180px;
}

.field-label {
  font-size: 0.88rem;
  font-weight: 600;
  color: var(--user-text);
  white-space: nowrap;
}

.proxy-input {
  width: 100%;
  box-sizing: border-box;
  padding: 9px 12px;
  border-radius: 8px;
  border: 1px solid var(--user-border, rgba(255, 255, 255, 0.12));
  background: var(--user-surface-alt, rgba(0, 0, 0, 0.2));
  color: var(--user-text);
  font-size: 0.92rem;
  outline: none;
  transition:
    border-color 0.2s,
    box-shadow 0.2s;
}

.proxy-input:focus {
  border-color: var(--user-primary, var(--user-accent, #3b82f6));
  box-shadow: 0 0 0 2px var(--user-glow, rgba(59, 130, 246, 0.2));
}

.test-section {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-top: 6px;
  padding-top: 0;
  border-top: none !important;
}

.test-btn {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 0.88rem;
  font-weight: 600;
}

.test-result {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 0.88rem;
  font-weight: 600;
  padding: 6px 14px;
  border-radius: 8px;
}

.test-result.active {
  color: #34d399;
  background: rgba(52, 211, 153, 0.12);
}

.test-result.inactive {
  color: #f87171;
  background: rgba(248, 113, 113, 0.12);
}

.spin {
  animation: spin 1s linear infinite;
}

@keyframes spin {
  from {
    transform: rotate(0deg);
  }
  to {
    transform: rotate(360deg);
  }
}

.field-col.add-col {
  width: 38px;
  flex: 0 0 38px;
}

.add-proxy-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 38px;
  height: 38px;
  border-radius: 8px;
  border: 1px solid var(--user-border, rgba(255, 255, 255, 0.12));
  background: var(--user-primary, var(--user-accent, #3b82f6));
  color: #07111f !important;
  cursor: pointer;
  transition:
    opacity 0.2s,
    transform 0.1s;
}

.add-proxy-btn:hover:not(:disabled) {
  opacity: 0.9;
  transform: translateY(-1px);
}

.add-proxy-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.saved-proxies-container {
  display: flex;
  flex-direction: column;
  margin-top: 20px;
}

.saved-proxies-container .color-group-title {
  margin-bottom: 12px;
  display: block;
}

.saved-proxies-list {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}

@media (max-width: 640px) {
  .saved-proxies-list {
    grid-template-columns: 1fr;
  }
}

.saved-proxy-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 8px 12px;
  border-radius: 8px;
  border: 1px solid var(--user-border, rgba(255, 255, 255, 0.12));
  background: var(--user-surface-alt, rgba(0, 0, 0, 0.2));
  cursor: pointer;
  transition:
    border-color 0.2s,
    background 0.2s;
  min-width: 0;
}

.saved-proxy-item:hover {
  border-color: var(
    --user-border-light,
    var(--user-accent, rgba(255, 255, 255, 0.25))
  );
}

.saved-proxy-item.active {
  border-color: var(--user-primary, var(--user-accent, #3b82f6));
  background: var(--user-surface, rgba(59, 130, 246, 0.08));
}

.saved-proxy-item.active .radio-circle {
  border-color: var(--user-primary, var(--user-accent, #3b82f6));
}

.saved-proxy-item.active .radio-dot {
  opacity: 1;
  transform: scale(1);
  background: var(--user-primary, var(--user-accent, #3b82f6));
}

.proxy-item-left {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
  flex: 1;
}

.proxy-item-info {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  flex: 1;
}

.proxy-item-name {
  font-size: 0.88rem;
  font-weight: 600;
  color: var(--user-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.proxy-item-ping {
  font-size: 0.78rem;
  color: var(--user-text-dim, #a0aec0);
  white-space: nowrap;
  flex-shrink: 0;
}

.proxy-item-ping.active {
  color: #34d399;
}

.proxy-item-ping.error {
  color: #f87171;
}

.proxy-info-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  flex-shrink: 0;
  border-radius: 50%;
  border: 1px solid var(--user-border, rgba(255, 255, 255, 0.12));
  background: transparent;
  color: var(--user-text-dim, #a0aec0);
  cursor: pointer;
  transition:
    color 0.2s,
    border-color 0.2s,
    background 0.2s;
}

.proxy-info-btn:hover {
  color: var(--user-text);
  border-color: var(--user-primary, #3b82f6);
  background: rgba(255, 255, 255, 0.08);
}

.modal-overlay {
  position: fixed;
  top: 0;
  left: 0;
  width: 100vw;
  height: 100vh;
  background: rgba(2, 8, 19, 0.75);
  backdrop-filter: blur(8px);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 10000;
}

.proxy-detail-modal {
  background: var(--user-bg-top, #0e2032);
  border: 1px solid var(--user-border-light, rgba(255, 255, 255, 0.15));
  border-radius: 16px;
  padding: 22px 24px;
  max-width: 500px;
  width: 92%;
  box-shadow: 0 20px 40px rgba(0, 0, 0, 0.6);
  box-sizing: border-box;
}

.modal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding-bottom: 14px;
  margin-bottom: 16px;
  border-bottom: 1px solid var(--user-border-light, rgba(255, 255, 255, 0.12));
}

.modal-header h3 {
  margin: 0;
  font-size: 1.05rem;
  font-weight: 600;
  color: var(--user-text);
}

.modal-body {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.close-btn {
  background: transparent;
  border: none;
  color: var(--user-text-dim, #a0aec0);
  cursor: pointer;
  padding: 4px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 6px;
  transition:
    color 0.2s,
    background 0.2s;
}

.close-btn:hover {
  color: var(--user-text);
  background: rgba(255, 255, 255, 0.1);
}

.proxy-modal-row {
  display: flex;
  align-items: flex-end;
  gap: 12px;
  width: 100%;
}

.proxy-modal-row .field-col.host-col {
  flex: 1;
}

.proxy-modal-row .field-col.port-col {
  width: 90px;
  flex: 0 0 90px;
}

.proxy-modal-row .field-col.user-col {
  flex: 1;
}

.proxy-modal-row .field-col.pass-col {
  flex: 1;
}

.proxy-modal-row .field-col.secret-col {
  flex: 1;
}

.proxy-modal-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 24px;
  padding-top: 16px;
  border-top: 1px solid var(--user-border-light, rgba(255, 255, 255, 0.12));
}

.proxy-modal-footer button {
  height: 38px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 0 16px;
  border-radius: 8px;
  box-sizing: border-box;
  margin: 0;
  font-size: 0.88rem;
  font-weight: 600;
}

.footer-right-actions {
  display: flex;
  align-items: center;
  gap: 10px;
}

.delete-btn {
  color: #f87171 !important;
  border-color: rgba(248, 113, 113, 0.3) !important;
  background: transparent !important;
}

.delete-btn:hover {
  background: rgba(248, 113, 113, 0.12) !important;
}

.share-btn {
  color: var(--user-text) !important;
  border-color: var(--user-border, rgba(255, 255, 255, 0.15)) !important;
  background: transparent !important;
}

.share-btn:hover {
  border-color: var(--user-primary, #3b82f6) !important;
  background: rgba(255, 255, 255, 0.08) !important;
}

.proxy-share-notice {
  margin-top: 10px;
  font-size: 0.85rem;
  color: #34d399;
  font-weight: 500;
}
</style>
