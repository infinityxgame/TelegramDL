<script setup>
import { ref, computed } from 'vue'
import { Settings2, Zap, Trash2, Save, Copy, Eye, EyeOff, RefreshCw, Download, Upload } from 'lucide-vue-next'
import FolderPicker from '../components/FolderPicker.vue'
import { useAuthToken } from '../composables/useAuthToken'

const { authHeaders } = useAuthToken()

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
  'clear-history',
  'reset-color',
  'reset-loader-color',
  'regenerate-token'
])

const showToken = ref(false)
const copyLabel = ref('Copiar')
const maskedToken = computed(() => props.apiToken ? '•'.repeat(Math.min(props.apiToken.length, 40)) : '')

const copyToken = async () => {
  if (!props.apiToken) return
  try {
    await navigator.clipboard.writeText(props.apiToken)
    copyLabel.value = '¡Copiado!'
  } catch (e) {
    copyLabel.value = 'No se pudo copiar'
  }
  setTimeout(() => { copyLabel.value = 'Copiar' }, 2000)
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
  if (!response.ok) throw new Error(data.detail || data.error || 'Error en el servidor')
  return data
}

const normalizeChat = (raw) => {
  if (!raw || typeof raw !== 'object') return null
  const id = Number(raw.id ?? raw.chat_id)
  if (!Number.isInteger(id) || id === 0) return null
  const flag = (value) => value === undefined || value === null ? true : !!value
  return {
    id,
    name: String(raw.name ?? id),
    auto_download: !!raw.auto_download,
    f_photos: flag(raw.f_photos),
    f_videos: flag(raw.f_videos),
    f_audios: flag(raw.f_audios),
    f_docs: flag(raw.f_docs),
    f_stickers: flag(raw.f_stickers)
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
      props.notify('No hay chats configurados en Escucha para exportar', true)
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
    triggerBlobDownload(JSON.stringify(payload, null, 2), `tgdown-escucha-${stamp}.json`, 'application/json')
    props.notify(`Escucha exportada (${chats.length} chat${chats.length === 1 ? '' : 's'})`)
  } catch (err) {
    props.notify(err.message || 'No se pudo exportar la escucha', true)
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
    } catch (e) {
      throw new Error('El archivo no es un JSON válido')
    }

    const rawChats = Array.isArray(parsed) ? parsed : (parsed?.chats || parsed?.listener_chats)
    if (!Array.isArray(rawChats)) {
      throw new Error('El archivo no contiene una lista de chats de escucha')
    }

    const incoming = rawChats.map(normalizeChat).filter(Boolean)
    if (!incoming.length) {
      throw new Error('El archivo no tiene ningún chat válido')
    }

    const current = await listenerApi()
    const merged = new Map()
    for (const chat of (current.chats || []).map(normalizeChat).filter(Boolean)) {
      merged.set(chat.id, chat)
    }
    let added = 0
    let updated = 0
    for (const chat of incoming) {
      if (merged.has(chat.id)) updated++
      else added++
      merged.set(chat.id, chat)
    }

    const body = { enabled: !!current.enabled, chats: Array.from(merged.values()) }
    if (typeof parsed?.listener_enabled === 'boolean') {
      body.enabled = parsed.listener_enabled
    }

    await listenerApi({
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body)
    })

    props.notify(`Escucha importada: ${added} nuevo${added === 1 ? '' : 's'}, ${updated} actualizado${updated === 1 ? '' : 's'}`)
  } catch (err) {
    props.notify(err.message || 'No se pudo importar la escucha', true)
  } finally {
    importing.value = false
  }
}
</script>

<template>
  <div class="settings-view">
    <div class="content-grid-single">
      <aside class="panel settings-panel-full">
        <div class="panel-heading">
          <div>
            <span class="eyebrow"><Settings2 :size="12" /> PREFERENCIAS</span>
            <h2>Configuración General</h2>
          </div>
          <span class="save-state">{{ saving ? 'Guardando…' : 'Auto-guardado' }}</span>
        </div>

        <div class="settings-sections-grid">
          <!-- Concurrencia y Workers -->
          <div class="settings-group">
            <label class="setting-label">
              Descargas simultáneas <output>{{ settings.max_concurrent_downloads }}</output>
            </label>
            <input
              v-model.number="settings.max_concurrent_downloads"
              type="range"
              min="1"
              max="32"
              class="range-input"
            />
            <div class="range-hints"><span>1</span><span>32</span></div>

            <div class="setting-line">
              <div>
                <strong>Partes simultáneas</strong>
                <small>Acelera cada archivo usando varios bloques.</small>
              </div>
              <label class="switch">
                <input v-model="settings.parallel_chunks" type="checkbox" />
                <span></span>
              </label>
            </div>

            <label class="setting-label compact">
              Workers por archivo <output>{{ settings.chunk_workers }}</output>
            </label>
            <input
              v-model.number="settings.chunk_workers"
              :disabled="!settings.parallel_chunks"
              type="range"
              min="1"
              max="8"
              class="range-input"
            />
            <div class="range-hints"><span>1</span><span>8</span></div>
          </div>

          <!-- Velocidad y Directorio -->
          <div class="settings-group">
            <div class="speed-setting">
              <label class="setting-label compact">Límite global de velocidad</label>
              <div class="speed-row">
                <input
                  v-model.number="settings.speed_limit.value"
                  type="number"
                  min="0"
                  step="0.5"
                />
                <select v-model="settings.speed_limit.unit">
                  <option>KB</option>
                  <option>MB</option>
                  <option>GB</option>
                </select>
                <span>/s</span>
              </div>
              <small>Usa 0 para quitar el límite.</small>
            </div>

            <FolderPicker v-model="settings.download_folder" />
          </div>

          <!-- Paleta de Color y Tema -->
          <div class="settings-group color-group">
            <span class="setting-label">Color de Acento y Tema</span>
            <div class="color-selector-container">
              <div class="color-row">
                <button
                  v-for="id in [0, 1, 2, 3, 4, 5, 6, 7]"
                  :key="id"
                  type="button"
                  class="color-dot"
                  :class="{ active: settings.color_id === id }"
                  :style="{ background: themeMap[id]?.gradient || '#38a7ff' }"
                  :title="'Color ' + id"
                  @click="settings.color_id = id"
                ></button>
              </div>
              <div class="color-row">
                <button
                  v-for="id in [8, 9, 10, 11, 12, 13, 14, 15]"
                  :key="id"
                  type="button"
                  class="color-dot gradient-dot"
                  :class="{ active: settings.color_id === id }"
                  :style="{
                    background: `linear-gradient(135deg, ${themeMap[id]?.primary || '#38a7ff'} 49.8%, ${themeMap[id]?.secondary || '#b48bf2'} 50.2%)`
                  }"
                  :title="'Degradado ' + id"
                  @click="settings.color_id = id"
                ></button>
              </div>
            </div>

            <button type="button" class="reset-button-alt" @click="emit('reset-color')">
              <Zap :size="14" /> Restablecer color de la cuenta
            </button>

            <!-- Color del Loader -->
            <span class="setting-label compact" style="margin-top: 30px; border-top: 1px solid var(--user-border); padding-top: 20px;">Color de Loader</span>
            <div class="color-selector-container">
              <div class="color-row">
                <button
                  v-for="id in [0, 1, 2, 3, 4, 5, 6, 7]"
                  :key="'loader-' + id"
                  type="button"
                  class="color-dot"
                  :class="{ active: settings.loader_color_id === id }"
                  :style="{ background: themeMap[id]?.gradient || '#38a7ff' }"
                  :title="'Color Loader ' + id"
                  @click="settings.loader_color_id = id"
                ></button>
              </div>
              <div class="color-row">
                <button
                  v-for="id in [8, 9, 10, 11, 12, 13, 14, 15]"
                  :key="'loader-grad-' + id"
                  type="button"
                  class="color-dot gradient-dot"
                  :class="{ active: settings.loader_color_id === id }"
                  :style="{
                    background: `linear-gradient(135deg, ${themeMap[id]?.primary || '#38a7ff'} 49.8%, ${themeMap[id]?.secondary || '#b48bf2'} 50.2%)`
                  }"
                  :title="'Degradado Loader ' + id"
                  @click="settings.loader_color_id = id"
                ></button>
              </div>
            </div>

            <button type="button" class="reset-button-alt" @click="emit('reset-loader-color')">
              <Zap :size="14" /> Restablecer color del loader
            </button>
          </div>

          <!-- Acceso remoto -->
          <div class="settings-group">
            <span class="setting-label">Acceso remoto</span>
            <small>
              Con este token puedes controlar TelegramDL desde otro dispositivo (celular, otra PC).
              No abras este puerto directamente a internet: combínalo con una VPN como
              <a class="inline-link" href="https://tailscale.com" target="_blank" rel="noopener">Tailscale</a>
              o un túnel como Cloudflare Tunnel, y pega el token en la pantalla de login remoto.
            </small>

            <div class="token-row">
              <input
                :value="showToken ? apiToken : maskedToken"
                type="text"
                readonly
              />
            </div>

            <div class="settings-actions" style="margin-top: 10px; padding: 0;">
              <button type="button" class="reset-button-alt" @click="showToken = !showToken">
                <component :is="showToken ? EyeOff : Eye" :size="14" /> {{ showToken ? 'Ocultar' : 'Mostrar' }}
              </button>
              <button type="button" class="reset-button-alt" @click="copyToken">
                <Copy :size="14" /> {{ copyLabel }}
              </button>
              <button type="button" class="reset-button-alt" @click="emit('regenerate-token')">
                <RefreshCw :size="14" /> Regenerar token
              </button>
            </div>
          </div>
        </div>

        <!-- Acciones de Configuración -->
        <div class="settings-actions" style="margin-bottom: 10px;">
          <button type="button" class="reset-button-alt" :disabled="exporting" @click="exportListener">
            <Download :size="14" /> {{ exporting ? 'Exportando…' : 'Exportar escucha' }}
          </button>
          <button type="button" class="reset-button-alt" :disabled="importing" @click="pickImportFile">
            <Upload :size="14" /> {{ importing ? 'Importando…' : 'Importar escucha' }}
          </button>
          <input
            ref="importInput"
            type="file"
            accept="application/json,.json"
            style="display: none"
            @change="onImportFile"
          />
        </div>

        <div class="settings-actions">
          <button
            class="clear-history-button"
            type="button"
            @click="emit('clear-history')"
          >
            <Trash2 :size="15" /> Limpiar historial
          </button>
          <button
            class="save-button"
            type="button"
            :disabled="saving"
            @click="emit('save-settings')"
          >
            <Save :size="15" /> {{ saving ? 'Guardando…' : 'Guardar ahora' }}
          </button>
        </div>
      </aside>
    </div>
  </div>
</template>
