<script setup>
import { computed, onMounted, onUnmounted, ref, reactive, watch } from 'vue'
import { Download, FileText, Folder, Image, Inbox, MessageCircle, Music, Plus, Radio, Trash2, Video, Settings2 } from '../icons'
import ConfirmModal from '../components/ConfirmModal.vue'
import { useAuthToken } from '../composables/useAuthToken'

const { authHeaders } = useAuthToken()

const props = defineProps({
  notify: { type: Function, default: () => {} },
  disk: { type: Object, default: null },
  initialItems: { type: Array, default: () => [] },
  settings: { type: Object, default: () => ({ listener_enabled: true, listener_chats: [] }) },
  active: { type: Boolean, default: true }
})

// Estado local para el menú de selección de nombre por archivo
const nameSelectionMenus = ref({})

const enabled = ref(props.settings.listener_enabled)
const chats = ref(props.settings.listener_chats || [])
const newChatId = ref('')
// Un grupo con temas se añade en dos pasos: primero se resuelve el grupo y
// después se elige si se escucha entero o solo uno de sus temas.
const topicPicker = reactive({ visible: false, loading: false, chat: null, topics: [], selected: 'all' })
const items = ref(props.initialItems)
const saving = ref(false)
const error = ref('')
let timer
let disposed = false

watch(() => props.active, (newActive) => {
  if (!newActive) {
    nameSelectionMenus.value = {}
  }
})

watch(() => props.settings.listener_enabled, (newVal) => {
  if (!saving.value) enabled.value = newVal
})

watch(() => props.settings.listener_chats, (newVal) => {
  if (!saving.value) {
    chats.value = (newVal || []).map(chat => ({
      ...chat,
      auto_download: !!chat.auto_download,
      manual_name_selection: !!chat.manual_name_selection,
      f_photos: chat.f_photos ?? true,
      f_videos: chat.f_videos ?? true,
      f_audios: chat.f_audios ?? true,
      f_docs: chat.f_docs ?? true,
      f_stickers: chat.f_stickers ?? true
    }))
  }
}, { deep: true })

watch(() => props.initialItems, (newItems) => {
  items.value = newItems
}, { deep: true })

// Una entrada de escucha se identifica por el grupo Y el tema: el mismo grupo
// puede aparecer varias veces, una por cada tema vigilado.
const chatKey = chat => (chat && chat.topic_id ? `${chat.id}:${chat.topic_id}` : String(chat ? chat.id : ''))

const topicLabel = chat => {
  if (!chat || !chat.topic_id) return ''
  return (chat.topic_name || '').trim() || `Tema ${chat.topic_id}`
}

// Primero el nombre del tema y después el del grupo, para saber a dónde pertenece.
const chatLabel = chat => {
  const group = ((chat && chat.name) || '').trim() || String(chat ? chat.id : '')
  const topic = topicLabel(chat)
  return topic ? `${topic} · ${group}` : group
}

const chatMeta = chat => (chat && chat.topic_id ? `${chat.id} · tema ${chat.topic_id}` : String(chat ? chat.id : ''))

// Carpeta de descarga asignada a este chat. El backend la fija con el primer
// archivo que llega, así que hasta entonces no hay nada que enseñar.
const chatFolder = chat => {
  if (!chat || props.settings.organize_by_chat === false) return ''
  const base = (chat.folder || '').trim()
  if (!base) return ''
  const tema = (chat.topic_folder || '').trim()
  return chat.topic_id && tema ? `${base} / ${tema}` : base
}

// Acepta un ID numérico o un enlace privado https://t.me/c/<grupo>/<tema>[/<mensaje>].
const parseChatInput = raw => {
  const value = (raw || '').trim()
  if (!value) return null

  const link = value.match(/t\.me\/c\/(\d+)\/(\d+)(?:\/(\d+))?/i)
  if (link) {
    const id = Number(`-100${link[1]}`)
    const topic = Number(link[2])
    return { id, topic: Number.isInteger(topic) && topic > 0 ? topic : 0 }
  }

  const id = Number(value)
  if (!Number.isInteger(id) || id === 0) return null
  return { id, topic: 0 }
}

const api = async (url, options = {}) => {
  const response = await fetch(url, { ...options, headers: { ...(options.headers || {}), ...authHeaders() } })
  const data = await response.json().catch(() => ({}))
  if (!response.ok) throw new Error(data.detail || data.error || 'Error en el servidor')
  return data
}

const load = async () => {
  if (saving.value || disposed) return
  try {
    const [settings, detected] = await Promise.all([api('/api/listener/settings'), api('/api/listener')])
    enabled.value = settings.enabled
    chats.value = (settings.chats || []).map(chat => ({
      ...chat,
      auto_download: !!chat.auto_download,
      manual_name_selection: !!chat.manual_name_selection,
      f_photos: chat.f_photos ?? true,
      f_videos: chat.f_videos ?? true,
      f_audios: chat.f_audios ?? true,
      f_docs: chat.f_docs ?? true,
      f_stickers: chat.f_stickers ?? true
    }))
    items.value = detected
    error.value = ''
  } catch (e) { console.error(e) }
}

const save = async () => {
  if (saving.value) return
  saving.value = true
  try {
    const data = await api('/api/listener/settings', {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled: !!enabled.value, chats: chats.value })
    })
    if (data.enabled !== undefined) enabled.value = data.enabled
    if (Array.isArray(data.chats)) {
      chats.value = data.chats.map(chat => ({
        ...chat,
        auto_download: !!chat.auto_download,
        manual_name_selection: !!chat.manual_name_selection,
        f_photos: chat.f_photos ?? true,
        f_videos: chat.f_videos ?? true,
        f_audios: chat.f_audios ?? true,
        f_docs: chat.f_docs ?? true,
        f_stickers: chat.f_stickers ?? true
      }))
    }
    error.value = ''
    props.notify('Configuración de escucha guardada')
  } catch (err) { props.notify(err.message, true) } finally { saving.value = false }
}

const closeTopicPicker = () => {
  topicPicker.visible = false
  topicPicker.loading = false
  topicPicker.chat = null
  topicPicker.topics = []
  topicPicker.selected = 'all'
}

const openTopicPicker = async (chat, preselect) => {
  topicPicker.chat = chat
  topicPicker.topics = []
  topicPicker.selected = 'all'
  topicPicker.visible = true
  topicPicker.loading = true
  error.value = ''
  try {
    const data = await api(`/api/listener/topics?chat_id=${chat.id}`)
    topicPicker.topics = data.topics || []
    // Si el enlace pegado ya traía un tema, viene marcado de entrada.
    if (preselect && topicPicker.topics.some(topic => Number(topic.id) === Number(preselect))) {
      topicPicker.selected = String(preselect)
    }
  } catch (err) {
    props.notify(err.message, true)
  } finally {
    topicPicker.loading = false
  }
}

const addResolvedChat = async chat => {
  const entry = { ...chat }
  delete entry.is_forum
  if (!entry.topic_id) {
    delete entry.topic_id
    delete entry.topic_name
  }
  if (chats.value.some(existing => chatKey(existing) === chatKey(entry))) {
    error.value = entry.topic_id ? 'Ese tema ya está configurado' : 'Ese chat ya está configurado'
    return
  }
  chats.value = [...chats.value, entry]
  newChatId.value = ''
  error.value = ''
  closeTopicPicker()
  await save()
}

const addChat = async () => {
  const parsed = parseChatInput(newChatId.value)
  if (!parsed) { error.value = 'Escribe el ID numérico del chat o un enlace https://t.me/c/...'; return }

  let chat
  try {
    const data = await api(`/api/listener/chat/${parsed.id}`)
    chat = data.chat
  } catch (err) { props.notify(err.message, true); return }

  // Solo los grupos con temas ofrecen la elección; el resto se añade directo.
  if (chat && chat.is_forum) {
    await openTopicPicker(chat, parsed.topic)
    return
  }
  await addResolvedChat({ ...chat, topic_id: parsed.topic || 0 })
}

const confirmTopicSelection = async () => {
  const chat = topicPicker.chat
  if (!chat) return
  if (topicPicker.selected === 'all') {
    await addResolvedChat({ ...chat, topic_id: 0 })
    return
  }
  const topicId = Number(topicPicker.selected)
  const topic = topicPicker.topics.find(t => Number(t.id) === topicId)
  await addResolvedChat({ ...chat, topic_id: topicId, topic_name: topic ? topic.name : '' })
}

const removeChat = async chat => {
  const key = chatKey(chat)
  chats.value = chats.value.filter(existing => chatKey(existing) !== key)
  await save()
}
const toggle = async () => { await save() }
const download = async item => {
  try {
    item.status = 'queued'
    await api('/api/listener/download', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: item.id })
    })
    props.notify('Descarga añadida a la cola')
    await load()
  } catch (err) {
    props.notify(err.message, true)
    await load()
  }
}

const removeItem = async item => {
  try {
    items.value = items.value.filter(i => i.id !== item.id)
    await api('/api/listener/delete', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: item.id })
    })
    props.notify('Multimedia descartada')
    await load()
  } catch (err) {
    props.notify(err.message, true)
    await load()
  }
}

const modal = reactive({
  show: false,
  title: '',
  message: '',
  confirmText: '',
  cancelText: 'Cancelar',
  type: 'primary',
  action: null
})

const openConfirm = (config) => {
  modal.title = config.title
  modal.message = config.message
  modal.confirmText = config.confirmText
  modal.cancelText = config.cancelText !== undefined ? config.cancelText : 'Cancelar'
  modal.type = config.type || 'primary'
  modal.action = config.action
  modal.show = true
}

const handleConfirm = () => {
  if (modal.action) modal.action()
  modal.show = false
}

const downloadAll = async () => {
  const availableItems = items.value.filter(item => item.status === 'available')
  if (!availableItems.length) {
    props.notify('No hay elementos pendientes por descargar')
    return
  }

  openConfirm({
    title: 'Descargar todo',
    message: `¿Estás seguro de que quieres añadir ${availableItems.length} archivos a la cola de descarga?`,
    confirmText: 'Sí, descargar todo',
    action: async () => {
      let successCount = 0
      for (const item of availableItems) {
        try {
          item.status = 'queued'
          await api('/api/listener/download', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ id: item.id })
          })
          successCount++
        } catch (err) {
          console.error(`Error descargando ${item.id}:`, err)
        }
      }
      props.notify(`${successCount} descargas añadidas a la cola`)
      await load()
    }
  })
}

const clearAll = async () => {
  if (!items.value.length) {
    props.notify('La lista ya está vacía')
    return
  }

  openConfirm({
    title: 'Limpiar lista',
    message: '¿Estás seguro de que quieres eliminar todos los elementos detectados? Esta acción no borrará los archivos ya descargados.',
    confirmText: 'Limpiar lista',
    type: 'danger',
    action: async () => {
      try {
        items.value = []
        await api('/api/listener/clear', { method: 'POST' })
        props.notify('Lista de escucha limpiada')
        await load()
      } catch (err) {
        props.notify(err.message, true)
        await load()
      }
    }
  })
}
const statusText = status => ({ available: 'Disponible', queued: 'En cola', downloading: 'Descargando', completed: 'Completado', failed: 'Fallido' }[status] || status)
const availableCount = computed(() => items.value.filter(item => item.status === 'available').length)

const getMediaKind = item => {
  if (['photo', 'video', 'song', 'file'].includes(item.kind)) return item.kind
  const name = (item.file_name || '').toLowerCase()
  if (/\.(jpg|jpeg|png|gif|webp|bmp|heic|svg)$/i.test(name)) return 'photo'
  if (/\.(mp4|mkv|webm|avi|mov|flv|wmv|m4v)$/i.test(name)) return 'video'
  if (/\.(mp3|m4a|flac|wav|ogg|opus|aac|wma)$/i.test(name)) return 'song'
  return 'file'
}

const mediaMeta = kind => {
  switch (kind) {
    case 'photo':
      return { label: 'Foto', icon: Image, class: 'media-photo' }
    case 'video':
      return { label: 'Vídeo', icon: Video, class: 'media-video' }
    case 'song':
      return { label: 'Canción', icon: Music, class: 'media-song' }
    case 'file':
    default:
      return { label: 'Archivo', icon: FileText, class: 'media-file' }
  }
}

const getChatName = item => {
  if (item.chat_name && item.chat_name !== String(item.chat_id)) return item.chat_name
  const found = chats.value.find(c => String(c.id) === String(item.chat_id) && Number(c.topic_id || 0) === Number(item.topic_id || 0))
    || chats.value.find(c => String(c.id) === String(item.chat_id))
  if (found && found.name && found.name !== String(found.id)) return chatLabel(found)
  return item.chat_name || item.chat_id
}

const hasManualNameSelection = item => {
  const found = chats.value.find(c => String(c.id) === String(item.chat_id) && Number(c.topic_id || 0) === Number(item.topic_id || 0))
    || chats.value.find(c => String(c.id) === String(item.chat_id))
  return !!(found && found.manual_name_selection)
}

const toggleNameMenu = (itemId) => {
  nameSelectionMenus.value[itemId] = !nameSelectionMenus.value[itemId]
}

const selectFileName = async (item, selectedName) => {
  try {
    await api('/api/listener/update-filename', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: item.id, file_name: selectedName })
    })
    item.file_name = selectedName
    nameSelectionMenus.value[item.id] = false
    props.notify('Nombre de archivo actualizado')
  } catch (err) {
    props.notify(err.message, true)
  }
}

// Rastrear activación del switch de selección manual
const handleManualNameToggle = (chat) => {
  save()
}

const handleClickOutside = (e) => {
  if (!e.target.closest('.name-select-wrapper')) {
    nameSelectionMenus.value = {}
  }
}

onMounted(async () => {
  disposed = false;
  window.addEventListener('click', handleClickOutside);
  await load();
  timer = setInterval(load, 15000); // Polling mucho más lento, el socket de App.vue ya trae los datos
})
onUnmounted(() => {
  disposed = true;
  window.removeEventListener('click', handleClickOutside);
  nameSelectionMenus.value = {};
  clearInterval(timer);
})
</script>

<template>
  <section class="listener-view">
    <section class="listener-hero"><div><span class="hero-kicker"><Radio :size="13" /> MONITOR DE MENSAJES</span><h2>Escucha multimedia en tiempo real</h2><p>Cuando llegue un archivo a uno de tus chats, aparecerá aquí listo para descargar.</p></div><label class="switch large"><input v-model="enabled" type="checkbox" @change="toggle"><span></span><b>{{ enabled ? 'Escucha activa' : 'Escucha pausada' }}</b></label></section>
    <div class="listener-grid">
    <section class="panel listener-config"><div class="panel-heading"><div><span class="eyebrow"><MessageCircle :size="12" /> ORÍGENES</span><h2>Chats vigilados</h2></div><span class="count-pill">{{ chats.length }} configurados</span></div><p class="helper-text">Añade el ID numérico de un grupo, canal o chat privado, o pega un enlace <code>https://t.me/c/...</code>. Si el grupo usa temas podrás escuchar solo uno de ellos en lugar del grupo entero.</p><div class="listener-add"><input v-model="newChatId" @keyup.enter="addChat" placeholder="Ej. -1001234567890 o https://t.me/c/1234567890/57"><button class="save-button" :disabled="saving" @click="addChat"><Plus :size="15" /> Añadir</button></div>
        <div v-if="error" class="listener-error">{{ error }}</div>
        <div v-if="topicPicker.visible" class="topic-picker">
          <div class="topic-picker-head">
            <strong>{{ topicPicker.chat?.name }}</strong>
            <small>Este grupo usa temas. Elige qué quieres escuchar.</small>
          </div>
          <div v-if="topicPicker.loading" class="empty-small">Cargando temas…</div>
          <select v-else v-model="topicPicker.selected" class="topic-select">
            <option value="all">Todo el grupo</option>
            <option v-for="topic in topicPicker.topics" :key="topic.id" :value="String(topic.id)">{{ topic.name || ('Tema ' + topic.id) }}{{ topic.closed ? ' (cerrado)' : '' }}</option>
          </select>
          <div class="topic-picker-actions">
            <button class="save-button" :disabled="saving || topicPicker.loading" @click="confirmTopicSelection"><Plus :size="14" /> Añadir</button>
            <button class="ghost-button" :disabled="saving" @click="closeTopicPicker">Cancelar</button>
          </div>
        </div><div v-if="!chats.length" class="empty-small">No hay chats configurados.</div>
        <div v-for="chat in chats" :key="chatKey(chat)" class="chat-chip">
          <div class="chat-chip-main">
            <MessageCircle :size="14" />
            <div class="chat-details">
              <strong>{{ chatLabel(chat) }}</strong>
              <small>{{ chatMeta(chat) }}</small>
              <small v-if="chatFolder(chat)" class="chat-carpeta"><Folder :size="10" /> {{ chatFolder(chat) }}</small>
            </div>
            <label class="auto-toggle switch" :class="{ disabled: saving }">
              <input type="checkbox" v-model="chat.auto_download" :disabled="saving" @change="save">
              <span></span>
              <b>Auto</b>
            </label>
            <label class="auto-toggle switch" :class="{ disabled: saving }" title="Activar selección manual de nombres">
              <input type="checkbox" v-model="chat.manual_name_selection" :disabled="saving" @change="handleManualNameToggle(chat)">
              <span></span>
              <b>Nombre</b>
            </label>
            <button :disabled="saving" @click="removeChat(chat)" aria-label="Eliminar chat"><Trash2 :size="14" /></button>
          </div>
          <div class="chat-filters">
            <label class="filter-tag f-photos" :class="{ active: chat.f_photos }"><input type="checkbox" v-model="chat.f_photos" :disabled="saving" @change="save"><span>Fotos</span></label>
            <label class="filter-tag f-videos" :class="{ active: chat.f_videos }"><input type="checkbox" v-model="chat.f_videos" :disabled="saving" @change="save"><span>Videos</span></label>
            <label class="filter-tag f-audios" :class="{ active: chat.f_audios }"><input type="checkbox" v-model="chat.f_audios" :disabled="saving" @change="save"><span>Audios</span></label>
            <label class="filter-tag f-docs" :class="{ active: chat.f_docs }"><input type="checkbox" v-model="chat.f_docs" :disabled="saving" @change="save"><span>Docs</span></label>
            <label class="filter-tag f-stickers" :class="{ active: chat.f_stickers }"><input type="checkbox" v-model="chat.f_stickers" :disabled="saving" @change="save"><span>Stickers</span></label>
          </div>
        </div>
        <small class="save-hint">Los nombres y reglas se guardan automáticamente.</small></section>
      <section class="panel listener-feed">
        <div class="panel-heading">
          <div>
            <span class="eyebrow"><Inbox :size="12" /> BANDEJA DE ENTRADA</span>
            <h2>Multimedia detectada</h2>
          </div>
          <div class="header-actions">
            <div v-if="disk" class="disk-monitor">
              <div class="disk-bar"><div class="fill" :style="{ width: disk.percent + '%', backgroundColor: disk.status === 'red' ? '#ff4d4d' : '#4dff4d' }"></div></div>
              <small>Total/Libre: ({{ disk.total_str }} / {{ disk.projected_free_str }})</small>
            </div>
            <span class="count-pill">{{ availableCount }} nuevas</span>
            <div v-if="items.length" class="bulk-actions">
              <button class="bulk-download" title="Descargar todo lo disponible" @click="downloadAll">
                <Download :size="14" /> Todo
              </button>
              <button class="bulk-delete" title="Limpiar lista completa" @click="clearAll">
                <Trash2 :size="14" /> Limpiar
              </button>
            </div>
          </div>
        </div>
        <div v-if="!items.length" class="empty-state">
          <Inbox :size="28" />
          <p>Aún no se detectó multimedia</p>
          <small>Deja esta vista abierta o vuelve cuando llegue un archivo.</small>
        </div>
        <div v-for="item in items" :key="item.id" class="listener-item">
          <div class="file-symbol" :class="mediaMeta(getMediaKind(item)).class" :title="mediaMeta(getMediaKind(item)).label">
            <component :is="mediaMeta(getMediaKind(item)).icon" :size="16" />
          </div>
          <div class="file-info">
            <strong :title="item.file_name">{{ item.file_name }}</strong>
            <span>{{ getChatName(item) }} · mensaje {{ item.message_id }} · {{ item.total_str }}</span>
          </div>
          <div class="row-side">
            <span class="listener-status">{{ statusText(item.status) }}</span>
            <div class="row-actions">
              <div v-if="hasManualNameSelection(item) && item.status === 'available'" class="name-select-wrapper">
                <button class="name-select-button" @click="toggleNameMenu(item.id)">
                  <Settings2 :size="13" /> Nombre
                </button>
                <div v-if="nameSelectionMenus[item.id]" class="name-dropdown">
                  <div v-if="item.caption_file_name && item.caption_file_name !== item.file_name"
                       class="name-option"
                       @click="selectFileName(item, item.caption_file_name)">
                    <span class="name-preview">{{ item.caption_file_name }}</span>
                    <small>(Caption)</small>
                  </div>
                  <div v-if="item.original_file_name && item.original_file_name !== item.file_name"
                       class="name-option"
                       @click="selectFileName(item, item.original_file_name)">
                    <span class="name-preview">{{ item.original_file_name }}</span>
                    <small>(Original)</small>
                  </div>
                </div>
              </div>
              <button v-if="item.status === 'available'" class="download-small" @click="download(item)">
                <Download :size="13" /> Descargar
              </button>
              <button class="delete-small" @click="removeItem(item)" aria-label="Eliminar">
                <Trash2 :size="13" />
              </button>
            </div>
          </div>
        </div>
      </section>
    </div>
    <ConfirmModal
      :show="modal.show"
      :title="modal.title"
      :message="modal.message"
      :confirmText="modal.confirmText"
      :cancelText="modal.cancelText"
      :type="modal.type"
      @confirm="handleConfirm"
      @cancel="modal.show = false"
    />
  </section>
</template>

<style scoped>
.listener-view{display:flex;flex-direction:column;gap:18px}.listener-hero{display:flex;justify-content:space-between;align-items:center;gap:20px;padding:28px 30px;border:1px solid var(--user-border-light);border-radius:18px;background:linear-gradient(110deg,var(--user-surface-light),var(--user-surface) 70%)}.listener-hero h2{font:600 23px 'Space Grotesk';margin:8px 0 5px;color:#f1f7ff}.listener-hero p,.helper-text{margin:0;color:var(--user-text-dim);font-size:13px}.switch.large{display:flex;align-items:center;gap:10px;white-space:nowrap}.switch.large b{font-size:12px;color:var(--user-accent);font-weight:500}.listener-grid{display:grid;grid-template-columns:.82fr 1.18fr;gap:18px;min-width:0}.listener-grid > section{min-width:0}.helper-text{line-height:1.5;margin-bottom:17px}.listener-add{display:flex;gap:8px}.listener-add input{flex:1;min-width:0;background:var(--user-bg-base);border:1px solid var(--user-border-light);color:#dbe7f5;border-radius:10px;padding:11px 12px;outline:none;font:inherit}.listener-add .save-button{width:auto;margin:0;padding:0 15px}.chat-chip{display:flex;flex-direction:column;gap:8px;border-top:1px solid var(--user-border);padding:14px 0}
.chat-chip-main{display:flex;align-items:center;gap:9px;color:var(--user-primary);font-size:12px}
.chat-filters{display:flex;flex-wrap:wrap;gap:8px;padding-left:23px;margin-top:4px}
.filter-tag{display:flex;align-items:center;padding:4px 12px;border-radius:20px;font-size:10px;font-weight:700;color:var(--user-text-dim);cursor:pointer;background:var(--user-bg-base);border:1px solid var(--user-border-light);transition:all .25s cubic-bezier(0.4, 0, 0.2, 1);user-select:none;text-transform:uppercase;letter-spacing:0.5px}
.filter-tag input{display:none}
.filter-tag:hover{border-color:var(--user-primary);transform:translateY(-1px)}
.filter-tag.active{color:#000;border-color:transparent;box-shadow:0 4px 10px rgba(0,0,0,0.3);transform:translateY(-1px)}
.filter-tag.f-photos.active{background:#38bdf8;box-shadow:0 4px 12px rgba(56,189,248,0.3)}
.filter-tag.f-videos.active{background:#c084fc;box-shadow:0 4px 12px rgba(192,132,252,0.3)}
.filter-tag.f-audios.active{background:#4ade80;box-shadow:0 4px 12px rgba(74,222,128,0.3)}
.filter-tag.f-docs.active{background:#94a3b8;box-shadow:0 4px 12px rgba(148,163,184,0.3)}
.filter-tag.f-stickers.active{background:#f472b6;box-shadow:0 4px 12px rgba(244,114,182,0.3)}
.chat-chip strong{flex:1;color:#d6e4f1;font-weight:500}
.chat-chip button{border:0;background:transparent;color:#e58b91;font-size:20px;cursor:pointer}
.save-hint{display:block;color:var(--user-text-dim);font-size:10px;margin-top:13px}.panel-heading{display:flex;justify-content:space-between;align-items:flex-start}.header-actions{display:flex;flex-direction:column;align-items:flex-end;gap:10px}.bulk-actions{display:flex;gap:6px}.bulk-download,.bulk-delete{border:1px solid var(--user-border-light);background:var(--user-bg-base);color:#dbe7f5;border-radius:6px;padding:4px 8px;font-size:11px;cursor:pointer;display:flex;align-items:center;gap:4px;transition:all .2s}.bulk-download:hover{background:var(--user-icon-bg);border-color:var(--user-primary);color:var(--user-accent)}.bulk-delete:hover{background:#251415;border-color:#4a2b2d;color:#e58b91}.listener-item{display:flex;align-items:center;gap:12px;border-top:1px solid var(--user-border);padding:13px 0;position:relative}.listener-item.menu-open{z-index:50}.file-info{flex:1;min-width:0;overflow:hidden}.file-info strong,.file-info span{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.row-side{display:flex;flex-direction:column;align-items:flex-end;gap:5px;margin-left:auto;flex-shrink:0}.row-actions{display:flex;align-items:center;gap:6px;flex-shrink:0;position:relative}.listener-status{font-size:10px;color:var(--user-text-dim)}.download-small{border:1px solid var(--user-primary);background:var(--user-icon-bg);color:var(--user-primary);border-radius:7px;padding:6px 9px;font-size:10px;cursor:pointer;display:flex;align-items:center;gap:4px}.download-small:hover{background:var(--user-surface-light)}.delete-small{border:1px solid #4a2b2d;background:#251415;color:#e58b91;border-radius:7px;padding:6px 9px;font-size:10px;cursor:pointer;display:flex;align-items:center;justify-content:center}.delete-small:hover{background:#3a1d1f}@media(max-width:900px){.listener-grid{grid-template-columns:1fr}}@media(max-width:580px){.listener-hero{align-items:flex-start;flex-direction:column;padding:22px}.listener-add{flex-direction:column}.listener-add .save-button{height:38px}.listener-item .row-side{min-width:75px}}
.listener-error{margin-top:10px;color:#e58b91;font-size:11px}
.topic-picker{margin-top:12px;padding:13px;border:1px solid var(--user-border-light);border-radius:12px;background:var(--user-bg-base);display:flex;flex-direction:column;gap:10px}
.topic-picker-head{display:flex;flex-direction:column;gap:3px}
.topic-picker-head strong{color:#d6e4f1;font-size:13px;font-weight:500}
.topic-picker-head small{color:var(--user-text-dim);font-size:11px}
.topic-select{width:100%;background:var(--user-surface);border:1px solid var(--user-border-light);color:#dbe7f5;border-radius:9px;padding:9px 10px;outline:none;font:inherit;font-size:12px}
.topic-picker-actions{display:flex;gap:8px}
.topic-picker-actions .save-button{width:auto;margin:0;padding:0 15px}
.ghost-button{border:1px solid var(--user-border-light);background:transparent;color:var(--user-text-dim);border-radius:9px;padding:0 15px;font:inherit;font-size:12px;cursor:pointer}
.ghost-button:hover{color:#dbe7f5;border-color:var(--user-primary)}
.chat-details{flex:1;min-width:0;display:flex;flex-direction:column;gap:3px}.chat-details strong{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.chat-details small{color:var(--user-text-dim);font-size:10px}.chat-carpeta{display:flex;align-items:center;gap:4px;color:var(--user-accent);overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.chat-carpeta svg{flex:none}.auto-toggle{display:flex;align-items:center;gap:7px;color:var(--user-text-dim);cursor:pointer;white-space:nowrap}.auto-toggle span{flex:none}.auto-toggle b{font-size:10px;font-weight:700;letter-spacing:.4px;text-transform:uppercase;color:var(--user-text-dim);transition:color .2s}.auto-toggle input:checked+span+b{color:var(--user-accent)}.auto-toggle.disabled{cursor:default;opacity:.6}
.file-symbol{width:36px;height:36px;border-radius:9px;display:flex;align-items:center;justify-content:center;border:1px solid var(--user-border);flex-shrink:0;transition:all .2s ease}
.media-badge{display:inline-flex;align-items:center;padding:2px 6px;border-radius:4px;font-size:10px;font-weight:600;text-transform:uppercase;letter-spacing:.3px;margin-right:6px;border:1px solid transparent;vertical-align:middle}
.media-photo{color:#38bdf8;background:rgba(56,189,248,.12);border-color:rgba(56,189,248,.3)}
.media-video{color:#c084fc;background:rgba(192,132,252,.12);border-color:rgba(192,132,252,.3)}
.media-song{color:#4ade80;background:rgba(74,222,128,.12);border-color:rgba(74,222,128,.3)}
.media-file{color:#94a3b8;background:rgba(148,163,184,.12);border-color:rgba(148,163,184,.3)}.name-select-wrapper{position:relative;display:inline-flex}.name-select-button{border:1px solid var(--user-primary);background:var(--user-icon-bg);color:var(--user-primary);border-radius:7px;padding:6px 10px;font-size:10px;cursor:pointer;display:flex;align-items:center;gap:4px;transition:all .2s;font-weight:600}.name-select-button:hover{background:var(--user-surface-light);border-color:var(--user-accent);color:var(--user-accent);transform:translateY(-1px)}.name-dropdown{position:absolute;right:0;top:calc(100% + 8px);background:var(--user-surface);border:2px solid var(--user-primary);border-radius:10px;padding:8px;min-width:220px;max-width:360px;z-index:1000;box-shadow:0 8px 24px rgba(0,0,0,0.4);animation:fadeIn .2s ease-out}.name-dropdown::before{content:'';position:absolute;top:-6px;right:20px;width:12px;height:12px;background:var(--user-surface);border-left:2px solid var(--user-primary);border-top:2px solid var(--user-primary);transform:rotate(45deg)}.name-option{padding:10px 12px;border-radius:8px;cursor:pointer;display:flex;flex-direction:column;gap:4px;transition:all .2s;border:1px solid transparent}.name-option:hover{background:var(--user-icon-bg);border-color:var(--user-primary);transform:translateX(-2px)}.name-option .name-preview{font-size:12px;color:#d6e4f1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-weight:500}.name-option small{font-size:10px;color:var(--user-accent);font-weight:600;text-transform:uppercase;letter-spacing:.5px}@keyframes fadeIn{from{opacity:0;transform:translateY(-4px)}to{opacity:1;transform:translateY(0)}}
</style>
