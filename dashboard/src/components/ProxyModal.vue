<script setup>
import { ref, watch } from 'vue'
import {
  X,
  Radio,
  CheckCircle2,
  AlertCircle,
  Loader2,
  Save,
  Plus,
  Info,
  Share2,
  Trash2
} from '../icons'
import { useI18n } from '../i18n'
import { useAuthToken } from '../composables/useAuthToken'

const { t } = useI18n()
const { authHeaders } = useAuthToken()

const props = defineProps({
  show: {
    type: Boolean,
    default: false
  },
  settings: {
    type: Object,
    required: true
  }
})

const emit = defineEmits(['close', 'save', 'update:settings'])

// Configuración de proxy activa/seleccionada
const form = ref({
  proxy_enabled: false,
  proxy_type: 'socks5',
  proxy_host: '',
  proxy_port: 1080,
  proxy_username: '',
  proxy_password: '',
  proxy_secret: ''
})

// Formulario para añadir un nuevo proxy
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

// Formulario para importación por enlace
const proxyLinkInput = ref('')
const testingLinkProxy = ref(false)
const linkTestResult = ref(null)

// Lista de proxies guardados y latencias
const savedProxies = ref([])
const proxyPings = ref({})

// Modal secundario de edición/detalle
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
const saving = ref(false)

watch(
  [() => props.show, () => props.settings],
  ([showVal, settingsVal]) => {
    if (showVal && settingsVal) {
      form.value = {
        proxy_enabled: !!settingsVal.proxy_enabled,
        proxy_type: settingsVal.proxy_type || 'socks5',
        proxy_host: settingsVal.proxy_host || '',
        proxy_port: settingsVal.proxy_port || 1080,
        proxy_username: settingsVal.proxy_username || '',
        proxy_password: settingsVal.proxy_password || '',
        proxy_secret: settingsVal.proxy_secret || ''
      }
      newProxyForm.value = {
        proxy_type: settingsVal.proxy_type || 'socks5',
        proxy_host: '',
        proxy_port: 1080,
        proxy_username: '',
        proxy_password: '',
        proxy_secret: ''
      }
      proxyLinkInput.value = ''
      linkTestResult.value = null
      newProxyTestResult.value = null
      editingProxyModal.value = false
      fetchSavedProxies()
    }
  },
  { immediate: true, deep: true }
)

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
        selectProxy(payload)
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
        selectProxy(payload)
        await fetchSavedProxies()
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

const isProxySelected = (p) => {
  return (
    form.value.proxy_enabled &&
    form.value.proxy_host === p.proxy_host &&
    Number(form.value.proxy_port) === Number(p.proxy_port) &&
    (form.value.proxy_type || 'socks5') === (p.proxy_type || 'socks5')
  )
}

const selectProxy = (p) => {
  form.value = {
    proxy_enabled: true,
    proxy_type: p.proxy_type || 'socks5',
    proxy_host: p.proxy_host,
    proxy_port: Number(p.proxy_port),
    proxy_username: p.proxy_username || '',
    proxy_password: p.proxy_password || '',
    proxy_secret: p.proxy_secret || ''
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
        selectProxy(p)
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
        form.value = {
          proxy_enabled: false,
          proxy_type: 'socks5',
          proxy_host: '',
          proxy_port: 1080,
          proxy_username: '',
          proxy_password: '',
          proxy_secret: ''
        }
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
  } catch (err) {
    console.error('Error copying proxy link:', err)
  }
}

const handleSave = async () => {
  saving.value = true
  try {
    const payload = {
      proxy_enabled: form.value.proxy_enabled,
      proxy_type: form.value.proxy_type,
      proxy_host: form.value.proxy_host,
      proxy_port: Number(form.value.proxy_port) || 1080,
      proxy_username: form.value.proxy_username,
      proxy_password: form.value.proxy_password,
      proxy_secret: form.value.proxy_secret,
      ...(form.value.proxy_enabled ? { proxy_notice: '' } : {})
    }
    emit('update:settings', payload)
    emit('save', payload)
    emit('close')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Teleport to="body">
    <Transition name="fade">
      <div v-if="show" class="modal-overlay" @click.self="emit('close')">
        <div class="modal-content proxy-modal">
          <div class="modal-header">
            <div class="header-title">
              <Radio :size="20" class="header-icon" />
              <h3>{{ t('settings.proxyModalTitle') }}</h3>
            </div>
            <button class="close-btn" @click="emit('close')">
              <X :size="18" />
            </button>
          </div>

          <div class="modal-body">
            <!-- Usar Proxy switch -->
            <div class="setting-line clean-line">
              <div>
                <strong>{{ t('settings.proxyEnabled') }}</strong>
                <small>{{ t('settings.proxySub') }}</small>
              </div>
              <label class="switch">
                <input v-model="form.proxy_enabled" type="checkbox" />
                <span></span>
              </label>
            </div>

            <div
              class="proxy-fields"
              :class="{ disabled: !form.proxy_enabled }"
            >
              <!-- Importar proxy mediante enlace -->
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
              <div v-if="linkTestResult" class="test-section link-test-section">
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

              <!-- Selector Tipo de Proxy para agregar -->
              <div class="proxy-type-row">
                <span class="field-label-inline">{{
                  t('settings.proxyType')
                }}</span>
                <div class="radio-group">
                  <label
                    class="radio-option"
                    :class="{
                      active: (newProxyForm.proxy_type || 'socks5') === 'socks5'
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

              <!-- Inputs de host/puerto/credenciales con botón + para agregar -->
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

              <!-- Resultado de test al agregar proxy -->
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

              <!-- Lista de proxies guardados con SCROLL -->
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
                        <span class="proxy-item-name">{{ p.proxy_host }}</span>
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

          <div class="modal-footer">
            <button type="button" class="cancel-btn" @click="emit('close')">
              {{ t('common.cancel') }}
            </button>
            <button
              type="button"
              class="save-btn"
              :disabled="saving"
              @click="handleSave"
            >
              <Save :size="16" />
              <span>{{ t('settings.proxyModalSave') }}</span>
            </button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>

  <!-- Modal para ver y editar detalles del proxy -->
  <Teleport to="body">
    <Transition name="fade">
      <div
        v-if="editingProxyModal"
        class="modal-overlay sub-modal-overlay"
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

          <div class="modal-body detail-modal-body">
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
                <label class="field-label">{{ t('settings.proxyHost') }}</label>
                <input
                  v-model="editingProxyForm.proxy_host"
                  type="text"
                  class="proxy-input"
                  placeholder="127.0.0.1"
                />
              </div>
              <div class="field-col port-col">
                <label class="field-label">{{ t('settings.proxyPort') }}</label>
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

          <div class="proxy-detail-footer">
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
</template>

<style scoped>
/* La transición `fade` viene definida en global desde App.vue con 0,8 s, y se
   notaba arrastrada al abrir y cerrar estos modales. Con la marca de scope gana
   a la global y aquí se acorta. */
.fade-enter-active,
.fade-leave-active {
  transition: opacity 0.4s ease;
}

.fade-enter-from,
.fade-leave-to {
  opacity: 0;
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

.sub-modal-overlay {
  z-index: 10010;
}

.modal-content.proxy-modal {
  background: var(--user-surface, #111827);
  border: 1px solid var(--user-border-light, rgba(255, 255, 255, 0.12));
  border-radius: 18px;
  width: min(640px, 94vw);
  max-height: 85vh;
  box-shadow:
    0 25px 50px -12px rgba(0, 0, 0, 0.5),
    0 0 35px var(--user-glow, rgba(59, 130, 246, 0.15));
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

.modal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 18px 24px;
  border-bottom: 1px solid var(--user-border-light, rgba(255, 255, 255, 0.08));
}

.header-title {
  display: flex;
  align-items: center;
  gap: 10px;
}

.header-title h3 {
  margin: 0;
  font-size: 1.1rem;
  font-weight: 600;
  color: var(--user-text, #f9fafb);
}

.header-icon {
  color: var(--user-accent, #3b82f6);
}

.close-btn {
  background: transparent;
  border: none;
  color: var(--user-text-muted, #9ca3af);
  cursor: pointer;
  padding: 4px;
  border-radius: 6px;
  display: flex;
  align-items: center;
  justify-content: center;
  transition: all 0.2s;
}

.close-btn:hover {
  color: var(--user-text, #fff);
  background: rgba(255, 255, 255, 0.1);
}

.modal-body {
  padding: 20px 24px;
  display: flex;
  flex-direction: column;
  gap: 16px;
  overflow-y: auto;
  flex: 1;
  min-height: 0;
}

.setting-line.clean-line {
  border-top: none !important;
  border-bottom: none !important;
  padding-top: 0 !important;
  padding-bottom: 0 !important;
  margin-top: 0 !important;
  margin-bottom: 0 !important;
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.setting-line strong {
  display: block;
  font-size: 0.95rem;
  color: var(--user-text, #f3f4f6);
}

.setting-line small {
  display: block;
  font-size: 0.8rem;
  color: var(--user-text-muted, #9ca3af);
  margin-top: 2px;
}

.proxy-fields {
  display: flex;
  flex-direction: column;
  gap: 14px;
  transition: opacity 0.2s;
}

.proxy-fields.disabled {
  opacity: 0.5;
  pointer-events: none;
}

.proxy-link-import-row {
  margin-bottom: 2px;
  width: 100%;
}

.link-col {
  width: 100%;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.link-input-group {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
}

.proxy-type-row {
  display: flex;
  align-items: center;
  gap: 16px;
}

.field-label-inline {
  font-size: 0.9rem;
  font-weight: 600;
  color: var(--user-text, #f3f4f6);
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
  border: 2px solid var(--user-border-light, #4b5563);
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
  border-color: var(--user-primary, var(--user-accent, #3b82f6));
  box-shadow: 0 0 8px var(--user-glow, rgba(59, 130, 246, 0.3));
}

.radio-option.active .radio-dot {
  background-color: var(--user-primary, var(--user-accent, #3b82f6));
  transform: scale(1);
}

.radio-label {
  font-size: 0.9rem;
  font-weight: 600;
  color: var(--user-text, #f3f4f6);
}

.radio-option.active .radio-label {
  color: var(--user-text, #ffffff);
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
  min-width: 130px;
}

.field-col.port-col {
  width: 80px;
  flex: 0 0 80px;
}

.field-col.user-col {
  width: 140px;
  flex: 0 0 140px;
}

.field-col.pass-col {
  width: 140px;
  flex: 0 0 140px;
}

.field-col.secret-col {
  flex: 1;
  min-width: 160px;
}

.field-col.add-col {
  width: 38px;
  flex: 0 0 38px;
}

.field-label {
  font-size: 0.88rem;
  font-weight: 600;
  color: var(--user-text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
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

.test-section {
  display: flex;
  align-items: center;
  justify-content: flex-start;
  gap: 12px;
  margin-top: 2px;
}

.link-test-section {
  margin-top: 2px;
  margin-bottom: 4px;
}

.test-result {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 0.85rem;
  font-weight: 500;
  padding: 6px 12px;
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

.color-group-title {
  font-size: 0.82rem;
  font-weight: 700;
  letter-spacing: 0.05em;
  text-transform: uppercase;
  color: var(--user-text-muted, #9ca3af);
  margin-bottom: 10px;
  display: block;
}

.saved-proxies-container {
  display: flex;
  flex-direction: column;
  margin-top: 6px;
}

.saved-proxies-list {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
  max-height: 180px;
  overflow-y: auto;
  padding-right: 4px;
}

.saved-proxies-list::-webkit-scrollbar {
  width: 6px;
}

.saved-proxies-list::-webkit-scrollbar-track {
  background: rgba(0, 0, 0, 0.2);
  border-radius: 4px;
}

.saved-proxies-list::-webkit-scrollbar-thumb {
  background: rgba(255, 255, 255, 0.2);
  border-radius: 4px;
}

.saved-proxies-list::-webkit-scrollbar-thumb:hover {
  background: rgba(255, 255, 255, 0.35);
}

@media (max-width: 640px) {
  .saved-proxies-list {
    grid-template-columns: 1fr;
  }

  .proxy-inputs-single-line {
    flex-wrap: wrap;
  }

  .field-col.host-col,
  .field-col.user-col,
  .field-col.pass-col,
  .field-col.secret-col {
    width: 100%;
    flex: 1 1 100%;
  }

  .field-col.port-col {
    width: 100px;
    flex: 0 0 100px;
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

.modal-footer {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 12px;
  padding: 16px 24px;
  border-top: 1px solid var(--user-border-light, rgba(255, 255, 255, 0.08));
  background: rgba(0, 0, 0, 0.15);
}

.cancel-btn {
  padding: 9px 16px;
  border-radius: 10px;
  border: 1px solid var(--user-border, rgba(255, 255, 255, 0.12));
  background: var(--user-surface-light, transparent);
  color: var(--user-text-dim, #9ca3af);
  font-size: 0.9rem;
  font-weight: 500;
  cursor: pointer;
}

.cancel-btn:hover {
  background: var(--user-border, rgba(255, 255, 255, 0.12));
  color: var(--user-text, #fff);
}

.save-btn {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 9px 18px;
  border-radius: 10px;
  border: none;
  background: var(--user-gradient, var(--user-primary, #3b82f6));
  color: var(--user-btn-text, var(--user-bg-base, #080d14));
  font-size: 0.9rem;
  font-weight: 700;
  cursor: pointer;
  box-shadow: 0 4px 12px var(--user-glow, rgba(59, 130, 246, 0.2));
  transition: all 0.2s;
}

.save-btn:hover {
  opacity: 0.92;
  box-shadow: 0 6px 16px var(--user-glow, rgba(59, 130, 246, 0.3));
}

/* Modal de detalle/edición de proxy */
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

.detail-modal-body {
  display: flex;
  flex-direction: column;
  gap: 14px;
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

.proxy-modal-row .field-col.user-col,
.proxy-modal-row .field-col.pass-col,
.proxy-modal-row .field-col.secret-col {
  flex: 1;
}

.proxy-detail-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 24px;
  padding-top: 16px;
  border-top: 1px solid var(--user-border-light, rgba(255, 255, 255, 0.12));
}

.proxy-detail-footer button {
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
  cursor: pointer;
}

.footer-right-actions {
  display: flex;
  align-items: center;
  gap: 10px;
}

.reset-button-alt {
  background: var(--user-surface-light, rgba(255, 255, 255, 0.05));
  border: 1px solid var(--user-border, rgba(255, 255, 255, 0.12));
  color: var(--user-text-dim, #a0aec0);
  cursor: pointer;
  transition: all 0.2s;
}

.reset-button-alt:hover {
  color: var(--user-text, #fff);
  border-color: var(--user-primary, #3b82f6);
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

.save-button {
  background: var(--user-gradient, var(--user-primary, #3b82f6));
  color: var(--user-btn-text, #080d14);
  border: none;
  transition: all 0.2s;
}

.save-button:hover {
  opacity: 0.92;
}

.proxy-share-notice {
  margin-top: 10px;
  font-size: 0.85rem;
  color: #34d399;
  font-weight: 500;
}
</style>
