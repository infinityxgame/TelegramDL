<script setup>
import { ref, computed } from 'vue'
import {
  Settings2,
  Zap,
  Trash2,
  Save,
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
  Send
} from '../icons'
import FolderPicker from '../components/FolderPicker.vue'
import { useAuthToken } from '../composables/useAuthToken'
import { useExternalLink } from '../composables/useExternalLink'
import { useI18n } from '../i18n'
import AppAccordion from '../components/AppAccordion.vue'

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
const temasEspeciales = computed(() =>
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
      `tgdown-escucha-${stamp}.json`,
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
      body: JSON.stringify({ token: props.settings.notification_bot_token })
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

        <div class="ajustes-acordeon">
          <AppAccordion
            :title="t('settings.downloadsTitle')"
            :description="t('settings.downloadsSub')"
          >
            <template #icon>
              <ArrowDownToLine :size="15" />
            </template>

            <div class="ajuste-columnas">
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
              </div>
            </div>
          </AppAccordion>

          <!-- 2. Temas -->
          <AppAccordion
            :title="t('settings.themesTitle')"
            :description="t('settings.themesSub')"
          >
            <template #icon>
              <Palette :size="15" />
            </template>

            <div class="ajuste-columnas">
              <!-- Paleta de Color y Tema -->
              <div class="settings-group color-group">
                <span class="setting-label">{{
                  t('settings.accentTitle')
                }}</span>

                <!-- Los 0-15 vienen de Telegram Premium; del 16 en adelante son las
                       paletas propias del panel. Van separados con su propio título
                       para que se vea de dónde sale cada grupo. -->
                <span class="grupo-color-titulo">{{
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
                <span class="grupo-color-titulo">{{
                  t('settings.thematicGroup')
                }}</span>
                <div class="temas-lista">
                  <button
                    v-for="id in temasEspeciales"
                    :key="'tema-' + id"
                    type="button"
                    class="tema-chip"
                    :class="{ active: props.settings.color_id === id }"
                    :title="
                      themeMap[id]?.name || t('settings.themeN', { n: id })
                    "
                    @click="patch({ color_id: id })"
                  >
                    <span
                      class="tema-muestra"
                      :style="{ background: themeMap[id]?.gradient }"
                    ></span>
                    <span class="tema-nombre">{{
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

                <span class="grupo-color-titulo">{{
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
                <span class="grupo-color-titulo">{{
                  t('settings.thematicGroupShort')
                }}</span>
                <div class="color-selector-container">
                  <div class="color-row">
                    <button
                      v-for="id in temasEspeciales"
                      :key="'loader-tema-' + id"
                      type="button"
                      class="color-dot tema-dot"
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
          >
            <template #icon>
              <Languages :size="15" />
            </template>

            <div class="settings-group">
              <!-- El idioma se aplica al instante y se guarda como un ajuste
                     más (settings.language). El chip activo sigue a `locale`,
                     que es el idioma realmente en uso, no al valor bruto del
                     ajuste (vacío la primera vez, antes de autodetectar). -->
              <div class="temas-lista">
                <button
                  v-for="lang in availableLocales"
                  :key="lang.id"
                  type="button"
                  class="tema-chip idioma-chip"
                  :class="{ active: locale === lang.id }"
                  :aria-pressed="locale === lang.id"
                  @click="patch({ language: lang.id })"
                >
                  <span class="tema-nombre">{{ lang.label }}</span>
                </button>
              </div>
              <small>{{ t('settings.langNote') }}</small>
            </div>
          </AppAccordion>

          <!-- 4. Acceso remoto -->
          <AppAccordion
            :title="t('settings.remoteTitle')"
            :description="t('settings.remoteSub')"
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

                <!-- Toggles en 2 columnas -->
                <div class="notif-toggles-grid">
                  <!-- Notificar al terminar la cola -->
                  <div class="notif-toggle-card">
                    <div>
                      <strong>{{ t('settings.notifyOnComplete') }}</strong>
                      <small>{{ t('settings.notifyOnCompleteSub') }}</small>
                    </div>
                    <label class="switch">
                      <input
                        type="checkbox"
                        :checked="settings.notify_on_complete"
                        @change="
                          patch({ notify_on_complete: $event.target.checked })
                        "
                      />
                      <span></span>
                    </label>
                  </div>

                  <!-- Notificar errores -->
                  <div class="notif-toggle-card">
                    <div>
                      <strong>{{ t('settings.notifyOnError') }}</strong>
                      <small>{{ t('settings.notifyOnErrorSub') }}</small>
                    </div>
                    <label class="switch">
                      <input
                        type="checkbox"
                        :checked="settings.notify_on_error"
                        @change="
                          patch({ notify_on_error: $event.target.checked })
                        "
                      />
                      <span></span>
                    </label>
                  </div>
                </div>
              </template>
            </div>
          </AppAccordion>

          <!-- 6. Datos: escucha e historial -->
          <AppAccordion
            :title="t('settings.dataTitle')"
            :description="t('settings.dataSub')"
          >
            <template #icon>
              <HardDrive :size="15" />
            </template>

            <div class="settings-group">
              <div class="settings-actions acciones-datos">
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
                  class="clear-history-button boton-historial"
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
              </div>
            </div>
          </AppAccordion>
        </div>
      </aside>
    </div>

    <!-- Los ajustes se guardan solos; este botón es para forzar el guardado en
         el momento. Va flotando en la esquina para no ocupar sitio dentro del
         panel ahora que las secciones se pliegan. -->
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
  </div>
</template>
