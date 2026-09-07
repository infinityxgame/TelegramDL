<script setup>
import { computed, ref } from 'vue'
import {
  Activity,
  ArrowUpRight,
  CheckCircle2,
  Clock3,
  ExternalLink,
  FileCheck,
  FileDown,
  Gauge,
  Pause,
  Play,
  RotateCcw,
  Trash2,
  X
} from 'lucide-vue-next'

const props = defineProps({
  downloads: {
    type: Array,
    default: () => []
  },
  disk: {
    type: Object,
    default: null
  },
  settings: {
    type: Object,
    required: true
  },
  loading: {
    type: Boolean,
    default: false
  }
})

const emit = defineEmits([
  'start-download',
  'pause-download',
  'resume-download',
  'cancel-download',
  'retry-download',
  'delete-download',
  'open-file',
  'pause-all',
  'resume-all',
  'cancel-all'
])

const inputUrl = ref('')

const statusPriority = status => ({
  downloading: 2,
  paused: 2,
  queued: 2,
  pending: 2
}[status] || 1)

const compareDownloads = (a, b) => {
  const sA = statusPriority(a.status)
  const sB = statusPriority(b.status)

  if (sA !== sB) return sB - sA

  // Para Activos/En cola (prioridad >= 2), orden de creación ascendente
  if (sA >= 2) {
    // Priorizar MessageID si son del mismo Job (rango)
    if (a.job_id && a.job_id === b.job_id) {
      if (a.message_id !== b.message_id) {
        return (a.message_id || 0) - (b.message_id || 0)
      }
    }
    if ((a.created_at || 0) !== (b.created_at || 0)) {
      return (a.created_at || 0) - (b.created_at || 0)
    }
    // Desempate por MessageID para rangos
    return (a.message_id || 0) - (b.message_id || 0)
  }

  // Para Historial (prioridad 1), lo más reciente primero (updated_at)
  return (b.updated_at || 0) - (a.updated_at || 0)
}

const orderedDownloads = computed(() => [...props.downloads].sort(compareDownloads))

const monitorDownloads = computed(() =>
  orderedDownloads.value.filter(item => ['downloading', 'paused', 'pending', 'queued'].includes(item.status))
)

const handleStart = () => {
  const url = inputUrl.value.trim()
  if (!url) return
  emit('start-download', url)
  inputUrl.value = ''
}

const activeDownloads = computed(() =>
  orderedDownloads.value.filter(item => ['downloading', 'paused'].includes(item.status))
)

const pendingDownloads = computed(() =>
  orderedDownloads.value.filter(item => ['pending', 'queued'].includes(item.status))
)

const recentDownloads = computed(() =>
  orderedDownloads.value
    .filter(item => ['completed', 'skipped', 'failed', 'cancelled'].includes(item.status))
    .slice(0, 15)
)

const completedCount = computed(() =>
  props.downloads.filter(item => item.status === 'completed').length
)

const skippedCount = computed(() =>
  props.downloads.filter(item => item.status === 'skipped').length
)

const statusText = (status) => {
  const map = {
    downloading: 'Descargando',
    paused: 'Pausada',
    queued: 'En cola',
    pending: 'Pendiente',
    completed: 'Completado',
    skipped: 'Omitido',
    failed: 'Fallido',
    cancelled: 'Cancelado'
  }
  return map[status] || status
}

const progress = (item) => Math.max(0, Math.min(100, Number(item.progress || 0)))

const hasActiveOrQueued = computed(() =>
  props.downloads.some(item => ['downloading', 'queued', 'pending'].includes(item.status))
)

const allActivePaused = computed(() => {
  const activeItems = props.downloads.filter(item => ['downloading', 'queued', 'pending', 'paused'].includes(item.status))
  return activeItems.length > 0 && activeItems.every(item => item.status === 'paused')
})
</script>

<template>
  <div class="downloads-view">
    <!-- Hero Card / Formulario de Descarga -->
    <section class="hero-card">
      <div class="hero-copy">
        <span class="hero-kicker">NUEVA TAREA</span>
        <h2>Descarga contenido de Telegram</h2>
        <p>Pega un enlace de mensaje o un rango para comenzar.</p>
      </div>
      <div class="download-form">
        <input
          v-model="inputUrl"
          @keyup.enter="handleStart"
          placeholder="https://t.me/c/..."
          aria-label="Enlace de Telegram"
        />
        <button
          class="primary-button"
          :disabled="loading || !inputUrl.trim()"
          @click="handleStart"
        >
          <span>{{ loading ? 'Añadiendo…' : 'Iniciar descarga' }}</span>
          <ArrowUpRight :size="18" />
        </button>
      </div>
    </section>

    <!-- Métricas y Estadísticas -->
    <section class="stats-grid">
      <div class="stat-card">
        <span class="stat-icon blue"><Activity :size="19" /></span>
        <div>
          <span class="stat-label">ACTIVAS</span>
          <strong>{{ activeDownloads.length }}</strong>
          <small>de {{ settings.max_concurrent_downloads }} permitidas</small>
        </div>
      </div>
      <div class="stat-card">
        <span class="stat-icon amber"><Clock3 :size="19" /></span>
        <div>
          <span class="stat-label">EN COLA</span>
          <strong>{{ pendingDownloads.length }}</strong>
          <small>esperando turno</small>
        </div>
      </div>
      <div class="stat-card">
        <span class="stat-icon green"><CheckCircle2 :size="19" /></span>
        <div>
          <span class="stat-label">COMPLETADAS</span>
          <strong>{{ completedCount }}</strong>
          <small>en esta sesión</small>
        </div>
      </div>
      <div class="stat-card">
        <span class="stat-icon gray"><FileCheck :size="19" /></span>
        <div>
          <span class="stat-label">OMITIDAS</span>
          <strong>{{ skippedCount }}</strong>
          <small>ya existían</small>
        </div>
      </div>
    </section>

    <!-- Monitor de Actividad en Tiempo Real -->
    <div class="content-grid-single">
      <section class="panel activity-panel">
        <div class="panel-heading">
          <div>
            <span class="eyebrow">MONITOR</span>
            <h2>Actividad en tiempo real</h2>
          </div>
          <div class="header-actions">
            <button
              v-if="hasActiveOrQueued || allActivePaused"
              class="action-btn-mini"
              @click="allActivePaused ? emit('resume-all') : emit('pause-all')"
            >
              <component :is="allActivePaused ? Play : Pause" :size="12" />
              {{ allActivePaused ? 'Reanudar todo' : 'Pausar todo' }}
            </button>
            <button
              v-if="activeDownloads.length || pendingDownloads.length"
              class="action-btn-mini danger"
              @click="emit('cancel-all')"
            >
              <X :size="12" />
              <span>Cancelar todo</span>
            </button>
            <div v-if="disk" class="disk-monitor">
              <div class="disk-bar">
                <div
                  class="fill"
                  :style="{
                    width: disk.percent + '%',
                    backgroundColor: disk.status === 'red' ? '#ff4d4d' : '#4dff4d'
                  }"
                ></div>
              </div>
              <small>Total/Libre: ({{ disk.total_str }} / {{ disk.projected_free_str }})</small>
            </div>
            <span class="count-pill">{{ activeDownloads.length + pendingDownloads.length }} tareas</span>
          </div>
        </div>

        <div
          v-if="!activeDownloads.length && !pendingDownloads.length"
          class="empty-state"
        >
          <Activity :size="28" />
          <p>No hay descargas activas</p>
          <small>Las nuevas tareas aparecerán aquí.</small>
        </div>

        <div
          v-for="item in monitorDownloads"
          :key="item.id"
          class="download-row"
        >
          <div class="file-symbol"><FileDown :size="16" /></div>
          <div class="file-info">
            <strong :title="item.file_name">{{ item.file_name }}</strong>
            <span>{{ item.current_str }} / {{ item.total_str }} · {{ item.speed }}</span>
            <div class="progress-track">
              <div class="progress-fill" :style="{ width: `${progress(item)}%` }"></div>
            </div>
          </div>
          <div class="row-side">
            <b>{{ progress(item).toFixed(0) }}%</b>
            <span>{{ statusText(item.status) }}</span>
            <button
              v-if="['downloading', 'queued'].includes(item.status)"
              class="pause-action"
              @click="emit('pause-download', item.id)"
            >
              <Gauge :size="12" /> Pausar
            </button>
            <button
              v-if="item.status === 'paused'"
              class="resume-action"
              @click="emit('resume-download', item.id)"
            >
              <ArrowUpRight :size="12" /> Reanudar
            </button>
            <button @click="emit('cancel-download', item.id)">
              <X :size="12" /> Cancelar
            </button>
          </div>
        </div>
      </section>
    </div>

    <!-- Historial Reciente -->
    <section class="panel recent-panel">
      <div class="panel-heading">
        <div>
          <span class="eyebrow">HISTORIAL</span>
          <h2>Últimas descargas</h2>
        </div>
      </div>
      <div v-if="!recentDownloads.length" class="empty-small">
        Todavía no hay descargas terminadas.
      </div>
      <div
        v-for="item in recentDownloads"
        :key="item.id"
        class="recent-row"
      >
        <span class="recent-icon" :class="item.status">
          {{ item.status === 'completed' ? '✓' : '•' }}
        </span>
        <strong>{{ item.file_name }}</strong>
        <span class="recent-size">{{ item.total_str }}</span>
        <span class="badge" :class="item.status">{{ statusText(item.status) }}</span>
        <button
          v-if="item.status === 'completed'"
          class="open-button"
          type="button"
          title="Abrir archivo"
          aria-label="Abrir archivo"
          @click="emit('open-file', item)"
        >
          <ExternalLink :size="14" />
        </button>
        <button
          v-if="['failed', 'cancelled'].includes(item.status)"
          class="retry-button"
          type="button"
          title="Reintentar descarga"
          aria-label="Reintentar descarga"
          @click="emit('retry-download', item)"
        >
          <RotateCcw :size="14" />
        </button>
        <button
          v-if="['completed', 'failed', 'cancelled', 'skipped'].includes(item.status)"
          class="delete-button"
          type="button"
          title="Eliminar del historial"
          aria-label="Eliminar del historial"
          @click="emit('delete-download', item)"
        >
          <Trash2 :size="14" />
        </button>
      </div>
    </section>
  </div>
</template>
