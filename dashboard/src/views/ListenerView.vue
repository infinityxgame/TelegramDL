<script setup>
import { computed, onMounted, onUnmounted, ref, reactive, watch } from 'vue'
import {
  Download,
  FileText,
  Folder,
  Image,
  Inbox,
  Menu,
  MessageCircle,
  Music,
  Plus,
  Radio,
  Trash2,
  Video,
  Settings2
} from '../icons'
import ConfirmModal from '../components/ConfirmModal.vue'
import AppPill from '../components/AppPill.vue'
import { useAuthToken } from '../composables/useAuthToken'
import { useI18n } from '../i18n'

const { authHeaders } = useAuthToken()
const { t, splitOn, has } = useI18n()

const props = defineProps({
  notify: { type: Function, default: () => {} },
  disk: { type: Object, default: null },
  initialItems: { type: Array, default: () => [] },
  settings: {
    type: Object,
    default: () => ({ listener_enabled: true, listener_chats: [] })
  },
  active: { type: Boolean, default: true }
})

// Estado local para el menú de selección de nombre por archivo
const nameSelectionMenus = ref({})
const bulkNameMenuOpen = ref(false)
const chatNameModeMenus = ref({})

const enabled = ref(props.settings.listener_enabled)
const chats = ref(props.settings.listener_chats || [])
const newChatId = ref('')
// Un grupo con temas se añade en dos pasos: primero se resuelve el grupo y
// después se elige si se escucha entero o solo uno de sus temas.
const topicPicker = reactive({
  visible: false,
  loading: false,
  chat: null,
  topics: [],
  selected: 'all'
})
const items = ref(props.initialItems)
const saving = ref(false)
const error = ref('')
let timer
let disposed = false

watch(
  () => props.active,
  (newActive) => {
    if (!newActive) {
      nameSelectionMenus.value = {}
      bulkNameMenuOpen.value = false
      chatNameModeMenus.value = {}
    }
  }
)

watch(
  () => props.settings.listener_enabled,
  (newVal) => {
    if (!saving.value) enabled.value = newVal
  }
)

watch(
  () => props.settings.listener_chats,
  (newVal) => {
    if (!saving.value) {
      chats.value = (newVal || []).map((chat) => ({
        ...chat,
        auto_download: !!chat.auto_download,
        name_mode: chat.name_mode || 'manual',
        f_photos: chat.f_photos ?? true,
        f_videos: chat.f_videos ?? true,
        f_audios: chat.f_audios ?? true,
        f_docs: chat.f_docs ?? true,
        f_stickers: chat.f_stickers ?? true
      }))
    }
  },
  { deep: true }
)

watch(
  () => props.initialItems,
  async (newItems) => {
    if (draggedIndex.value !== null) return
    items.value = newItems
    await applyAutomaticNamesAll()
  },
  { deep: true }
)

const draggedIndex = ref(null)
const dragOverIndex = ref(null)

const onDragStart = (index, event) => {
  draggedIndex.value = index
  if (event.dataTransfer) {
    event.dataTransfer.effectAllowed = 'move'
    event.dataTransfer.setData('text/plain', String(index))
  }
}

const onDragOver = (index, event) => {
  event.preventDefault()
  if (draggedIndex.value === null) return
  dragOverIndex.value = index
}

const onDragLeave = (index) => {
  if (dragOverIndex.value === index) {
    dragOverIndex.value = null
  }
}

const onDrop = async (targetIndex) => {
  const sourceIndex = draggedIndex.value
  draggedIndex.value = null
  dragOverIndex.value = null

  if (
    sourceIndex === null ||
    sourceIndex === undefined ||
    sourceIndex === targetIndex
  ) {
    return
  }

  const updatedItems = [...items.value]
  const [movedItem] = updatedItems.splice(sourceIndex, 1)
  updatedItems.splice(targetIndex, 0, movedItem)
  items.value = updatedItems

  try {
    await api('/api/listener/reorder', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ids: items.value.map((i) => i.id) })
    })
  } catch (err) {
    console.error('Error guardando orden:', err)
  }
}

const onDragEnd = () => {
  draggedIndex.value = null
  dragOverIndex.value = null
}

// Una entrada de escucha se identifica por el grupo Y el tema: el mismo grupo
// puede aparecer varias veces, una por cada tema vigilado.
const chatKey = (chat) =>
  chat && chat.topic_id
    ? `${chat.id}:${chat.topic_id}`
    : String(chat ? chat.id : '')

const topicLabel = (chat) => {
  if (!chat || !chat.topic_id) return ''
  return (
    (chat.topic_name || '').trim() ||
    t('listener.topicFallback', { id: chat.topic_id })
  )
}

// Primero el nombre del tema y después el del grupo, para saber a dónde pertenece.
const chatLabel = (chat) => {
  const group =
    ((chat && chat.name) || '').trim() || String(chat ? chat.id : '')
  const topic = topicLabel(chat)
  return topic ? `${topic} · ${group}` : group
}

const chatMeta = (chat) =>
  chat && chat.topic_id
    ? t('listener.chatMetaTopic', { id: chat.id, topic: chat.topic_id })
    : String(chat ? chat.id : '')

// El texto de ayuda lleva un enlace de ejemplo en medio; se parte por el
// marcador {link} para seguir mostrándolo con su <code> en ambos idiomas.
const helperParts = computed(() => splitOn('listener.helper', 'link'))

// Carpeta de descarga asignada a este chat. El backend la fija con el primer
// archivo que llega, así que hasta entonces no hay nada que enseñar.
const chatFolder = (chat) => {
  if (!chat || props.settings.organize_by_chat === false) return ''
  const base = (chat.folder || '').trim()
  if (!base) return ''
  const tema = (chat.topic_folder || '').trim()
  return chat.topic_id && tema ? `${base} / ${tema}` : base
}

// Acepta un ID numérico o un enlace privado https://t.me/c/<grupo>/<tema>[/<mensaje>].
const parseChatInput = (raw) => {
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
  const response = await fetch(url, {
    ...options,
    headers: { ...(options.headers || {}), ...authHeaders() }
  })
  const data = await response.json().catch(() => ({}))
  if (!response.ok)
    throw new Error(data.detail || data.error || t('common.serverError'))
  return data
}

const load = async () => {
  if (saving.value || disposed || draggedIndex.value !== null) return
  try {
    const [settings, detected] = await Promise.all([
      api('/api/listener/settings'),
      api('/api/listener')
    ])
    enabled.value = settings.enabled
    chats.value = (settings.chats || []).map((chat) => ({
      ...chat,
      auto_download: !!chat.auto_download,
      name_mode: chat.name_mode || 'manual',
      f_photos: chat.f_photos ?? true,
      f_videos: chat.f_videos ?? true,
      f_audios: chat.f_audios ?? true,
      f_docs: chat.f_docs ?? true,
      f_stickers: chat.f_stickers ?? true
    }))
    items.value = detected
    await applyAutomaticNamesAll()
    error.value = ''
  } catch (e) {
    console.error(e)
  }
}

const save = async () => {
  if (saving.value) return
  saving.value = true
  try {
    const data = await api('/api/listener/settings', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled: !!enabled.value, chats: chats.value })
    })
    if (data.enabled !== undefined) enabled.value = data.enabled
    if (Array.isArray(data.chats)) {
      chats.value = data.chats.map((chat) => ({
        ...chat,
        auto_download: !!chat.auto_download,
        name_mode: chat.name_mode || 'manual',
        f_photos: chat.f_photos ?? true,
        f_videos: chat.f_videos ?? true,
        f_audios: chat.f_audios ?? true,
        f_docs: chat.f_docs ?? true,
        f_stickers: chat.f_stickers ?? true
      }))
    }
    error.value = ''
    props.notify(t('listener.saved'))
  } catch (err) {
    props.notify(err.message, true)
  } finally {
    saving.value = false
  }
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
    if (
      preselect &&
      topicPicker.topics.some((topic) => Number(topic.id) === Number(preselect))
    ) {
      topicPicker.selected = String(preselect)
    }
  } catch (err) {
    props.notify(err.message, true)
  } finally {
    topicPicker.loading = false
  }
}

const addResolvedChat = async (chat) => {
  const entry = { ...chat }
  delete entry.is_forum
  if (!entry.topic_id) {
    delete entry.topic_id
    delete entry.topic_name
  }
  if (chats.value.some((existing) => chatKey(existing) === chatKey(entry))) {
    error.value = entry.topic_id
      ? t('listener.errTopicExists')
      : t('listener.errChatExists')
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
  if (!parsed) {
    error.value = t('listener.errInvalidInput', { link: 'https://t.me/c/...' })
    return
  }

  let chat
  try {
    const data = await api(`/api/listener/chat/${parsed.id}`)
    chat = data.chat
  } catch (err) {
    props.notify(err.message, true)
    return
  }

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
  const topic = topicPicker.topics.find((t) => Number(t.id) === topicId)
  await addResolvedChat({
    ...chat,
    topic_id: topicId,
    topic_name: topic ? topic.name : ''
  })
}

const removeChat = async (chat) => {
  const key = chatKey(chat)
  chats.value = chats.value.filter((existing) => chatKey(existing) !== key)
  await save()
}
const toggle = async () => {
  await save()
}
const download = async (item) => {
  try {
    item.status = 'queued'
    await api('/api/listener/download', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: item.id })
    })
    props.notify(t('listener.queuedToast'))
    await load()
  } catch (err) {
    props.notify(err.message, true)
    await load()
  }
}

const removeItem = async (item) => {
  try {
    items.value = items.value.filter((i) => i.id !== item.id)
    await api('/api/listener/delete', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ id: item.id })
    })
    props.notify(t('listener.discarded'))
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
  cancelText: '',
  type: 'primary',
  action: null
})

const openConfirm = (config) => {
  modal.title = config.title
  modal.message = config.message
  modal.confirmText = config.confirmText
  modal.cancelText =
    config.cancelText !== undefined ? config.cancelText : t('common.cancel')
  modal.type = config.type || 'primary'
  modal.action = config.action
  modal.show = true
}

const handleConfirm = () => {
  if (modal.action) modal.action()
  modal.show = false
}

const downloadAll = async () => {
  const availableItems = items.value.filter(
    (item) => item.status === 'available'
  )
  if (!availableItems.length) {
    props.notify(t('listener.nonePending'))
    return
  }

  openConfirm({
    title: t('listener.downloadAllModalTitle'),
    message: t('listener.downloadAllModalText', { n: availableItems.length }),
    confirmText: t('listener.downloadAllModalConfirm'),
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
      props.notify(t('listener.addedToQueue', { n: successCount }))
      await load()
    }
  })
}

const clearAll = async () => {
  if (!items.value.length) {
    props.notify(t('listener.alreadyEmpty'))
    return
  }

  openConfirm({
    title: t('listener.clearModalTitle'),
    message: t('listener.clearModalText'),
    confirmText: t('listener.clearModalConfirm'),
    type: 'danger',
    action: async () => {
      try {
        items.value = []
        await api('/api/listener/clear', { method: 'POST' })
        props.notify(t('listener.listCleared'))
        await load()
      } catch (err) {
        props.notify(err.message, true)
        await load()
      }
    }
  })
}
const statusText = (status) =>
  has('listener.status.' + status) ? t('listener.status.' + status) : status
const availableCount = computed(
  () => items.value.filter((item) => item.status === 'available').length
)

const getMediaKind = (item) => {
  if (['photo', 'video', 'song', 'file'].includes(item.kind)) return item.kind
  const name = (item.file_name || '').toLowerCase()
  if (/\.(jpg|jpeg|png|gif|webp|bmp|heic|svg)$/i.test(name)) return 'photo'
  if (/\.(mp4|mkv|webm|avi|mov|flv|wmv|m4v)$/i.test(name)) return 'video'
  if (/\.(mp3|m4a|flac|wav|ogg|opus|aac|wma)$/i.test(name)) return 'song'
  return 'file'
}

const mediaMeta = (kind) => {
  switch (kind) {
    case 'photo':
      return {
        label: t('listener.mediaPhoto'),
        icon: Image,
        class: 'media-photo'
      }
    case 'video':
      return {
        label: t('listener.mediaVideo'),
        icon: Video,
        class: 'media-video'
      }
    case 'song':
      return {
        label: t('listener.mediaSong'),
        icon: Music,
        class: 'media-song'
      }
    case 'file':
    default:
      return {
        label: t('listener.mediaFile'),
        icon: FileText,
        class: 'media-file'
      }
  }
}

const getChatName = (item) => {
  if (item.chat_name && item.chat_name !== String(item.chat_id))
    return item.chat_name
  const found =
    chats.value.find(
      (c) =>
        String(c.id) === String(item.chat_id) &&
        Number(c.topic_id || 0) === Number(item.topic_id || 0)
    ) || chats.value.find((c) => String(c.id) === String(item.chat_id))
  if (found && found.name && found.name !== String(found.id))
    return chatLabel(found)
  return item.chat_name || item.chat_id
}

const hasManualNameSelection = (item) => {
  const found =
    chats.value.find(
      (c) =>
        String(c.id) === String(item.chat_id) &&
        Number(c.topic_id || 0) === Number(item.topic_id || 0)
    ) || chats.value.find((c) => String(c.id) === String(item.chat_id))
  return !!(found && found.name_mode === 'manual')
}

const toggleNameMenu = (itemId) => {
  nameSelectionMenus.value[itemId] = !nameSelectionMenus.value[itemId]
}

const toggleBulkNameMenu = () => {
  bulkNameMenuOpen.value = !bulkNameMenuOpen.value
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
    props.notify(t('listener.nameUpdated'))
  } catch (err) {
    props.notify(err.message, true)
  }
}

const selectBulkFileName = async (type) => {
  bulkNameMenuOpen.value = false

  const eligibleItems = items.value.filter((item) => {
    if (!hasManualNameSelection(item)) return false
    if (item.status !== 'available') return false
    const targetName =
      type === 'caption' ? item.caption_file_name : item.original_file_name
    return targetName !== null && targetName !== undefined && targetName !== ''
  })

  if (!eligibleItems.length) {
    props.notify(t('listener.bulkNoneEligible'))
    return
  }

  let updatedCount = 0
  for (const item of eligibleItems) {
    const targetName =
      type === 'caption' ? item.caption_file_name : item.original_file_name
    try {
      await api('/api/listener/update-filename', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ id: item.id, file_name: targetName })
      })
      item.file_name = targetName
      updatedCount++
    } catch (err) {
      console.error(`Error actualizando nombre para ${item.id}:`, err)
    }
  }

  if (updatedCount > 0) {
    props.notify(
      t('listener.bulkUpdated', {
        n: updatedCount,
        target:
          type === 'caption'
            ? t('listener.targetCaption')
            : t('listener.targetOriginal')
      })
    )
    // Recargar los items desde el servidor para asegurar sincronización
    await load()
  }
}

const applyAutomaticNamesForChat = async (chat) => {
  if (!chat || !chat.name_mode || chat.name_mode === 'manual') return

  const targetMode = chat.name_mode
  const matchingItems = items.value.filter((item) => {
    if (item.status !== 'available') return false
    const matchChat = String(item.chat_id) === String(chat.id)
    const matchTopic = Number(chat.topic_id || 0) === Number(item.topic_id || 0)
    return matchChat && matchTopic
  })

  for (const item of matchingItems) {
    let targetName = null
    if (targetMode === 'caption' && item.caption_file_name) {
      targetName = item.caption_file_name
    } else if (targetMode === 'original' && item.original_file_name) {
      targetName = item.original_file_name
    }

    if (targetName && item.file_name !== targetName) {
      try {
        await api('/api/listener/update-filename', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id: item.id, file_name: targetName })
        })
        item.file_name = targetName
      } catch (err) {
        console.error(`Error actualizando nombre para ${item.id}:`, err)
      }
    }
  }
}

const applyAutomaticNamesAll = async () => {
  if (!chats.value || !items.value) return
  for (const chat of chats.value) {
    if (chat.name_mode && chat.name_mode !== 'manual') {
      await applyAutomaticNamesForChat(chat)
    }
  }
}

const onNameModeChange = async (chat) => {
  await save()
  await applyAutomaticNamesForChat(chat)
}

const nameModeLabel = (mode) => {
  if (mode === 'original') return t('listener.nameModeOriginal')
  if (mode === 'caption') return t('listener.nameModeCaption')
  return t('listener.nameModeManual')
}

const toggleChatNameModeMenu = (chat) => {
  const key = chatKey(chat)
  const isCurrentlyOpen = !!chatNameModeMenus.value[key]
  chatNameModeMenus.value = { [key]: !isCurrentlyOpen }
}

const selectChatNameMode = async (chat, mode) => {
  chatNameModeMenus.value = {}
  if (chat.name_mode === mode) return
  chat.name_mode = mode
  await onNameModeChange(chat)
}

const handleClickOutside = (e) => {
  if (
    !e.target.closest('.name-select-wrapper') &&
    !e.target.closest('.bulk-name-wrapper') &&
    !e.target.closest('.chat-name-mode-wrapper')
  ) {
    nameSelectionMenus.value = {}
    bulkNameMenuOpen.value = false
    chatNameModeMenus.value = {}
  }
}

onMounted(async () => {
  disposed = false
  window.addEventListener('click', handleClickOutside)
  await load()
  timer = setInterval(load, 15000) // Polling mucho más lento, el socket de App.vue ya trae los datos
})
onUnmounted(() => {
  disposed = true
  window.removeEventListener('click', handleClickOutside)
  nameSelectionMenus.value = {}
  chatNameModeMenus.value = {}
  clearInterval(timer)
})
</script>

<template>
  <section class="listener-view">
    <section class="listener-hero">
      <div>
        <span class="hero-kicker"
          ><Radio :size="13" /> {{ t('listener.kicker') }}</span
        >
        <h2>{{ t('listener.title') }}</h2>
        <p>{{ t('listener.text') }}</p>
      </div>
      <label class="switch large"
        ><input
          v-model="enabled"
          type="checkbox"
          @change="toggle"
        /><span></span
        ><b>{{
          enabled ? t('listener.active') : t('listener.paused')
        }}</b></label
      >
    </section>
    <div class="listener-grid">
      <section class="panel listener-config">
        <div class="panel-heading">
          <div>
            <span class="eyebrow"
              ><MessageCircle :size="12" />
              {{ t('listener.originsKicker') }}</span
            >
            <h2>{{ t('listener.chatsTitle') }}</h2>
          </div>
          <AppPill>{{ t('listener.chatsPill', { n: chats.length }) }}</AppPill>
        </div>
        <p class="helper-text">
          {{ helperParts[0] }}<code>https://t.me/c/...</code
          >{{ helperParts[1] }}
        </p>
        <div class="listener-add">
          <input
            v-model="newChatId"
            @keyup.enter="addChat"
            :placeholder="t('listener.placeholder')"
          /><button class="save-button" :disabled="saving" @click="addChat">
            <Plus :size="15" /> {{ t('common.add') }}
          </button>
        </div>
        <div v-if="error" class="listener-error">{{ error }}</div>
        <div v-if="topicPicker.visible" class="topic-picker">
          <div class="topic-picker-head">
            <strong>{{ topicPicker.chat?.name }}</strong>
            <small>{{ t('listener.topicHint') }}</small>
          </div>
          <div v-if="topicPicker.loading" class="empty-small">
            {{ t('listener.topicsLoading') }}
          </div>
          <select v-else v-model="topicPicker.selected" class="topic-select">
            <option value="all">{{ t('listener.wholeGroup') }}</option>
            <option
              v-for="topic in topicPicker.topics"
              :key="topic.id"
              :value="String(topic.id)"
            >
              {{ topic.name || t('listener.topicFallback', { id: topic.id })
              }}{{ topic.closed ? ' ' + t('listener.topicClosed') : '' }}
            </option>
          </select>
          <div class="topic-picker-actions">
            <button
              class="save-button"
              :disabled="saving || topicPicker.loading"
              @click="confirmTopicSelection"
            >
              <Plus :size="14" /> {{ t('common.add') }}
            </button>
            <button
              class="ghost-button"
              :disabled="saving"
              @click="closeTopicPicker"
            >
              {{ t('common.cancel') }}
            </button>
          </div>
        </div>
        <div v-if="!chats.length" class="empty-small">
          {{ t('listener.emptyChats') }}
        </div>
        <div v-for="chat in chats" :key="chatKey(chat)" class="chat-chip">
          <div class="chat-chip-main">
            <MessageCircle :size="14" />
            <div class="chat-details">
              <strong>{{ chatLabel(chat) }}</strong>
              <small>{{ chatMeta(chat) }}</small>
              <small v-if="chatFolder(chat)" class="chat-carpeta"
                ><Folder :size="10" /> {{ chatFolder(chat) }}</small
              >
            </div>
            <div
              class="auto-toggle-wrapper"
              :class="{ active: chat.auto_download }"
            >
              <label class="auto-toggle switch" :class="{ disabled: saving }">
                <input
                  type="checkbox"
                  v-model="chat.auto_download"
                  :disabled="saving"
                  @change="save"
                />
                <span></span>
              </label>
              <span class="auto-toggle-text">
                <span class="line1">Descarga</span>
                <span class="line2">auto</span>
              </span>
            </div>
            <div class="chat-name-mode-wrapper">
              <div
                class="chat-name-mode-btn-wrapper"
                :class="{
                  active: chat.name_mode && chat.name_mode !== 'manual'
                }"
              >
                <button
                  type="button"
                  class="chat-name-mode-btn"
                  :class="{
                    active: chat.name_mode && chat.name_mode !== 'manual',
                    disabled: saving
                  }"
                  :disabled="saving"
                  @click.stop="toggleChatNameModeMenu(chat)"
                >
                  <span class="chat-switch-pill">
                    <span class="chat-switch-thumb"></span>
                  </span>
                </button>
                <span class="chat-name-mode-text">
                  <span class="line1">Nombre:</span>
                  <span class="line2">{{ nameModeLabel(chat.name_mode) }}</span>
                </span>
              </div>
              <div
                v-if="chatNameModeMenus[chatKey(chat)]"
                class="chat-name-mode-dropdown"
                @click.stop
              >
                <div
                  class="chat-name-mode-item"
                  :class="{ active: chat.name_mode === 'manual' }"
                  @click="selectChatNameMode(chat, 'manual')"
                >
                  <span>{{ t('listener.nameModeManual') }}</span>
                </div>
                <div
                  class="chat-name-mode-item"
                  :class="{ active: chat.name_mode === 'original' }"
                  @click="selectChatNameMode(chat, 'original')"
                >
                  <span>{{ t('listener.nameModeOriginal') }}</span>
                </div>
                <div
                  class="chat-name-mode-item"
                  :class="{ active: chat.name_mode === 'caption' }"
                  @click="selectChatNameMode(chat, 'caption')"
                >
                  <span>{{ t('listener.nameModeCaption') }}</span>
                </div>
              </div>
            </div>
            <button
              :disabled="saving"
              @click="removeChat(chat)"
              :aria-label="t('listener.removeChatAria')"
            >
              <Trash2 :size="14" />
            </button>
          </div>
          <div class="chat-filters">
            <label
              class="filter-tag f-photos"
              :class="{ active: chat.f_photos }"
              ><input
                type="checkbox"
                v-model="chat.f_photos"
                :disabled="saving"
                @change="save"
              /><span>{{ t('listener.filterPhotos') }}</span></label
            >
            <label
              class="filter-tag f-videos"
              :class="{ active: chat.f_videos }"
              ><input
                type="checkbox"
                v-model="chat.f_videos"
                :disabled="saving"
                @change="save"
              /><span>{{ t('listener.filterVideos') }}</span></label
            >
            <label
              class="filter-tag f-audios"
              :class="{ active: chat.f_audios }"
              ><input
                type="checkbox"
                v-model="chat.f_audios"
                :disabled="saving"
                @change="save"
              /><span>{{ t('listener.filterAudios') }}</span></label
            >
            <label class="filter-tag f-docs" :class="{ active: chat.f_docs }"
              ><input
                type="checkbox"
                v-model="chat.f_docs"
                :disabled="saving"
                @change="save"
              /><span>{{ t('listener.filterDocs') }}</span></label
            >
            <label
              class="filter-tag f-stickers"
              :class="{ active: chat.f_stickers }"
              ><input
                type="checkbox"
                v-model="chat.f_stickers"
                :disabled="saving"
                @change="save"
              /><span>{{ t('listener.filterStickers') }}</span></label
            >
          </div>
        </div>
        <small class="save-hint">{{ t('listener.saveHint') }}</small>
      </section>
      <section class="panel listener-feed">
        <div class="panel-heading">
          <div>
            <span class="eyebrow"
              ><Inbox :size="12" /> {{ t('listener.inboxKicker') }}</span
            >
            <h2>{{ t('listener.feedTitle') }}</h2>
          </div>
          <div class="header-actions">
            <div v-if="disk" class="disk-monitor">
              <div class="disk-bar">
                <div
                  class="fill"
                  :style="{
                    width: disk.percent + '%',
                    backgroundColor:
                      disk.status === 'red' ? '#ff4d4d' : '#4dff4d'
                  }"
                ></div>
              </div>
              <small>{{
                t('downloads.diskLabel', {
                  total: disk.total_str,
                  free: disk.projected_free_str
                })
              }}</small>
            </div>
            <AppPill>
              {{ t('listener.newPill', { n: availableCount }) }}
            </AppPill>
            <div v-if="items.length" class="bulk-actions">
              <div class="bulk-name-wrapper">
                <button
                  class="bulk-name-button"
                  :title="t('listener.bulkNameTitle')"
                  @click.stop="toggleBulkNameMenu"
                >
                  <Settings2 :size="14" /> {{ t('listener.nameLabel') }}
                </button>
                <div
                  v-if="bulkNameMenuOpen"
                  class="name-dropdown bulk-name-dropdown"
                >
                  <div
                    class="name-option"
                    @click="selectBulkFileName('caption')"
                  >
                    <span class="name-preview">{{
                      t('listener.useCaption')
                    }}</span>
                    <small>{{ t('listener.onlyManual') }}</small>
                  </div>
                  <div
                    class="name-option"
                    @click="selectBulkFileName('original')"
                  >
                    <span class="name-preview">{{
                      t('listener.useOriginal')
                    }}</span>
                    <small>{{ t('listener.onlyManual') }}</small>
                  </div>
                </div>
              </div>
              <button
                class="bulk-download"
                :title="t('listener.downloadAllTitleTip')"
                @click="downloadAll"
              >
                <Download :size="14" /> {{ t('listener.downloadAll') }}
              </button>
              <button
                class="bulk-delete"
                :title="t('listener.clearListTip')"
                @click="clearAll"
              >
                <Trash2 :size="14" /> {{ t('listener.clearList') }}
              </button>
            </div>
          </div>
        </div>
        <div v-if="!items.length" class="empty-state">
          <Inbox :size="28" />
          <p>{{ t('listener.emptyTitle') }}</p>
          <small>{{ t('listener.emptySub') }}</small>
        </div>
        <div
          v-for="(item, index) in items"
          :key="item.id"
          class="listener-item"
          :class="{
            'is-dragging': draggedIndex === index,
            'is-drag-over': dragOverIndex === index
          }"
          @dragover="onDragOver(index, $event)"
          @dragleave="onDragLeave(index)"
          @drop="onDrop(index)"
        >
          <div
            class="drag-handle"
            draggable="true"
            :title="t('listener.reorderTip')"
            @dragstart="onDragStart(index, $event)"
            @dragend="onDragEnd"
          >
            <Menu :size="14" />
          </div>
          <div
            class="file-symbol"
            :class="mediaMeta(getMediaKind(item)).class"
            :title="mediaMeta(getMediaKind(item)).label"
          >
            <component :is="mediaMeta(getMediaKind(item)).icon" :size="16" />
          </div>
          <div class="file-info">
            <strong :title="item.file_name">{{ item.file_name }}</strong>
            <span
              >{{ getChatName(item) }} ·
              {{ t('listener.itemMessage', { id: item.message_id }) }} ·
              {{ item.total_str }}</span
            >
          </div>
          <div class="row-side">
            <span class="listener-status">{{ statusText(item.status) }}</span>
            <div class="row-actions">
              <div
                v-if="
                  hasManualNameSelection(item) && item.status === 'available'
                "
                class="name-select-wrapper"
              >
                <button
                  class="name-select-button"
                  @click="toggleNameMenu(item.id)"
                >
                  <Settings2 :size="13" /> {{ t('listener.nameLabel') }}
                </button>
                <div v-if="nameSelectionMenus[item.id]" class="name-dropdown">
                  <div
                    v-if="
                      item.caption_file_name &&
                      item.caption_file_name !== item.file_name
                    "
                    class="name-option"
                    @click="selectFileName(item, item.caption_file_name)"
                  >
                    <span class="name-preview">{{
                      item.caption_file_name
                    }}</span>
                    <small>({{ t('listener.captionTag') }})</small>
                  </div>
                  <div
                    v-if="
                      item.original_file_name &&
                      item.original_file_name !== item.file_name
                    "
                    class="name-option"
                    @click="selectFileName(item, item.original_file_name)"
                  >
                    <span class="name-preview">{{
                      item.original_file_name
                    }}</span>
                    <small>({{ t('listener.originalTag') }})</small>
                  </div>
                </div>
              </div>
              <button
                v-if="item.status === 'available'"
                class="download-small"
                @click="download(item)"
              >
                <Download :size="13" /> {{ t('listener.download') }}
              </button>
              <button
                class="delete-small"
                @click="removeItem(item)"
                :aria-label="t('listener.removeItemAria')"
              >
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
.listener-view {
  display: flex;
  flex-direction: column;
  gap: 18px;
}
.listener-hero {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 20px;
  padding: 28px 30px;
  border: 1px solid var(--user-border-light);
  border-radius: 18px;
  background: linear-gradient(
    110deg,
    var(--user-surface-light),
    var(--user-surface) 70%
  );
}
.listener-hero h2 {
  font: 600 23px 'Space Grotesk';
  margin: 8px 0 5px;
  color: #f1f7ff;
}
.listener-hero p,
.helper-text {
  margin: 0;
  color: var(--user-text-dim);
  font-size: 13px;
}
.switch.large {
  display: flex;
  align-items: center;
  gap: 10px;
  white-space: nowrap;
}
.switch.large b {
  font-size: 12px;
  color: var(--user-accent);
  font-weight: 500;
}
.listener-grid {
  display: grid;
  grid-template-columns: 0.82fr 1.18fr;
  gap: 18px;
  min-width: 0;
}
.listener-grid > section {
  min-width: 0;
}
.helper-text {
  line-height: 1.5;
  margin-bottom: 17px;
}
.listener-add {
  display: flex;
  gap: 8px;
}
.listener-add input {
  flex: 1;
  min-width: 0;
  background: var(--user-bg-base);
  border: 1px solid var(--user-border-light);
  color: #dbe7f5;
  border-radius: 10px;
  padding: 11px 12px;
  outline: none;
  font: inherit;
}
.listener-add .save-button {
  width: auto;
  margin: 0;
  padding: 0 15px;
}
.chat-chip {
  display: flex;
  flex-direction: column;
  gap: 8px;
  border-top: 1px solid var(--user-border);
  padding: 14px 0;
}
.chat-chip-main {
  display: flex;
  align-items: center;
  gap: 9px;
  color: var(--user-primary);
  font-size: 12px;
}
.chat-filters {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  padding-left: 23px;
  margin-top: 4px;
}
.filter-tag {
  display: flex;
  align-items: center;
  padding: 4px 12px;
  border-radius: 20px;
  font-size: 10px;
  font-weight: 700;
  color: var(--user-text-dim);
  cursor: pointer;
  background: var(--user-bg-base);
  border: 1px solid var(--user-border-light);
  transition: all 0.25s cubic-bezier(0.4, 0, 0.2, 1);
  user-select: none;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
.filter-tag input {
  display: none;
}
.filter-tag:hover {
  border-color: var(--user-primary);
  transform: translateY(-1px);
}
.filter-tag.active {
  color: #000;
  border-color: transparent;
  box-shadow: 0 4px 10px rgba(0, 0, 0, 0.3);
  transform: translateY(-1px);
}
.filter-tag.f-photos.active {
  background: #38bdf8;
  box-shadow: 0 4px 12px rgba(56, 189, 248, 0.3);
}
.filter-tag.f-videos.active {
  background: #c084fc;
  box-shadow: 0 4px 12px rgba(192, 132, 252, 0.3);
}
.filter-tag.f-audios.active {
  background: #4ade80;
  box-shadow: 0 4px 12px rgba(74, 222, 128, 0.3);
}
.filter-tag.f-docs.active {
  background: #94a3b8;
  box-shadow: 0 4px 12px rgba(148, 163, 184, 0.3);
}
.filter-tag.f-stickers.active {
  background: #f472b6;
  box-shadow: 0 4px 12px rgba(244, 114, 182, 0.3);
}
.chat-chip strong {
  flex: 1;
  color: #d6e4f1;
  font-weight: 500;
}
.chat-chip button {
  border: 0;
  background: transparent;
  color: #e58b91;
  font-size: 20px;
  cursor: pointer;
}
.save-hint {
  display: block;
  color: var(--user-text-dim);
  font-size: 10px;
  margin-top: 13px;
}
.panel-heading {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
}
.header-actions {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 10px;
}
.bulk-actions {
  display: flex;
  gap: 6px;
  position: relative;
}
.bulk-name-wrapper {
  position: relative;
  display: inline-flex;
}
.bulk-name-button {
  border: 1px solid var(--user-primary);
  background: var(--user-icon-bg);
  color: var(--user-primary);
  border-radius: 6px;
  padding: 4px 8px;
  font-size: 11px;
  cursor: pointer;
  display: flex;
  align-items: center;
  gap: 4px;
  transition: all 0.2s;
  font-weight: 600;
}
.bulk-name-button:hover {
  background: var(--user-surface-light);
  border-color: var(--user-accent);
  color: var(--user-accent);
}
.bulk-name-dropdown {
  right: 0;
  top: calc(100% + 8px);
  min-width: 220px;
  max-width: 320px;
}
.bulk-download,
.bulk-delete {
  border: 1px solid var(--user-border-light);
  background: var(--user-bg-base);
  color: #dbe7f5;
  border-radius: 6px;
  padding: 4px 8px;
  font-size: 11px;
  cursor: pointer;
  display: flex;
  align-items: center;
  gap: 4px;
  transition: all 0.2s;
}
.bulk-download:hover {
  background: var(--user-icon-bg);
  border-color: var(--user-primary);
  color: var(--user-accent);
}
.bulk-delete:hover {
  background: #251415;
  border-color: #4a2b2d;
  color: #e58b91;
}
.listener-item {
  display: flex;
  align-items: center;
  gap: 12px;
  border-top: 1px solid var(--user-border);
  padding: 13px 0;
  position: relative;
  transition:
    opacity 0.2s ease,
    border-color 0.2s ease,
    background-color 0.2s ease;
}
.listener-item.menu-open {
  z-index: 50;
}
.listener-item.is-dragging {
  opacity: 0.35;
}
.listener-item.is-drag-over {
  border-top: 2px solid var(--user-primary);
  background: rgba(56, 189, 248, 0.05);
}
.drag-handle {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  color: var(--user-text-dim);
  cursor: grab;
  opacity: 0;
  transition:
    opacity 0.2s ease,
    color 0.2s ease,
    background-color 0.2s ease;
  user-select: none;
  flex-shrink: 0;
  border-radius: 4px;
}
.listener-item:hover .drag-handle {
  opacity: 1;
}
.drag-handle:hover {
  color: var(--user-accent);
  background: var(--user-surface-light);
}
.drag-handle:active {
  cursor: grabbing;
}
.file-info {
  flex: 1;
  min-width: 0;
  overflow: hidden;
}
.file-info strong,
.file-info span {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.row-side {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 5px;
  margin-left: auto;
  flex-shrink: 0;
}
.row-actions {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
  position: relative;
}
.listener-status {
  font-size: 10px;
  color: var(--user-text-dim);
}
.download-small {
  border: 1px solid var(--user-primary);
  background: var(--user-icon-bg);
  color: var(--user-primary);
  border-radius: 7px;
  padding: 6px 9px;
  font-size: 10px;
  cursor: pointer;
  display: flex;
  align-items: center;
  gap: 4px;
}
.download-small:hover {
  background: var(--user-surface-light);
}
.delete-small {
  border: 1px solid #4a2b2d;
  background: #251415;
  color: #e58b91;
  border-radius: 7px;
  padding: 6px 9px;
  font-size: 10px;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
}
.delete-small:hover {
  background: #3a1d1f;
}
@media (max-width: 900px) {
  .listener-grid {
    grid-template-columns: 1fr;
  }
}
@media (max-width: 580px) {
  .listener-hero {
    align-items: flex-start;
    flex-direction: column;
    padding: 22px;
  }
  .listener-add {
    flex-direction: column;
  }
  .listener-add .save-button {
    height: 38px;
  }
  .listener-item .row-side {
    min-width: 75px;
  }
}
.listener-error {
  margin-top: 10px;
  color: #e58b91;
  font-size: 11px;
}
.topic-picker {
  margin-top: 12px;
  padding: 13px;
  border: 1px solid var(--user-border-light);
  border-radius: 12px;
  background: var(--user-bg-base);
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.topic-picker-head {
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.topic-picker-head strong {
  color: #d6e4f1;
  font-size: 13px;
  font-weight: 500;
}
.topic-picker-head small {
  color: var(--user-text-dim);
  font-size: 11px;
}
.topic-select {
  width: 100%;
  background: var(--user-surface);
  border: 1px solid var(--user-border-light);
  color: #dbe7f5;
  border-radius: 9px;
  padding: 9px 10px;
  outline: none;
  font: inherit;
  font-size: 12px;
}
.topic-picker-actions {
  display: flex;
  gap: 8px;
}
.topic-picker-actions .save-button {
  width: auto;
  margin: 0;
  padding: 0 15px;
}
.ghost-button {
  border: 1px solid var(--user-border-light);
  background: transparent;
  color: var(--user-text-dim);
  border-radius: 9px;
  padding: 0 15px;
  font: inherit;
  font-size: 12px;
  cursor: pointer;
}
.ghost-button:hover {
  color: #dbe7f5;
  border-color: var(--user-primary);
}
.chat-details {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.chat-details strong {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.chat-details small {
  color: var(--user-text-dim);
  font-size: 10px;
}
.chat-carpeta {
  display: flex;
  align-items: center;
  gap: 4px;
  color: var(--user-accent);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.chat-carpeta svg {
  flex: none;
}
.auto-toggle-wrapper {
  display: flex;
  align-items: center;
  gap: 8px;
}
.auto-toggle {
  display: flex;
  align-items: center;
  color: var(--user-text-dim);
  cursor: pointer;
  white-space: nowrap;
}
.auto-toggle span {
  flex: none;
}
.auto-toggle.disabled {
  cursor: default;
  opacity: 0.6;
}
.auto-toggle-text {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 0;
  line-height: 1.1;
}
.auto-toggle-text .line1 {
  font-size: 9px;
  font-weight: 700;
  letter-spacing: 0.3px;
  text-transform: uppercase;
  color: var(--user-text-dim);
  transition: color 0.2s;
}
.auto-toggle-text .line2 {
  font-size: 9px;
  font-weight: 700;
  letter-spacing: 0.3px;
  text-transform: uppercase;
  color: var(--user-text-dim);
  transition: color 0.2s;
}
.auto-toggle-wrapper.active .auto-toggle-text .line1,
.auto-toggle-wrapper.active .auto-toggle-text .line2 {
  color: var(--user-accent);
}
.chat-name-mode-wrapper {
  position: relative;
  display: inline-flex;
  align-items: center;
}
.chat-name-mode-btn-wrapper {
  display: flex;
  align-items: center;
  gap: 8px;
}
.chat-name-mode-btn {
  display: flex;
  align-items: center;
  background: transparent;
  border: 0;
  padding: 0;
  cursor: pointer;
  outline: none;
  font: inherit;
  user-select: none;
}
.chat-name-mode-btn.disabled {
  opacity: 0.6;
  cursor: default;
}
.chat-switch-pill {
  display: block;
  width: 35px;
  height: 20px;
  background: var(--user-border-light);
  border-radius: 999px;
  position: relative;
  transition: background 0.2s ease;
  flex-shrink: 0;
}
.chat-switch-thumb {
  position: absolute;
  width: 14px;
  height: 14px;
  top: 3px;
  left: 3px;
  background: #9bb0c7;
  border-radius: 50%;
  transition:
    left 0.2s ease,
    background 0.2s ease;
}
.chat-name-mode-btn.active .chat-switch-pill {
  background: var(--user-primary);
}
.chat-name-mode-btn.active .chat-switch-thumb {
  left: 18px;
  background: #fff;
}
.chat-name-mode-text {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 0;
  line-height: 1.1;
}
.chat-name-mode-text .line1 {
  font-size: 9px;
  font-weight: 700;
  letter-spacing: 0.3px;
  text-transform: uppercase;
  color: var(--user-text-dim);
  transition: color 0.2s ease;
}
.chat-name-mode-text .line2 {
  font-size: 9px;
  font-weight: 700;
  letter-spacing: 0.3px;
  text-transform: uppercase;
  color: var(--user-text-dim);
  transition: color 0.2s ease;
}
.chat-name-mode-btn-wrapper.active .chat-name-mode-text .line1,
.chat-name-mode-btn-wrapper.active .chat-name-mode-text .line2 {
  color: var(--user-accent);
}
.chat-name-mode-dropdown {
  position: absolute;
  top: calc(100% + 6px);
  right: 0;
  background: var(--user-surface);
  border: 1px solid var(--user-primary);
  border-radius: 10px;
  padding: 6px;
  min-width: 125px;
  z-index: 1000;
  box-shadow: 0 8px 20px rgba(0, 0, 0, 0.4);
  display: flex;
  flex-direction: column;
  gap: 4px;
  animation: fadeIn 0.15s ease-out;
}
.chat-name-mode-item {
  padding: 6px 10px;
  border-radius: 6px;
  font-size: 11px;
  font-weight: 600;
  color: var(--user-text-dim);
  cursor: pointer;
  transition: all 0.15s ease;
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.chat-name-mode-item:hover {
  background: var(--user-icon-bg);
  color: #d6e4f1;
}
.chat-name-mode-item.active {
  background: var(--user-icon-bg);
  color: var(--user-accent);
}
.file-symbol {
  width: 36px;
  height: 36px;
  border-radius: 9px;
  display: flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--user-border);
  flex-shrink: 0;
  transition: all 0.2s ease;
}
.media-badge {
  display: inline-flex;
  align-items: center;
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 10px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.3px;
  margin-right: 6px;
  border: 1px solid transparent;
  vertical-align: middle;
}
.media-photo {
  color: #38bdf8;
  background: rgba(56, 189, 248, 0.12);
  border-color: rgba(56, 189, 248, 0.3);
}
.media-video {
  color: #c084fc;
  background: rgba(192, 132, 252, 0.12);
  border-color: rgba(192, 132, 252, 0.3);
}
.media-song {
  color: #4ade80;
  background: rgba(74, 222, 128, 0.12);
  border-color: rgba(74, 222, 128, 0.3);
}
.media-file {
  color: #94a3b8;
  background: rgba(148, 163, 184, 0.12);
  border-color: rgba(148, 163, 184, 0.3);
}
.name-select-wrapper {
  position: relative;
  display: inline-flex;
}
.name-select-button {
  border: 1px solid var(--user-primary);
  background: var(--user-icon-bg);
  color: var(--user-primary);
  border-radius: 7px;
  padding: 6px 10px;
  font-size: 10px;
  cursor: pointer;
  display: flex;
  align-items: center;
  gap: 4px;
  transition: all 0.2s;
  font-weight: 600;
}
.name-select-button:hover {
  background: var(--user-surface-light);
  border-color: var(--user-accent);
  color: var(--user-accent);
  transform: translateY(-1px);
}
.name-dropdown {
  position: absolute;
  right: 0;
  top: calc(100% + 8px);
  background: var(--user-surface);
  border: 2px solid var(--user-primary);
  border-radius: 10px;
  padding: 8px;
  min-width: 220px;
  max-width: 360px;
  z-index: 1000;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
  animation: fadeIn 0.2s ease-out;
}
.name-dropdown::before {
  content: '';
  position: absolute;
  top: -6px;
  right: 20px;
  width: 12px;
  height: 12px;
  background: var(--user-surface);
  border-left: 2px solid var(--user-primary);
  border-top: 2px solid var(--user-primary);
  transform: rotate(45deg);
}
.name-option {
  padding: 10px 12px;
  border-radius: 8px;
  cursor: pointer;
  display: flex;
  flex-direction: column;
  gap: 4px;
  transition: all 0.2s;
  border: 1px solid transparent;
}
.name-option:hover {
  background: var(--user-icon-bg);
  border-color: var(--user-primary);
  transform: translateX(-2px);
}
.name-option .name-preview {
  font-size: 12px;
  color: #d6e4f1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-weight: 500;
}
.name-option small {
  font-size: 10px;
  color: var(--user-accent);
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
@keyframes fadeIn {
  from {
    opacity: 0;
    transform: translateY(-4px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}
</style>
