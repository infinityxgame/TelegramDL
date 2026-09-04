<script setup>
import { Settings2, Zap, Trash2, Save } from 'lucide-vue-next'
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
  }
})

const emit = defineEmits([
  'save-settings',
  'clear-history',
  'reset-color'
])
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
              max="16"
              class="range-input"
            />
            <div class="range-hints"><span>1</span><span>16</span></div>

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
