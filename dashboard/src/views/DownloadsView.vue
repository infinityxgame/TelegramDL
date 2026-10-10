<script setup>
import { computed, ref, watch } from 'vue'
import {
  Activity,
  ArrowLeft,
  ArrowRight,
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
} from '../icons'
import { useI18n } from '../i18n'

import AppPill from '../components/AppPill.vue'

const { t, has } = useI18n()

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
  },
  // Descargas completadas y omitidas (por archivo ya existente) en esta
  // sesión: las manda el servidor, que solo cuenta desde su arranque, así que
  // no incluyen el historial restaurado.
  sessionCompleted: {
    type: Number,
    default: 0
  },
  sessionSkipped: {
    type: Number,
    default: 0
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

const statusPriority = (status) =>
  ({
    downloading: 2,
    paused: 2,
    queued: 2,
    pending: 2
  })[status] || 1

const compareDownloads = (a, b) => {
  const sA = statusPriority(a.status)
  const sB = statusPriority(b.status)

  if (sA !== sB) return sB - sA

  // Para Activos/En cola (prioridad >= 2), orden de creación ascendente
  if (sA >= 2) {
    if ((a.created_at || 0) !== (b.created_at || 0)) {
      return (a.created_at || 0) - (b.created_at || 0)
    }
    // Para rangos (mismo JobID con la misma fecha), priorizar MessageID
    if (a.job_id && a.job_id === b.job_id) {
      if (a.message_id !== b.message_id) {
        return (a.message_id || 0) - (b.message_id || 0)
      }
    }
    // Desempate por MessageID para rangos
    return (a.message_id || 0) - (b.message_id || 0)
  }

  // Para Historial (prioridad 1), lo más reciente primero (updated_at)
  return (b.updated_at || 0) - (a.updated_at || 0)
}

const orderedDownloads = computed(() =>
  [...props.downloads].sort(compareDownloads)
)

const monitorDownloads = computed(() =>
  orderedDownloads.value.filter((item) =>
    ['downloading', 'paused', 'pending', 'queued'].includes(item.status)
  )
)

const handleStart = () => {
  const url = inputUrl.value.trim()
  if (!url) return
  emit('start-download', url)
  inputUrl.value = ''
}

const activeDownloads = computed(() =>
  orderedDownloads.value.filter((item) =>
    ['downloading', 'paused'].includes(item.status)
  )
)

const pendingDownloads = computed(() =>
  orderedDownloads.value.filter((item) =>
    ['pending', 'queued'].includes(item.status)
  )
)

// --- Historial paginado -----------------------------------------------------
// Antes solo se veían las 15 últimas. Ahora está el historial completo, pero
// repartido en páginas para que la vista no crezca sin control.
const TAMANOS_PAGINA = [5, 10, 15]
const TAMANO_POR_DEFECTO = 10
const CLAVE_TAMANO = 'tgdl_historial_por_pagina'

// El tamaño elegido se recuerda en el navegador, como el color del loader. Se
// valida contra la lista porque el valor puede venir editado a mano.
const leerTamanoGuardado = () => {
  try {
    const guardado = Number(localStorage.getItem(CLAVE_TAMANO))
    return TAMANOS_PAGINA.includes(guardado) ? guardado : TAMANO_POR_DEFECTO
  } catch {
    return TAMANO_POR_DEFECTO
  }
}

const tamanoPagina = ref(leerTamanoGuardado())
const paginaHistorial = ref(1)

// Al cambiar el tamaño, la página en la que estabas ya no significa lo mismo:
// se vuelve al principio.
watch(tamanoPagina, (tamano) => {
  paginaHistorial.value = 1
  try {
    localStorage.setItem(CLAVE_TAMANO, tamano)
  } catch (e) {
    console.error(e)
  }
})

const historyDownloads = computed(() =>
  orderedDownloads.value.filter((item) =>
    ['completed', 'skipped', 'failed', 'cancelled'].includes(item.status)
  )
)

const totalPaginas = computed(() =>
  Math.max(1, Math.ceil(historyDownloads.value.length / tamanoPagina.value))
)

// Al borrar elementos la última página puede desaparecer: si estábamos en ella,
// retrocedemos para no quedarnos mirando una lista vacía.
watch(totalPaginas, (total) => {
  if (paginaHistorial.value > total) paginaHistorial.value = total
})

const recentDownloads = computed(() => {
  const inicio = (paginaHistorial.value - 1) * tamanoPagina.value
  return historyDownloads.value.slice(inicio, inicio + tamanoPagina.value)
})

// Como mucho 5 botones de página alrededor de la actual, para que el paginador
// no se desborde cuando el historial es largo.
const paginasVisibles = computed(() => {
  const total = totalPaginas.value
  const maximo = 5
  if (total <= maximo) return Array.from({ length: total }, (_, i) => i + 1)
  const fin = Math.min(
    total,
    Math.max(1, paginaHistorial.value - 2) + maximo - 1
  )
  const inicio = Math.max(1, fin - maximo + 1)
  return Array.from({ length: fin - inicio + 1 }, (_, i) => inicio + i)
})

const irAPagina = (pagina) => {
  paginaHistorial.value = Math.min(totalPaginas.value, Math.max(1, pagina))
}

// El estado llega del backend; si apareciera uno sin traducción (p. ej.
// 'duplicate', que solo sirve para disparar el aviso y nunca se pinta) se
// muestra el valor crudo en vez de la clave.
const statusText = (status) =>
  has('downloads.status.' + status) ? t('downloads.status.' + status) : status

const progress = (item) =>
  Math.max(0, Math.min(100, Number(item.progress || 0)))

const hasActiveOrQueued = computed(() =>
  props.downloads.some((item) =>
    ['downloading', 'queued', 'pending'].includes(item.status)
  )
)

const allActivePaused = computed(() => {
  const activeItems = props.downloads.filter((item) =>
    ['downloading', 'queued', 'pending', 'paused'].includes(item.status)
  )
  return (
    activeItems.length > 0 &&
    activeItems.every((item) => item.status === 'paused')
  )
})
</script>

<template>
  <div class="downloads-view">
    <!-- Hero Card / Formulario de Descarga -->
    <section class="hero-card">
      <div class="hero-copy">
        <span class="hero-kicker">{{ t('downloads.heroKicker') }}</span>
        <h2>{{ t('downloads.heroTitle') }}</h2>
        <p>{{ t('downloads.heroText') }}</p>
      </div>
      <div class="download-form">
        <input
          v-model="inputUrl"
          @keyup.enter="handleStart"
          placeholder="https://t.me/c/..."
          :aria-label="t('downloads.inputAria')"
        />
        <button
          class="primary-button"
          :disabled="loading || !inputUrl.trim()"
          @click="handleStart"
        >
          <span>{{
            loading ? t('downloads.adding') : t('downloads.start')
          }}</span>
          <ArrowUpRight :size="18" />
        </button>
      </div>
      <small class="form-hint">{{ t('downloads.rangeHint') }}</small>
    </section>

    <!-- Métricas y Estadísticas -->
    <section class="stats-grid">
      <div class="stat-card">
        <span class="stat-icon blue"><Activity :size="19" /></span>
        <div>
          <span class="stat-label">{{ t('downloads.statActive') }}</span>
          <strong>{{ activeDownloads.length }}</strong>
          <small>{{
            t('downloads.statActiveSub', {
              n: settings.max_concurrent_downloads
            })
          }}</small>
        </div>
      </div>
      <div class="stat-card">
        <span class="stat-icon amber"><Clock3 :size="19" /></span>
        <div>
          <span class="stat-label">{{ t('downloads.statQueued') }}</span>
          <strong>{{ pendingDownloads.length }}</strong>
          <small>{{ t('downloads.statQueuedSub') }}</small>
        </div>
      </div>
      <div class="stat-card">
        <span class="stat-icon green"><CheckCircle2 :size="19" /></span>
        <div>
          <span class="stat-label">{{ t('downloads.statCompleted') }}</span>
          <strong>{{ sessionCompleted }}</strong>
          <small>{{ t('downloads.statCompletedSub') }}</small>
        </div>
      </div>
      <div class="stat-card">
        <span class="stat-icon gray"><FileCheck :size="19" /></span>
        <div>
          <span class="stat-label">{{ t('downloads.statSkipped') }}</span>
          <strong>{{ sessionSkipped }}</strong>
          <small>{{ t('downloads.statSkippedSub') }}</small>
        </div>
      </div>
    </section>

    <!-- Monitor de Actividad en Tiempo Real -->
    <div class="content-grid-single">
      <section class="panel activity-panel">
        <div class="panel-heading">
          <div>
            <span class="eyebrow">{{ t('downloads.monitorKicker') }}</span>
            <h2>{{ t('downloads.monitorTitle') }}</h2>
          </div>
          <div class="header-actions">
            <button
              v-if="hasActiveOrQueued || allActivePaused"
              class="action-btn-mini"
              @click="allActivePaused ? emit('resume-all') : emit('pause-all')"
            >
              <component :is="allActivePaused ? Play : Pause" :size="12" />
              {{
                allActivePaused
                  ? t('downloads.resumeAll')
                  : t('downloads.pauseAll')
              }}
            </button>
            <button
              v-if="activeDownloads.length || pendingDownloads.length"
              class="action-btn-mini danger"
              @click="emit('cancel-all')"
            >
              <X :size="12" />
              <span>{{ t('downloads.cancelAll') }}</span>
            </button>
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
              {{
                t('downloads.tasksPill', {
                  n: activeDownloads.length + pendingDownloads.length
                })
              }}
            </AppPill>
          </div>
        </div>

        <div
          v-if="!activeDownloads.length && !pendingDownloads.length"
          class="empty-state"
        >
          <Activity :size="28" />
          <p>{{ t('downloads.emptyTitle') }}</p>
          <small>{{ t('downloads.emptySub') }}</small>
        </div>

        <div
          v-for="item in monitorDownloads"
          :key="item.id"
          class="download-row"
        >
          <div class="file-symbol"><FileDown :size="16" /></div>
          <div class="file-info">
            <strong :title="item.file_name">{{ item.file_name }}</strong>
            <!-- El ETA solo se pinta mientras descarga de verdad: en pausa o en
                 cola la última estimación ya no vale para nada. -->
            <span
              >{{ item.current_str }} / {{ item.total_str }} · {{ item.speed
              }}<template v-if="item.status === 'downloading' && item.eta">
                · {{ t('downloads.etaPrefix') }}
                <b class="eta">{{ item.eta }}</b></template
              ></span
            >
            <div class="progress-track">
              <div
                class="progress-fill"
                :style="{ width: `${progress(item)}%` }"
              ></div>
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
              <Gauge :size="12" /> {{ t('downloads.pause') }}
            </button>
            <button
              v-if="item.status === 'paused'"
              class="resume-action"
              @click="emit('resume-download', item.id)"
            >
              <ArrowUpRight :size="12" /> {{ t('downloads.resume') }}
            </button>
            <button @click="emit('cancel-download', item.id)">
              <X :size="12" /> {{ t('downloads.cancel') }}
            </button>
          </div>
        </div>
      </section>
    </div>

    <!-- Historial Reciente -->
    <section class="panel recent-panel">
      <div class="panel-heading">
        <div>
          <span class="eyebrow">{{ t('downloads.historyKicker') }}</span>
          <h2>{{ t('downloads.historyTitle') }}</h2>
        </div>
        <div class="header-actions">
          <AppPill>
            {{ t('downloads.historyTotal', { n: historyDownloads.length }) }}
          </AppPill>
        </div>
      </div>
      <div v-if="!recentDownloads.length" class="empty-small">
        {{ t('downloads.historyEmpty') }}
      </div>
      <div v-for="item in recentDownloads" :key="item.id" class="recent-row">
        <span class="recent-icon" :class="item.status">
          {{ item.status === 'completed' ? '✓' : '•' }}
        </span>
        <strong>{{ item.file_name }}</strong>
        <span class="recent-size">{{ item.total_str }}</span>
        <span class="badge" :class="item.status">{{
          statusText(item.status)
        }}</span>
        <button
          v-if="item.status === 'completed'"
          class="open-button"
          type="button"
          :title="t('downloads.openFile')"
          :aria-label="t('downloads.openFile')"
          @click="emit('open-file', item)"
        >
          <ExternalLink :size="14" />
        </button>
        <button
          v-if="['failed', 'cancelled'].includes(item.status)"
          class="retry-button"
          type="button"
          :title="t('downloads.retryDownload')"
          :aria-label="t('downloads.retryDownload')"
          @click="emit('retry-download', item)"
        >
          <RotateCcw :size="14" />
        </button>
        <button
          v-if="
            ['completed', 'failed', 'cancelled', 'skipped'].includes(
              item.status
            )
          "
          class="delete-button"
          type="button"
          :title="t('downloads.deleteEntry')"
          :aria-label="t('downloads.deleteEntry')"
          @click="emit('delete-download', item)"
        >
          <Trash2 :size="14" />
        </button>
      </div>

      <!-- Paginador del historial. La barra aparece en cuanto hay historial,
           aunque quepa en una sola página: si no, al elegir 15 elementos con 12
           en la lista desaparecería junto con el selector y no habría forma de
           volver a bajarlo. Lo que se oculta con una sola página son los
           botones de página, que ahí no pintan nada. -->
      <div v-if="historyDownloads.length" class="history-pager">
        <template v-if="totalPaginas > 1">
          <button
            class="pager-btn"
            type="button"
            :title="t('downloads.prevPage')"
            :aria-label="t('downloads.prevPage')"
            :disabled="paginaHistorial === 1"
            @click="irAPagina(paginaHistorial - 1)"
          >
            <ArrowLeft :size="14" />
          </button>
          <button
            v-for="pagina in paginasVisibles"
            :key="pagina"
            class="pager-btn"
            :class="{ active: pagina === paginaHistorial }"
            type="button"
            :aria-current="pagina === paginaHistorial ? 'page' : undefined"
            @click="irAPagina(pagina)"
          >
            {{ pagina }}
          </button>
          <button
            class="pager-btn"
            type="button"
            :title="t('downloads.nextPage')"
            :aria-label="t('downloads.nextPage')"
            :disabled="paginaHistorial === totalPaginas"
            @click="irAPagina(paginaHistorial + 1)"
          >
            <ArrowRight :size="14" />
          </button>
        </template>

        <div class="pager-side">
          <span v-if="totalPaginas > 1" class="pager-info">
            {{
              t('downloads.pageOf', {
                page: paginaHistorial,
                total: totalPaginas
              })
            }}
          </span>
          <select
            v-model="tamanoPagina"
            class="pager-select"
            :title="t('downloads.perPageLabel')"
            :aria-label="t('downloads.perPageLabel')"
          >
            <option
              v-for="tamano in TAMANOS_PAGINA"
              :key="tamano"
              :value="tamano"
            >
              {{ t('downloads.perPage', { n: tamano }) }}
            </option>
          </select>
        </div>
      </div>
    </section>
  </div>
</template>
