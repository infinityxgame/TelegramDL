<script setup>
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import ListenerView from './views/ListenerView.vue'
import FolderPicker from './components/FolderPicker.vue'
import ConfirmModal from './components/ConfirmModal.vue'
import { Activity, ArrowDownToLine, ArrowUpRight, CheckCircle2, Clock3, Download, FileDown, Gauge, Menu, Radio, Save, Settings2, Trash2, X, Zap } from 'lucide-vue-next'

const downloads = ref([])
const newUrl = ref('')
const loading = ref(false)
const message = ref('')
const error = ref('')
const saving = ref(false)
const hydrated = ref(false)
const host = window.location.host
const logoUrl = `${import.meta.env.BASE_URL}telegramdl-android-icon.svg`
const activeView = ref('downloads')
const mobileMenuOpen = ref(false)
const settings = reactive({
  max_concurrent_downloads: 2,
  parallel_chunks: true,
  chunk_workers: 4,
  speed_limit: { value: 0, unit: 'MB' }
})

let timer
let saveTimer
let syncingSettings = false

const syncSettings = async nextSettings => {
  syncingSettings = true
  Object.assign(settings, nextSettings)
  await nextTick()
  syncingSettings = false
}

const showMessage = (txt, isError = false) => {
  if (isError) {
    error.value = txt
    setTimeout(() => { if (error.value === txt) error.value = '' }, 5000)
  } else {
    message.value = txt
    setTimeout(() => { if (message.value === txt) message.value = '' }, 3500)
  }
}

const modal = reactive({
  show: false,
  title: '',
  message: '',
  confirmText: '',
  type: 'primary',
  action: null
})

const openConfirm = (config) => {
  modal.title = config.title
  modal.message = config.message
  modal.confirmText = config.confirmText
  modal.type = config.type || 'primary'
  modal.action = config.action
  modal.show = true
}

const handleConfirm = () => {
  if (modal.action) modal.action()
  modal.show = false
}

const api = async (url, options = {}) => {
  const response = await fetch(url, options)
  const data = await response.json().catch(() => ({}))
  if (!response.ok) throw new Error(data.detail || data.error || 'Error en el servidor')
  return data
}

const fetchDownloads = async () => {
  try {
    downloads.value = await api('/api/downloads')
    error.value = ''
  } catch (err) { /* No mostramos error en poll constante para evitar molestia */ }
}

const fetchSettings = async () => {
  try {
    const data = await api('/api/settings')
    await syncSettings(data.settings)
    hydrated.value = true
  } catch (err) { showMessage(err.message, true) }
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
  } catch (err) { showMessage(err.message, true) } finally { saving.value = false }
}

watch(settings, () => {
  if (!hydrated.value || syncingSettings) return
  clearTimeout(saveTimer)
  saveTimer = setTimeout(saveSettings, 450)
}, { deep: true })

const startDownload = async () => {
  if (!newUrl.value.trim()) return
  loading.value = true
  try {
    await api('/api/download', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url: newUrl.value.trim() })
    })
    newUrl.value = ''
    showMessage('Descarga añadida a la cola')
    await fetchDownloads()
  } catch (err) { showMessage(err.message, true) } finally { loading.value = false }
}

const cancelDownload = async (id) => {
  try {
    await api('/api/cancel', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id })
    })
    await fetchDownloads()
  } catch (err) { showMessage(err.message, true) }
}

const deleteDownload = async item => {
  openConfirm({
    title: 'Borrar archivo',
    message: `¿Estás seguro de que quieres eliminar "${item.file_name}" del servidor? Esta acción no se puede deshacer.`,
    confirmText: 'Sí, borrar archivo',
    type: 'danger',
    action: async () => {
      try {
        await api(`/api/downloads/${encodeURIComponent(item.id)}`, { method: 'DELETE' })
        showMessage('Archivo borrado')
        await fetchDownloads()
      } catch (err) { showMessage(err.message, true) }
    }
  })
}

const activeDownloads = computed(() => downloads.value.filter(item => item.status === 'downloading'))
const pendingDownloads = computed(() => downloads.value.filter(item => ['pending', 'queued'].includes(item.status)))
const recentDownloads = computed(() => downloads.value.filter(item => ['completed', 'skipped', 'failed', 'cancelled'].includes(item.status)).slice(0, 12))
const completedCount = computed(() => downloads.value.filter(item => item.status === 'completed').length)
const speedText = computed(() => settings.speed_limit.value > 0 ? `${settings.speed_limit.value} ${settings.speed_limit.unit}/s` : 'Sin límite')

const statusText = status => ({ downloading: 'Descargando', queued: 'En cola', pending: 'Pendiente', completed: 'Completado', skipped: 'Omitido', failed: 'Fallido', cancelled: 'Cancelado' }[status] || status)
const progress = item => Math.max(0, Math.min(100, Number(item.progress || 0)))

onMounted(async () => {
  await Promise.all([fetchSettings(), fetchDownloads()])
  timer = setInterval(fetchDownloads, 1000)
})
onUnmounted(() => { clearInterval(timer); clearTimeout(saveTimer) })
</script>

<template>
  <div class="app-shell">
    <aside class="sidebar" :class="{ 'mobile-open': mobileMenuOpen }">
      <div class="brand"><span class="brand-mark"><img :src="logoUrl" alt="" /></span><span>Telegram<span class="brand-accent">DL</span></span><button class="mobile-menu-toggle" type="button" :aria-expanded="mobileMenuOpen" aria-label="Abrir menú" @click="mobileMenuOpen = !mobileMenuOpen"><X v-if="mobileMenuOpen" :size="20" /><Menu v-else :size="20" /></button></div>
      <p class="sidebar-copy">Centro de descargas personal</p>
      <nav class="sidebar-nav">
        <button :class="{ selected: activeView === 'downloads' }" @click="activeView = 'downloads'; mobileMenuOpen = false"><ArrowDownToLine :size="16" /> Descargas</button>
        <button :class="{ selected: activeView === 'listener' }" @click="activeView = 'listener'; mobileMenuOpen = false"><Radio :size="16" /> Escucha</button>
      </nav>
      <div class="sidebar-status"><span class="status-dot"></span><span>Servicio conectado</span></div>
      <div class="sidebar-bottom"><span class="mini-label">LÍMITE ACTUAL</span><strong>{{ settings.max_concurrent_downloads }} descargas</strong><span>{{ speedText }}</span></div>
    </aside>

    <main class="main-content">
      <header class="topbar"><div><span class="eyebrow">PANEL DE CONTROL</span><h1>{{ activeView === 'downloads' ? 'Descargas' : 'Escucha' }}</h1></div><div class="topbar-meta">Actualización automática <span class="live-dot"></span></div></header>

      <div v-if="message" class="toast success"><CheckCircle2 :size="15" /> {{ message }}</div>
      <div v-if="error" class="toast danger">{{ error }}</div>

      <template v-if="activeView === 'downloads'">

      <section class="hero-card">
        <div class="hero-copy"><span class="hero-kicker">NUEVA TAREA</span><h2>Descarga contenido de Telegram</h2><p>Pega un enlace de mensaje o un rango para comenzar.</p></div>
        <div class="download-form"><input v-model="newUrl" @keyup.enter="startDownload" placeholder="https://t.me/c/..." aria-label="Enlace de Telegram"><button class="primary-button" :disabled="loading || !newUrl.trim()" @click="startDownload"><span>{{ loading ? 'Añadiendo…' : 'Iniciar descarga' }}</span><ArrowUpRight :size="18" /></button></div>
      </section>

      <section class="stats-grid">
        <div class="stat-card"><span class="stat-icon blue"><Activity :size="19" /></span><div><span class="stat-label">ACTIVAS</span><strong>{{ activeDownloads.length }}</strong><small>de {{ settings.max_concurrent_downloads }} permitidas</small></div></div>
        <div class="stat-card"><span class="stat-icon amber"><Clock3 :size="19" /></span><div><span class="stat-label">EN COLA</span><strong>{{ pendingDownloads.length }}</strong><small>esperando turno</small></div></div>
        <div class="stat-card"><span class="stat-icon green"><CheckCircle2 :size="19" /></span><div><span class="stat-label">COMPLETADAS</span><strong>{{ completedCount }}</strong><small>en esta sesión</small></div></div>
      </section>

      <div class="content-grid">
        <section class="panel activity-panel"><div class="panel-heading"><div><span class="eyebrow">MONITOR</span><h2>Actividad en tiempo real</h2></div><span class="count-pill">{{ activeDownloads.length + pendingDownloads.length }} tareas</span></div>
          <div v-if="!activeDownloads.length && !pendingDownloads.length" class="empty-state"><Activity :size="28" /><p>No hay descargas activas</p><small>Las nuevas tareas aparecerán aquí.</small></div>
          <div v-for="item in [...activeDownloads, ...pendingDownloads]" :key="item.id" class="download-row"><div class="file-symbol"><FileDown :size="16" /></div><div class="file-info"><strong :title="item.file_name">{{ item.file_name }}</strong><span>{{ item.current_str }} / {{ item.total_str }} · {{ item.speed }}</span><div class="progress-track"><div class="progress-fill" :style="{ width: `${progress(item)}%` }"></div></div></div><div class="row-side"><b>{{ progress(item).toFixed(0) }}%</b><span>{{ statusText(item.status) }}</span><button @click="cancelDownload(item.id)"><X :size="12" /> Cancelar</button></div></div>
        </section>

        <aside class="panel settings-panel"><div class="panel-heading"><div><span class="eyebrow"><Settings2 :size="12" /> PREFERENCIAS</span><h2>Configuración</h2></div><span class="save-state">{{ saving ? 'Guardando…' : 'Auto-guardado' }}</span></div>
          <label class="setting-label">Descargas simultáneas <output>{{ settings.max_concurrent_downloads }}</output></label><input v-model.number="settings.max_concurrent_downloads" type="range" min="1" max="16" class="range-input"><div class="range-hints"><span>1</span><span>16</span></div>
          <div class="setting-line"><div><strong>Partes simultáneas</strong><small>Acelera cada archivo usando varios bloques.</small></div><label class="switch"><input v-model="settings.parallel_chunks" type="checkbox"><span></span></label></div>
          <label class="setting-label compact">Workers por archivo <output>{{ settings.chunk_workers }}</output></label><input v-model.number="settings.chunk_workers" :disabled="!settings.parallel_chunks" type="range" min="1" max="8" class="range-input"><div class="range-hints"><span>1</span><span>8</span></div>
          <div class="speed-setting"><label class="setting-label compact">Límite global</label><div class="speed-row"><input v-model.number="settings.speed_limit.value" type="number" min="0" step="0.5"><select v-model="settings.speed_limit.unit"><option>KB</option><option>MB</option><option>GB</option></select><span>/s</span></div><small>Usa 0 para quitar el límite.</small></div>
          <FolderPicker v-model="settings.download_folder" />
          <button class="save-button" :disabled="saving" @click="saveSettings"><Save :size="15" /> {{ saving ? 'Guardando…' : 'Guardar ahora' }}</button>
        </aside>
      </div>

      <section class="panel recent-panel"><div class="panel-heading"><div><span class="eyebrow">HISTORIAL</span><h2>Últimas descargas</h2></div></div><div v-if="!recentDownloads.length" class="empty-small">Todavía no hay descargas terminadas.</div><div v-for="item in recentDownloads" :key="item.id" class="recent-row"><span class="recent-icon" :class="item.status">{{ item.status === 'completed' ? '✓' : '•' }}</span><strong>{{ item.file_name }}</strong><span class="recent-size">{{ item.total_str }}</span><span class="badge" :class="item.status">{{ statusText(item.status) }}</span><button v-if="item.status === 'completed'" class="delete-button" type="button" title="Borrar archivo" aria-label="Borrar archivo" @click="deleteDownload(item)"><Trash2 :size="14" /></button></div></section>
      </template>
      <ListenerView v-else :notify="showMessage" />
      <ConfirmModal
        :show="modal.show"
        :title="modal.title"
        :message="modal.message"
        :confirmText="modal.confirmText"
        :type="modal.type"
        @confirm="handleConfirm"
        @cancel="modal.show = false"
      />
      <footer>TelegramDL · Configuración persistida localmente en JSON · {{ host }}</footer>
    </main>
  </div>
</template>

<style>
.sidebar-nav{display:flex;flex-direction:column;gap:6px;margin-bottom:24px}.sidebar-nav button{border:0;background:transparent;color:#7890a7;text-align:left;padding:11px 12px;border-radius:9px;font:500 12px 'DM Sans';cursor:pointer}.sidebar-nav button span{display:inline-block;width:22px;color:#5d83a2;font-size:16px}.sidebar-nav button:hover,.sidebar-nav button.selected{background:#102b42;color:#eef7ff}.sidebar-nav button.selected span{color:#55bdff}
</style>
