<script setup>
import { ref, computed } from 'vue'
import { Settings2, Zap, Trash2, Save, ShieldCheck, Copy, Eye, EyeOff, RefreshCw } from 'lucide-vue-next'
import FolderPicker from '../components/FolderPicker.vue'

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
            <span class="setting-label"><ShieldCheck :size="14" style="vertical-align: -2px; margin-right: 4px;" />Acceso remoto</span>
            <small>
              Con este token puedes controlar TelegramDL desde otro dispositivo (celular, otra PC).
              No abras este puerto directamente a internet: combínalo con una VPN como
              <a href="https://tailscale.com" target="_blank" rel="noopener">Tailscale</a>
              o un túnel como Cloudflare Tunnel, y pega el token en la pantalla de login remoto.
            </small>

            <div class="speed-row" style="margin-top: 12px;">
              <input
                :value="showToken ? apiToken : maskedToken"
                type="text"
                readonly
                style="font-family: 'JetBrains Mono', monospace; letter-spacing: 0.02em;"
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
