<script setup>
import { ref, computed, watch } from 'vue'
import {
  KeyRound,
  Phone,
  ShieldCheck,
  CheckCircle2,
  AlertCircle,
  ArrowRight,
  ArrowLeft,
  Loader2,
  Info,
  QrCode,
  Palette,
  Check,
  X
} from '../icons'
import ProxyModal from './ProxyModal.vue'
import {
  useAuthToken,
  isWailsRuntime,
  hasStoredToken
} from '../composables/useAuthToken'
import { openExternal } from '../composables/useExternalLink'
import { useI18n } from '../i18n'
import { isLoopbackHost } from '../utils/loopback.js'
import { useQRLogin } from '../composables/useQRLogin'
import { themeMap } from '../utils/theme'

const { authHeaders } = useAuthToken()
const { t, locale, setLocale, splitOn } = useI18n()

// El banner de las credenciales nombra la sección "API development tools",
// que en my.telegram.org aparece siempre en inglés: va como marcador para
// poder seguir pintándola en cursiva dentro de la frase traducida.
const bannerBParts = computed(() => splitOn('auth.credsBannerB', 'apiTools'))

const props = defineProps({
  authStatus: {
    type: Object,
    required: true
  },
  settings: {
    type: Object,
    default: () => ({})
  }
})

const emit = defineEmits([
  'auth-success',
  'update:settings',
  'save-settings',
  'dismiss-proxy-notice'
])

const showProxyModal = ref(false)
const showThemeModal = ref(false)
const authMethod = ref('phone')

// Temas propios del panel (ids 16+). Se sacan del themeMap para que añadir uno
// nuevo no obligue a tocar esta vista.
const specialThemes = computed(() =>
  Object.keys(themeMap)
    .map(Number)
    .filter((id) => id >= 16)
    .sort((a, b) => a - b)
)

// Aplica el tema al instante (update:settings lo pinta en App.vue y deja la
// copia local del dispositivo) y, si esta pantalla tiene token, lo guarda en el
// servidor. Sin token el PUT respondería 401: basta con la copia local, que es
// la que hace que la pantalla de acceso recuerde el tema al recargar.
const selectTheme = (id) => {
  emit('update:settings', { color_id: id })
  if (hasStoredToken()) emit('save-settings')
  // Se cierra al elegir: el cambio ya está aplicado y el modal no aporta nada
  // más (antes había que cerrarlo a mano después de cada prueba de tema).
  showThemeModal.value = false
}

// Mismo logo que el sidebar; Vite lo sirve desde /public con la base URL.
const logoUrl = `${import.meta.env.BASE_URL}telegramdl-android-icon.svg`

// Paso que dicta el servidor segun el estado real de la sesion de Telegram.
const serverStep = computed(() => {
  if (
    !props.authStatus.has_credentials ||
    props.authStatus.state === 'UNCONFIGURED'
  )
    return 1
  if (
    props.authStatus.state === 'NOT_LOGGED_IN' ||
    props.authStatus.state === 'NEED_PHONE'
  )
    return 2
  if (props.authStatus.state === 'WAITING_CODE') return 3
  if (props.authStatus.state === 'WAITING_2FA') return 4
  if (props.authStatus.state === 'LOGGED_IN') return 5
  return 1
})

// Paso que el usuario ha forzado con el boton "Atras". Mientras valga null el
// asistente sigue al servidor; en cuanto una llamada al servidor sale bien se
// vuelve a poner a null para no quedarse anclado en una pantalla vieja.
const stepOverride = ref(null)

const currentStep = computed(() => stepOverride.value ?? serverStep.value)

// El servidor ya no devuelve api_id ni api_hash (el token de acceso no debe
// servir para leer las credenciales de Telegram), así que estos campos siempre
// arrancan vacíos. Este paso solo se muestra cuando todavía no hay credenciales
// configuradas, que es justo cuando no habría nada que rellenar.
const apiId = ref('')
const apiHash = ref('')
const phoneNumber = ref(props.authStatus.phone_number || '')
const code = ref('')
const canResendCode = ref(false)
const password = ref('')

watch(
  () => props.authStatus,
  (val) => {
    if (val) {
      if (val.phone_number && !phoneNumber.value)
        phoneNumber.value = val.phone_number
    }
  },
  { immediate: true }
)

const loading = ref(false)
const errorMessage = ref('')
const connectionWarning = ref('')
const credentialsSaved = ref(
  props.authStatus.has_credentials === true &&
    props.authStatus.state === 'CONNECTING'
)
const connecting = ref(false)
// Un error pertenece al paso en el que se produjo: al movernos de paso deja de
// tener sentido y se limpia, para que no viaje a la pantalla siguiente.
watch(currentStep, () => {
  errorMessage.value = ''
})

const apiCall = async (url, body) => {
  loading.value = true
  errorMessage.value = ''
  try {
    // La cabecera propia no es un secreto: obliga al navegador a hacer el
    // preflight de CORS, que es lo que impide que otra página web dispare este
    // endpoint (responde sin token) desde el navegador del usuario.
    const res = await fetch(url, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...authHeaders(),
        'X-TGDL-Request': '1'
      },
      body: JSON.stringify(body)
    })

    // Verificar que la respuesta tenga contenido antes de intentar parsear JSON
    const text = await res.text()
    if (!text || text.trim() === '') {
      throw new Error(t('auth.errNoResponse'))
    }

    let data
    try {
      data = JSON.parse(text)
    } catch {
      throw new Error(t('auth.errInvalidResponse'))
    }

    if (!res.ok) {
      throw new Error(data.detail || data.error || t('auth.errProcess'))
    }
    // La accion ha ido bien: devolvemos el mando al estado del servidor.
    stepOverride.value = null
    return data
  } catch (err) {
    errorMessage.value = err.message
    throw err
  } finally {
    loading.value = false
  }
}

const { qrImage, qrStatus, qrError, startQRLogin, cancelQRLogin } = useQRLogin({
  authHeaders,
  t,
  authStatus: props.authStatus,
  emit,
  apiCall
})

// Alterna entre los dos métodos sin mantener dos flujos de autenticación activos.
const switchAuthMethod = async (method) => {
  if (loading.value || authMethod.value === method) return

  if (method === 'phone') {
    if (qrStatus.value === 'WAITING_SCAN' || qrStatus.value === 'STARTING') {
      await cancelQRLogin()
    }
    authMethod.value = 'phone'
    // Cancelar/iniciar QR puede devolver el control al estado del servidor,
    // que todavía puede ser WAITING_CODE por un intento telefónico anterior.
    // Mantener el selector visible evita mostrar ese formulario obsoleto.
    if (props.authStatus.state === 'WAITING_CODE') {
      stepOverride.value = 2
    }
    return
  }

  authMethod.value = 'qr'
  // La llamada de inicio QR pasa por apiCall(), que normalmente elimina el
  // override para seguir el estado del servidor. En este caso WAITING_CODE
  // puede pertenecer al intento telefónico abandonado: la elección explícita
  // del usuario debe mantener visible el panel QR.
  stepOverride.value = 2
  if (qrStatus.value !== 'WAITING_SCAN' && qrStatus.value !== 'STARTING') {
    await startQRLogin()
  }

  // No forzar el selector si el QR ya completó la autenticación y el servidor
  // avanzó a 2FA o a la sesión iniciada.
  if (qrStatus.value !== 'WAITING_2FA' && qrStatus.value !== 'LOGGED_IN') {
    stepOverride.value = 2
  }
}

const pollConnectionStatus = async () => {
  if (connecting.value) return
  connecting.value = true
  errorMessage.value = ''
  connectionWarning.value = ''
  const deadline = Date.now() + 45000
  try {
    while (Date.now() < deadline) {
      const res = await fetch('/api/auth/status', {
        headers: { ...authHeaders(), 'X-TGDL-Request': '1' }
      })
      const text = await res.text()
      if (!text) throw new Error(t('auth.errNoResponse'))
      let data
      try {
        data = JSON.parse(text)
      } catch {
        throw new Error(t('auth.errInvalidResponse'))
      }
      if (!res.ok)
        throw new Error(data.detail || data.error || t('auth.errProcess'))
      const state = data.state
      if (data.connection_error)
        connectionWarning.value = t('auth.connectErrorDetails', {
          error: data.connection_error
        })
      if (
        state === 'NEED_PHONE' ||
        state === 'NOT_LOGGED_IN' ||
        state === 'WAITING_CODE' ||
        state === 'WAITING_2FA' ||
        state === 'LOGGED_IN'
      ) {
        emit('auth-success', data)
        connectionWarning.value = ''
        credentialsSaved.value = true
        return
      }
      await new Promise((resolve) => setTimeout(resolve, 900))
    }
    errorMessage.value = t('auth.connectTimeout')
  } catch (err) {
    errorMessage.value = err.message || t('auth.connectFailed')
  } finally {
    connecting.value = false
  }
}

const retryConnection = () => {
  if (connecting.value) return
  pollConnectionStatus()
}

watch(
  () => props.authStatus.state,
  (state) => {
    if (state === 'CONNECTING' && props.authStatus.has_credentials) {
      credentialsSaved.value = true
      pollConnectionStatus()
    }
  },
  { immediate: true }
)

const saveCredentials = async () => {
  if (loading.value) return
  const trimmedApiId = apiId.value.trim()
  const trimmedApiHash = apiHash.value.trim()
  if (!trimmedApiId || !trimmedApiHash) {
    errorMessage.value = t('auth.errFillBoth')
    return
  }
  if (!/^\d+$/.test(trimmedApiId)) {
    errorMessage.value = t('auth.errApiIdFormat')
    return
  }
  if (!/^[0-9a-fA-F]{32}$/.test(trimmedApiHash)) {
    errorMessage.value = t('auth.errApiHashFormat')
    return
  }
  const data = await apiCall('/api/auth/credentials', {
    api_id: trimmedApiId,
    api_hash: trimmedApiHash
  })
  credentialsSaved.value = true
  emit('auth-success', data)
  if (data.state === 'CONNECTING') pollConnectionStatus()
}

const sendCode = async () => {
  if (loading.value) return
  if (!phoneNumber.value) {
    errorMessage.value = t('auth.errPhone')
    return
  }
  let cleanPhone = phoneNumber.value.trim().replace(/\s+/g, '')
  if (!cleanPhone.startsWith('+')) {
    cleanPhone = '+' + cleanPhone
  }
  phoneNumber.value = cleanPhone
  const data = await apiCall('/api/auth/send-code', {
    phone_number: cleanPhone
  })
  canResendCode.value = Boolean(data.can_resend)
  emit('auth-success', {
    ...props.authStatus,
    state: 'WAITING_CODE',
    phone_number: cleanPhone
  })
}

const resendCode = async () => {
  if (loading.value || !canResendCode.value) return
  const data = await apiCall('/api/auth/resend-code', {})
  canResendCode.value = Boolean(data.can_resend)
}

const verifyCode = async () => {
  if (loading.value) return
  if (!code.value) {
    errorMessage.value = t('auth.errCode')
    return
  }
  const data = await apiCall('/api/auth/verify-code', {
    phone_number: phoneNumber.value,
    code: code.value.trim()
  })
  code.value = ''
  if (data.status === '2fa_required' || data.state === 'WAITING_2FA') {
    emit('auth-success', { ...props.authStatus, state: 'WAITING_2FA' })
  } else {
    emit('auth-success', { ...data, authenticated: true })
  }
}

const insecureTransport = computed(() => {
  if (isWailsRuntime()) return false
  return (
    !isLoopbackHost(window.location.hostname) &&
    window.location.protocol !== 'https:'
  )
})

const verify2FA = async () => {
  if (loading.value) return
  if (!password.value) {
    errorMessage.value = t('auth.err2fa')
    return
  }
  const data = await apiCall('/api/auth/verify-2fa', {
    password: password.value
  })
  password.value = ''
  emit('auth-success', { ...data, authenticated: true })
}

const goBack = async () => {
  if (loading.value) return
  errorMessage.value = ''

  if (currentStep.value === 2) {
    if (qrStatus.value === 'WAITING_SCAN' || qrStatus.value === 'STARTING') {
      await cancelQRLogin()
    }
    stepOverride.value = 1
    return
  }

  if (currentStep.value === 3) {
    code.value = ''
    stepOverride.value = 2
    return
  }

  if (currentStep.value === 4) {
    loading.value = true
    code.value = ''
    password.value = ''
    try {
      await fetch('/api/auth/logout', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          ...authHeaders(),
          'X-TGDL-Request': '1'
        }
      })
    } catch (e) {
      console.error(e)
    }
    loading.value = false
    stepOverride.value = 2
    emit('auth-success', {
      ...props.authStatus,
      authenticated: false,
      state: props.authStatus.has_credentials ? 'NOT_LOGGED_IN' : 'UNCONFIGURED'
    })
  }
}

// Descarta la vuelta atras al paso 1 y devuelve al usuario donde lo dejo el
// servidor (solo aparece cuando ya habia credenciales guardadas).
const cancelBack = () => {
  errorMessage.value = ''
  stepOverride.value = null
}
</script>

<template>
  <div class="auth-overlay">
    <!-- Selector de tema: vive fuera de la tarjeta, en el fondo de la pantalla,
         para no robarle ancho al título del encabezado. -->
    <button
      type="button"
      class="auth-theme-fab"
      :title="t('auth.themeBtn')"
      :aria-label="t('auth.themeBtn')"
      @click="showThemeModal = true"
    >
      <Palette :size="18" />
    </button>

    <div class="auth-card">
      <!-- Header -->
      <div class="auth-header">
        <div class="auth-logo">
          <img :src="logoUrl" alt="" />
        </div>
        <div class="auth-title-box">
          <h2>{{ t('auth.title') }}</h2>
          <p>{{ t('auth.subtitle') }}</p>
        </div>
        <div class="header-controls">
          <button
            type="button"
            class="proxy-badge-btn"
            :class="{ enabled: settings?.proxy_enabled }"
            :title="t('settings.proxyModalTitle')"
            @click="showProxyModal = true"
          >
            <Radio :size="15" />
            <span>{{ t('auth.proxyBtn') }}</span>
            <span v-if="settings?.proxy_enabled" class="status-dot"></span>
          </button>

          <label
            class="lang-switch-toggle"
            :title="locale === 'es' ? 'Switch to English' : 'Cambiar a Español'"
          >
            <input
              type="checkbox"
              :checked="locale === 'en'"
              @change="setLocale(locale === 'es' ? 'en' : 'es')"
            />
            <span class="lang-switch-track">
              <span
                class="lang-switch-label left"
                :class="{ active: locale === 'es' }"
                >ES</span
              >
              <span
                class="lang-switch-label right"
                :class="{ active: locale === 'en' }"
                >EN</span
              >
              <span class="lang-switch-thumb"></span>
            </span>
          </label>
        </div>
      </div>

      <!-- Banner Informativo de Proxy -->
      <div v-if="settings?.proxy_notice" class="proxy-notice-card" role="alert">
        <div class="proxy-notice-content">
          <AlertCircle :size="18" class="proxy-notice-icon" />
          <div class="proxy-notice-body">
            <strong>{{ t('settings.proxyNoticeTitle') }}</strong>
            <p>{{ settings.proxy_notice }}</p>
          </div>
        </div>
        <button
          type="button"
          class="proxy-notice-accept-btn"
          @click="emit('dismiss-proxy-notice')"
        >
          <Check :size="14" />
          <span>{{ t('common.accept') }}</span>
        </button>
      </div>

      <!-- Stepper -->
      <div class="stepper">
        <div
          class="step-item"
          :class="{ active: currentStep === 1, done: currentStep > 1 }"
        >
          <span class="step-num">1</span>
          <span class="step-label">{{ t('auth.stepApi') }}</span>
        </div>
        <div class="step-divider"></div>
        <div
          class="step-item"
          :class="{ active: currentStep === 2, done: currentStep > 2 }"
        >
          <span class="step-num">2</span>
          <span class="step-label">{{ t('auth.stepPhone') }}</span>
        </div>
        <div class="step-divider"></div>
        <div
          class="step-item"
          :class="{
            active: currentStep === 3 || currentStep === 4,
            done: currentStep > 4
          }"
        >
          <span class="step-num">3</span>
          <span class="step-label">{{ t('auth.stepCode') }}</span>
        </div>
        <div class="step-divider"></div>
        <div
          class="step-item"
          :class="{ active: currentStep === 5, done: currentStep === 5 }"
        >
          <span class="step-num">4</span>
          <span class="step-label">{{ t('auth.stepDone') }}</span>
        </div>
      </div>

      <!-- Step 1: API credentials / connection status -->
      <div v-if="currentStep === 1" class="step-content">
        <div v-if="credentialsSaved" class="connection-state">
          <div class="step-intro">
            <Loader2 v-if="connecting" class="spin icon-accent" :size="28" />
            <AlertCircle v-else class="icon-accent" :size="28" />
            <div>
              <h3>
                {{
                  connecting
                    ? t('auth.connectingTitle')
                    : t('auth.connectFailedTitle')
                }}
              </h3>
              <p>
                {{
                  connecting
                    ? t('auth.connectingText')
                    : t('auth.connectFailedText')
                }}
              </p>
            </div>
          </div>
          <div
            v-if="errorMessage"
            class="auth-alert danger"
            role="alert"
            aria-live="polite"
          >
            <AlertCircle :size="18" class="alert-icon" />
            <span>{{ errorMessage }}</span>
          </div>
          <div
            v-if="connectionWarning"
            class="info-banner"
            role="status"
            aria-live="polite"
          >
            <Info :size="16" />
            <span>{{ connectionWarning }}</span>
          </div>
          <div class="button-group">
            <button
              class="auth-button primary full-width"
              :disabled="connecting"
              @click="retryConnection"
            >
              <Loader2 v-if="connecting" class="spin" :size="18" />
              <span v-else>{{ t('auth.connectRetry') }}</span>
            </button>
            <button
              class="auth-button secondary full-width"
              :disabled="connecting"
              @click="credentialsSaved = false"
            >
              {{ t('auth.editCredentials') }}
            </button>
          </div>
        </div>
        <div v-else>
          <div class="step-intro">
            <KeyRound :size="28" class="icon-accent" />
            <div>
              <h3>{{ t('auth.credsTitle') }}</h3>
              <p>{{ t('auth.credsText') }}</p>
            </div>
          </div>

          <div
            v-if="errorMessage"
            class="auth-alert danger"
            role="alert"
            aria-live="polite"
          >
            <AlertCircle :size="18" class="alert-icon" />
            <span>{{ errorMessage }}</span>
          </div>

          <div class="info-banner">
            <Info :size="16" />
            <span>
              {{ t('auth.credsBannerA') }}
              <a
                href="https://my.telegram.org"
                target="_blank"
                rel="noopener noreferrer"
                @click="openExternal('https://my.telegram.org', $event)"
                >my.telegram.org</a
              >
              {{ bannerBParts[0] }}<i>API development tools</i
              >{{ bannerBParts[1] }}
            </span>
          </div>

          <div class="form-group">
            <label>TGDL_API_ID</label>
            <input
              v-model="apiId"
              type="text"
              :placeholder="t('auth.apiIdPlaceholder')"
              @keyup.enter="saveCredentials"
            />
          </div>

          <div class="form-group">
            <label>TGDL_API_HASH</label>
            <!-- SEC-34: el hash es un secreto de la app, no debe verse en pantalla
               (ni en capturas/grabaciones); autocomplete="off" igual que el campo
               de token de RemoteLogin para que el navegador no lo guarde. -->
            <input
              v-model="apiHash"
              type="password"
              :placeholder="t('auth.apiHashPlaceholder')"
              autocomplete="off"
              @keyup.enter="saveCredentials"
            />
          </div>

          <div class="button-group">
            <button
              v-if="stepOverride === 1 && serverStep > 1"
              class="auth-button secondary btn-back"
              :disabled="loading"
              @click="cancelBack"
            >
              <ArrowLeft :size="16" />
              <span>{{ t('common.cancel') }}</span>
            </button>
            <button
              class="auth-button primary"
              :disabled="loading"
              @click="saveCredentials"
            >
              <Loader2 v-if="loading" class="spin" :size="18" />
              <template v-else>
                <span>{{ t('auth.saveContinue') }}</span>
                <ArrowRight :size="18" />
              </template>
            </button>
          </div>
        </div>
      </div>

      <!-- Step 2: Método de autenticación -->
      <div v-else-if="currentStep === 2" class="step-content">
        <div
          class="auth-method-switch"
          role="tablist"
          :aria-label="t('auth.methodLabel')"
        >
          <button
            type="button"
            class="auth-method-tab"
            :class="{ active: authMethod === 'phone' }"
            :aria-selected="authMethod === 'phone'"
            role="tab"
            :disabled="loading"
            @click="switchAuthMethod('phone')"
          >
            <Phone :size="16" />
            <span>{{ t('auth.phoneMethod') }}</span>
          </button>
          <button
            type="button"
            class="auth-method-tab"
            :class="{ active: authMethod === 'qr' }"
            :aria-selected="authMethod === 'qr'"
            role="tab"
            :disabled="loading"
            @click="switchAuthMethod('qr')"
          >
            <QrCode :size="15" />
            <span>{{ t('auth.qrMethod') }}</span>
          </button>
        </div>

        <template v-if="authMethod === 'phone'">
          <div class="step-intro">
            <Phone :size="28" class="icon-accent" />
            <div>
              <h3>{{ t('auth.phoneTitle') }}</h3>
              <p>{{ t('auth.phoneText') }}</p>
            </div>
          </div>

          <div
            v-if="errorMessage"
            class="auth-alert danger"
            role="alert"
            aria-live="polite"
          >
            <AlertCircle :size="18" class="alert-icon" />
            <span>{{ errorMessage }}</span>
          </div>

          <div class="form-group">
            <label>{{ t('auth.phoneLabel') }}</label>
            <input
              v-model="phoneNumber"
              type="tel"
              :placeholder="t('auth.phonePlaceholder')"
              @keyup.enter="sendCode"
            />
            <small class="help-text">{{ t('auth.phoneHelp') }}</small>
          </div>

          <div class="button-group">
            <button
              class="auth-button secondary btn-back"
              :disabled="loading"
              @click="goBack"
            >
              <ArrowLeft :size="16" />
              <span>{{ t('common.back') }}</span>
            </button>
            <button
              class="auth-button primary"
              :disabled="loading"
              @click="sendCode"
            >
              <Loader2 v-if="loading" class="spin" :size="18" />
              <template v-else>
                <span>{{ t('auth.sendCode') }}</span>
                <ArrowRight :size="18" />
              </template>
            </button>
          </div>
        </template>

        <div v-else class="qr-login-panel">
          <div class="step-intro">
            <QrCode :size="26" class="icon-accent" />
            <div>
              <h3>{{ t('auth.qrTitle') }}</h3>
              <p>{{ t('auth.qrInstructions') }}</p>
            </div>
          </div>
          <div v-if="qrImage" class="qr-code-frame">
            <img :src="qrImage" :alt="t('auth.qrAlt')" />
          </div>
          <p v-if="qrStatus === 'WAITING_SCAN'" class="qr-status">
            {{ t('auth.qrWaiting') }}
          </p>
          <p v-else-if="qrStatus === 'STARTING'" class="help-text qr-status">
            {{ t('auth.qrPreparing') }}
          </p>
          <p v-else-if="!qrError" class="help-text qr-status">
            {{ t('auth.qrPreparing') }}
          </p>
          <p v-if="qrError" class="auth-alert danger" role="alert">
            {{ qrError }}
          </p>
          <div class="button-group">
            <button
              class="auth-button secondary btn-back"
              :disabled="loading"
              @click="goBack"
            >
              <ArrowLeft :size="16" />
              <span>{{ t('common.back') }}</span>
            </button>
            <button
              v-if="qrStatus === 'WAITING_SCAN' || qrStatus === 'STARTING'"
              class="auth-button secondary"
              :disabled="loading"
              @click="switchAuthMethod('phone')"
            >
              {{ t('common.cancel') }}
            </button>
            <button
              v-else
              class="auth-button primary"
              :disabled="loading"
              @click="startQRLogin"
            >
              <Loader2 v-if="loading" class="spin" :size="18" />
              <span v-else>{{ t('auth.qrRetry') }}</span>
            </button>
          </div>
        </div>
      </div>

      <!-- Step 3: Código de Verificación -->
      <div v-else-if="currentStep === 3" class="step-content">
        <div class="step-intro">
          <ShieldCheck :size="28" class="icon-accent" />
          <div>
            <h3>{{ t('auth.codeTitle') }}</h3>
            <p>{{ t('auth.codeText', { phone: phoneNumber }) }}</p>
          </div>
        </div>

        <div
          v-if="errorMessage"
          class="auth-alert danger"
          role="alert"
          aria-live="polite"
        >
          <AlertCircle :size="18" class="alert-icon" />
          <span>{{ errorMessage }}</span>
        </div>

        <div class="form-group">
          <label>{{ t('auth.codeLabel') }}</label>
          <input
            v-model="code"
            type="text"
            :placeholder="t('auth.codePlaceholder')"
            maxlength="6"
            class="code-input"
            @keyup.enter="verifyCode"
          />
        </div>

        <button
          v-if="canResendCode"
          class="auth-button secondary full-width"
          :disabled="loading"
          @click="resendCode"
        >
          <Loader2 v-if="loading" class="spin" :size="16" />
          <span>{{ t('auth.resendCode') }}</span>
        </button>

        <div class="button-group">
          <button
            class="auth-button secondary btn-back"
            :disabled="loading"
            @click="goBack"
          >
            <ArrowLeft :size="16" />
            <span>{{ t('common.back') }}</span>
          </button>
          <button
            class="auth-button primary"
            :disabled="loading"
            @click="verifyCode"
          >
            <Loader2 v-if="loading" class="spin" :size="18" />
            <template v-else>
              <span>{{ t('auth.verify') }}</span>
              <ArrowRight :size="18" />
            </template>
          </button>
        </div>
      </div>

      <!-- Step 4: 2FA Password -->
      <div v-else-if="currentStep === 4" class="step-content">
        <div class="step-intro">
          <ShieldCheck :size="28" class="icon-amber" />
          <div>
            <h3>{{ t('auth.tfaTitle') }}</h3>
            <p>{{ t('auth.tfaText') }}</p>
          </div>
        </div>

        <div
          v-if="errorMessage"
          class="auth-alert danger"
          role="alert"
          aria-live="polite"
        >
          <AlertCircle :size="18" class="alert-icon" />
          <span>{{ errorMessage }}</span>
        </div>

        <div class="form-group">
          <label>{{ t('auth.tfaLabel') }}</label>
          <input
            v-model="password"
            type="password"
            :placeholder="t('auth.tfaPlaceholder')"
            @keyup.enter="verify2FA"
          />
          <!-- Advertencia en línea junto al campo: no bloquea el envío (ver
               insecureTransport en el script), solo pone el riesgo sobre la
               mesa antes de que el usuario pulse "Iniciar Sesión". -->
          <div
            v-if="insecureTransport"
            class="auth-alert danger"
            role="alert"
            aria-live="polite"
          >
            <AlertCircle :size="18" class="alert-icon" />
            <span>{{ t('auth.tfaHttpWarning') }}</span>
          </div>
        </div>

        <div class="button-group">
          <button
            class="auth-button secondary btn-back"
            :disabled="loading"
            @click="goBack"
          >
            <ArrowLeft :size="16" />
            <span>{{ t('common.back') }}</span>
          </button>
          <button
            class="auth-button primary"
            :disabled="loading"
            @click="verify2FA"
          >
            <Loader2 v-if="loading" class="spin" :size="18" />
            <template v-else>
              <span>{{ t('auth.signIn') }}</span>
              <ArrowRight :size="18" />
            </template>
          </button>
        </div>
        <small class="help-text back-note">
          {{ t('auth.backNote') }}
        </small>
      </div>

      <!-- Step 5: Éxito -->
      <div v-else-if="currentStep === 5" class="step-content text-center">
        <div class="success-icon">
          <CheckCircle2 :size="48" />
        </div>
        <h3>{{ t('auth.successTitle') }}</h3>
        <p v-if="authStatus.user">
          {{ t('auth.connectedAs') }}
          <strong>{{ authStatus.user.first_name }}</strong>
          <span v-if="authStatus.user.username"
            >(@{{ authStatus.user.username }})</span
          >
        </p>
        <p v-else>{{ t('auth.successFallback') }}</p>

        <button
          class="auth-button primary full-width"
          @click="
            $emit('auth-success', {
              ...authStatus,
              authenticated: true,
              state: 'LOGGED_IN'
            })
          "
        >
          <span>{{ t('auth.goDashboard') }}</span>
          <ArrowRight :size="18" />
        </button>
      </div>
    </div>

    <!-- Selector de tema: solo los temas del panel (color_id), sin el loader -->
    <Teleport to="body">
      <div
        v-if="showThemeModal"
        class="theme-modal-overlay"
        role="dialog"
        aria-modal="true"
        :aria-label="t('auth.themeTitle')"
        @click.self="showThemeModal = false"
      >
        <div class="theme-modal">
          <div class="theme-modal-header">
            <h3>{{ t('auth.themeTitle') }}</h3>
            <button
              type="button"
              class="theme-modal-close"
              :title="t('common.close')"
              :aria-label="t('common.close')"
              @click="showThemeModal = false"
            >
              <X :size="18" />
            </button>
          </div>
          <p class="theme-modal-hint">{{ t('auth.themeHint') }}</p>

          <span class="color-group-title">{{
            t('settings.telegramGroup')
          }}</span>
          <div class="color-selector-container">
            <div class="color-row">
              <button
                v-for="id in [0, 1, 2, 3, 4, 5, 6, 7]"
                :key="'theme-solid-' + id"
                type="button"
                class="color-dot"
                :class="{ active: settings?.color_id === id }"
                :style="{ background: themeMap[id]?.gradient || '#38a7ff' }"
                :title="t('settings.colorN', { n: id })"
                @click="selectTheme(id)"
              ></button>
            </div>
            <div class="color-row">
              <button
                v-for="id in [8, 9, 10, 11, 12, 13, 14, 15]"
                :key="'theme-gradient-' + id"
                type="button"
                class="color-dot gradient-dot"
                :class="{ active: settings?.color_id === id }"
                :style="{
                  background: `linear-gradient(135deg, ${themeMap[id]?.primary || '#38a7ff'} 49.8%, ${themeMap[id]?.secondary || '#b48bf2'} 50.2%)`
                }"
                :title="t('settings.gradientN', { n: id })"
                @click="selectTheme(id)"
              ></button>
            </div>
          </div>

          <span class="color-group-title">{{
            t('settings.thematicGroup')
          }}</span>
          <div class="themes-list">
            <button
              v-for="id in specialThemes"
              :key="'theme-special-' + id"
              type="button"
              class="theme-chip"
              :class="{ active: settings?.color_id === id }"
              :title="themeMap[id]?.name || t('settings.themeN', { n: id })"
              @click="selectTheme(id)"
            >
              <span
                class="theme-swatch"
                :style="{ background: themeMap[id]?.gradient }"
              ></span>
              <span class="theme-name">{{
                themeMap[id]?.name || t('settings.themeN', { n: id })
              }}</span>
            </button>
          </div>
        </div>
      </div>
    </Teleport>

    <ProxyModal
      :show="showProxyModal"
      :settings="settings"
      @close="showProxyModal = false"
      @update:settings="(p) => emit('update:settings', p)"
      @save="(p) => emit('save-settings', p)"
    />
  </div>
</template>

<style scoped>
.auth-overlay {
  position: fixed;
  inset: 0;
  z-index: 1000;
  background: color-mix(in srgb, var(--user-bg-base), transparent 8%);
  backdrop-filter: blur(12px);
  display: grid;
  place-items: center;
  padding: 20px;
}

.auth-card {
  width: min(560px, 92vw);
  background: var(--user-surface);
  border: 1px solid var(--user-border);
  border-radius: 20px;
  padding: 30px;
  box-shadow: 0 25px 60px rgba(0, 0, 0, 0.6);
  animation: modalRise 0.35s cubic-bezier(0.16, 1, 0.3, 1);
  box-sizing: border-box;
  min-height: 0;
  /* Si el contenido no cabe en la ventana, la tarjeta se desplaza por dentro
     en vez de salirse por abajo. */
  max-height: calc(100vh - 40px);
  max-height: calc(100dvh - 40px);
  overflow-y: auto;
}

/* Botón de tema flotante en la esquina superior derecha del fondo. */
.auth-theme-fab {
  position: absolute;
  top: 20px;
  right: 20px;
  z-index: 2;
  display: grid;
  place-items: center;
  width: 40px;
  height: 40px;
  padding: 0;
  border-radius: 50%;
  border: 1px solid var(--user-border);
  background: var(--user-surface-light);
  color: var(--user-text-dim);
  cursor: pointer;
  box-shadow: 0 8px 22px rgba(0, 0, 0, 0.45);
  transition:
    background 0.2s,
    color 0.2s,
    border-color 0.2s,
    transform 0.2s;
}

.auth-theme-fab:hover {
  background: var(--user-surface);
  color: var(--user-accent);
  border-color: var(--user-primary);
  transform: translateY(-1px);
}

.auth-header {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 24px;
}

.header-controls {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
  margin-left: auto;
}

.proxy-badge-btn {
  display: flex;
  align-items: center;
  gap: 6px;
  background: var(--user-surface-light, rgba(255, 255, 255, 0.06));
  border: 1px solid var(--user-border, rgba(255, 255, 255, 0.12));
  color: var(--user-text-dim, #9ca3af);
  padding: 5px 12px;
  border-radius: 20px;
  font-size: 0.8rem;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.2s;
}

.proxy-badge-btn:hover {
  background: var(--user-surface, rgba(255, 255, 255, 0.12));
  color: var(--user-text);
  border-color: var(--user-primary, var(--user-accent));
}

.proxy-badge-btn.enabled {
  background: var(--user-primary-soft, rgba(59, 130, 246, 0.12));
  border-color: var(--user-primary, rgba(59, 130, 246, 0.4));
  color: var(--user-primary, #60a5fa);
}

.proxy-badge-btn .status-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background-color: #34d399;
  box-shadow: 0 0 6px #34d399;
}

.auth-title-box {
  flex: 1;
  min-width: 0;
}

.lang-switch-toggle {
  display: inline-flex;
  align-items: center;
  cursor: pointer;
  user-select: none;
  flex-shrink: 0;
}

.lang-switch-toggle input {
  position: absolute;
  opacity: 0;
  width: 0;
  height: 0;
  pointer-events: none;
}

.lang-switch-track {
  position: relative;
  display: flex;
  align-items: center;
  width: 64px;
  height: 28px;
  background: rgba(0, 0, 0, 0.35);
  border: 1px solid rgba(255, 255, 255, 0.2);
  border-radius: 14px;
  padding: 2px;
  box-sizing: border-box;
  transition: all 0.25s ease;
}

.lang-switch-label {
  flex: 1;
  text-align: center;
  z-index: 2;
  font-size: 11px;
  font-weight: 700;
  color: rgba(255, 255, 255, 0.55);
  transition: color 0.25s ease;
  line-height: 22px;
}

.lang-switch-label.active {
  color: #000000;
  font-weight: 800;
}

.lang-switch-thumb {
  position: absolute;
  top: 2px;
  left: 2px;
  width: 28px;
  height: 22px;
  background: #ffffff;
  border-radius: 11px;
  z-index: 1;
  transition: transform 0.25s cubic-bezier(0.16, 1, 0.3, 1);
  box-shadow: 0 2px 5px rgba(0, 0, 0, 0.4);
}

.lang-switch-toggle input:checked + .lang-switch-track .lang-switch-thumb {
  transform: translateX(30px);
}

.lang-switch-toggle:hover .lang-switch-track {
  border-color: rgba(255, 255, 255, 0.35);
}

.lang-switch-toggle input:focus-visible + .lang-switch-track {
  outline: 2px solid var(--user-primary, #38a7ff);
  outline-offset: 2px;
}

.auth-logo {
  width: 44px;
  height: 44px;
  flex-shrink: 0;
  border-radius: 12px;
  overflow: hidden;
  display: grid;
  place-items: center;
  /* Mismo vaivén que el logo del sidebar (brandFloat está en style.css). */
  animation: brandFloat 5s ease-in-out infinite;
}

.auth-logo img {
  width: 100%;
  height: 100%;
  display: block;
}

.auth-header h2 {
  font-family: 'Space Grotesk', sans-serif;
  font-size: 19px;
  font-weight: 700;
  color: #f1f7ff;
  margin: 0;
  white-space: nowrap;
}

/* En pantallas estrechas el título vuelve a partirse en vez de desbordarse. */
@media (max-width: 600px) {
  .auth-header h2 {
    white-space: normal;
  }
}

.auth-header p {
  font-size: 13px;
  color: #7d96b0;
  margin: 2px 0 0;
}

/* Stepper */
.stepper {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 26px;
  padding: 10px 0;
  border-bottom: 1px solid var(--user-border);
  gap: 4px;
}

.step-item {
  display: flex;
  align-items: center;
  gap: 5px;
  opacity: 0.7;
  transition: opacity 0.3s;
  flex-shrink: 0;
  color: var(--user-text-dim);
}

.step-item.active,
.step-item.done {
  opacity: 1;
  color: var(--user-text);
}

.step-num {
  width: 22px;
  height: 22px;
  border-radius: 50%;
  background: var(--user-surface-light, var(--user-bg-base));
  border: 1px solid var(--user-border);
  color: var(--user-text-dim);
  font-size: 11px;
  font-weight: 700;
  display: grid;
  place-items: center;
  flex-shrink: 0;
}

.step-item.active .step-num {
  background: var(--user-gradient, var(--user-primary));
  color: var(--user-btn-text, var(--user-bg-base));
  border-color: transparent;
}

.step-item.done .step-num {
  background: #39db9a;
  color: #061425;
  border-color: transparent;
}

.step-label {
  font-size: 11.5px;
  font-weight: 600;
  color: var(--user-text-dim);
  white-space: nowrap;
}

.step-item.active .step-label,
.step-item.done .step-label {
  color: var(--user-text);
}

.step-divider {
  flex: 1;
  min-width: 6px;
  height: 2px;
  background: var(--user-border);
  margin: 0 4px;
}

/* Step Content */
.auth-method-switch {
  display: inline-flex;
  align-self: center;
  align-items: center;
  justify-content: center;
  gap: 4px;
  width: auto;
  margin: 0 auto;
  padding: 0;
  background: transparent;
  border: 0;
  border-radius: 0;
}
.auth-method-tab {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  min-height: 30px;
  padding: 5px 11px;
  border: 1px solid transparent;
  border-radius: 8px;
  background: transparent;
  color: var(--user-text-dim);
  font-size: 12px;
  font-weight: 600;
  white-space: nowrap;
  cursor: pointer;
  transition:
    background 0.18s ease,
    color 0.18s ease;
}
.auth-method-tab:hover:not(:disabled) {
  color: var(--user-text);
  background: var(--user-surface-light);
}
.auth-method-tab.active {
  color: #fff;
  background: rgba(56, 167, 255, 0.16);
  border-color: rgba(56, 167, 255, 0.28);
  box-shadow: none;
}
.auth-method-tab:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
.qr-code-frame {
  display: flex;
  align-items: center;
  justify-content: center;
  align-self: center;
  width: min(260px, 100%);
  height: auto;
  aspect-ratio: 1 / 1;
  padding: 4px;
  background: #fff;
  border-radius: 8px;
  box-sizing: border-box;
  flex: 0 0 auto;
  overflow: hidden;
}
.qr-code-frame img {
  display: block;
  width: 100% !important;
  height: 100% !important;
  max-width: none !important;
  max-height: none !important;
  object-fit: fill;
  image-rendering: pixelated;
}
.qr-login-panel {
  display: flex;
  flex-direction: column;
  align-items: stretch;
  gap: 10px;
  text-align: left;
  color: var(--user-text-dim);
  font-size: 12px;
}
.qr-login-panel > p {
  margin: 0;
}
.qr-login-panel .qr-status {
  text-align: center;
}
.qr-login-panel img {
  max-width: 100%;
  image-rendering: pixelated;
}
.step-content {
  display: flex;
  flex-direction: column;
  gap: 18px;
}

/* Cada paso contiene un wrapper interno; el gap de .step-content no se
   aplicaba a sus campos y terminaba pegando el banner, inputs y botones. */
.step-content > div {
  display: flex;
  flex-direction: column;
  gap: 12px;
  min-width: 0;
}

.step-intro {
  display: flex;
  gap: 14px;
  align-items: flex-start;
}

.icon-accent {
  color: #42aefa;
}
.icon-amber {
  color: #ffc764;
}

.step-intro h3 {
  font-family: 'Space Grotesk', sans-serif;
  font-size: 16px;
  color: #f1f7ff;
  margin: 0 0 4px;
}

.step-intro p {
  font-size: 12px;
  color: #839bb3;
  margin: 0;
  line-height: 1.4;
}

.info-banner {
  display: flex;
  gap: 10px;
  align-items: center;
  background: var(--user-surface-light);
  border: 1px solid var(--user-border);
  padding: 10px 14px;
  border-radius: 10px;
  font-size: 12px;
  color: var(--user-text-dim);
}

.info-banner a {
  color: var(--user-accent);
  text-decoration: underline;
}

.form-group {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.form-group label {
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.05em;
  color: #7f99b3;
  text-transform: uppercase;
}

.form-group input {
  background: var(--user-bg-base);
  border: 1px solid var(--user-border);
  border-radius: 10px;
  padding: 12px 14px;
  color: #dbe7f5;
  font-size: 14px;
  outline: none;
  transition:
    border-color 0.2s,
    box-shadow 0.2s;
}

.form-group input:focus {
  border-color: var(--user-primary);
  box-shadow: 0 0 0 3px var(--user-glow);
}

.code-input {
  letter-spacing: 6px;
  font-size: 20px !important;
  font-weight: 700;
  text-align: center;
}

.help-text {
  font-size: 11px;
  color: #607991;
  margin-top: 2px;
}

/* Alert
   Vive dentro de .step-content, justo debajo del encabezado del paso, para que
   el gap del contenedor le de aire por arriba y por abajo. Cuando colgaba de
   .auth-card quedaba pegada al titulo del paso. */
.auth-alert {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 11px 14px;
  border-radius: 10px;
  font-size: 13px;
  line-height: 1.45;
}

.auth-alert .alert-icon {
  flex: 0 0 auto;
  margin-top: 1px;
}

.auth-alert.danger {
  background: #3d171d;
  border: 1px solid #732a36;
  color: #ff9e9e;
}

/* Buttons */
.auth-button {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 13px 20px;
  border-radius: 10px;
  font-size: 14px;
  font-weight: 700;
  cursor: pointer;
  border: 0;
  transition:
    transform 0.2s,
    background 0.2s,
    box-shadow 0.2s;
}

.auth-button.primary {
  background: var(--user-gradient);
  color: var(--user-bg-base);
}

.auth-button.primary:hover:not(:disabled) {
  /* El brillo y el halo salen del tema activo: antes estaban fijados en azul y
     el botón se iluminaba igual en todos los temas. */
  filter: brightness(1.1);
  transform: translateY(-2px);
  box-shadow: 0 8px 20px
    color-mix(in srgb, var(--user-primary) 35%, transparent);
}

.auth-button.secondary {
  background: var(--user-surface-light);
  color: var(--user-text-dim);
  border: 1px solid var(--user-border);
}

.auth-button.secondary:hover:not(:disabled) {
  background: #1b3854;
  color: #dbe7f5;
}

.auth-button:disabled {
  opacity: 0.55;
  cursor: not-allowed;
}

.button-group {
  display: flex;
  gap: 10px;
}

.button-group .auth-button {
  flex: 1;
}

/* El boton de volver no compite con la accion principal. */
.button-group .btn-back {
  flex: 0 0 auto;
  padding: 13px 16px;
}

.back-note {
  margin-top: -8px;
  text-align: center;
}

.text-center {
  text-align: center;
}

.success-icon {
  color: #39db9a;
  display: grid;
  place-items: center;
  margin-bottom: 8px;
}

.full-width {
  width: 100%;
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

@keyframes modalRise {
  from {
    opacity: 0;
    transform: translateY(16px) scale(0.97);
  }
  to {
    opacity: 1;
    transform: translateY(0) scale(1);
  }
}

/* Modal de temas del panel. Se teletransporta a <body>, por eso no hereda el
   recorte de la tarjeta y usa position: fixed sobre toda la ventana. */
.theme-modal-overlay {
  position: fixed;
  inset: 0;
  z-index: 1100;
  display: grid;
  place-items: center;
  padding: 20px;
  background: rgba(0, 0, 0, 0.6);
  backdrop-filter: blur(6px);
}

.theme-modal {
  width: min(560px, 94vw);
  max-height: calc(100vh - 40px);
  max-height: calc(100dvh - 40px);
  overflow-y: auto;
  padding: 22px;
  box-sizing: border-box;
  border: 1px solid var(--user-border);
  border-radius: 16px;
  background: var(--user-surface);
  box-shadow: 0 25px 60px rgba(0, 0, 0, 0.6);
}

.theme-modal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.theme-modal-header h3 {
  margin: 0;
  font-family: 'Space Grotesk', sans-serif;
  font-size: 16px;
  color: #f1f7ff;
}

.theme-modal-close {
  display: grid;
  place-items: center;
  width: 30px;
  height: 30px;
  padding: 0;
  flex-shrink: 0;
  border: 1px solid var(--user-border);
  border-radius: 8px;
  background: var(--user-surface-light);
  color: var(--user-text-dim);
  cursor: pointer;
}

.theme-modal-close:hover {
  color: #fff;
  border-color: var(--user-primary);
}

.theme-modal-hint {
  margin: 6px 0 16px;
  font-size: 12px;
  line-height: 1.45;
  color: var(--user-text-dim);
}

/* El título de grupo global trae márgenes pensados para el gap de Ajustes;
   dentro del modal se espacian a mano. */
.theme-modal .color-group-title {
  margin: 16px 0 10px;
}

.theme-modal .color-group-title:first-of-type {
  margin-top: 0;
}

/* Pantallas bajas (p. ej. 1366x768): se compacta la tarjeta para que quepa
   entera; si aun asi no cabe, el max-height de .auth-card permite desplazarla. */
@media (max-height: 800px) {
  .auth-overlay {
    padding: 12px;
  }

  .auth-card {
    padding: 20px 24px;
    max-height: calc(100dvh - 24px);
  }

  .auth-header {
    margin-bottom: 16px;
  }

  .auth-theme-fab {
    top: 12px;
    right: 12px;
    width: 36px;
    height: 36px;
  }

  .auth-logo {
    width: 38px;
    height: 38px;
    border-radius: 10px;
  }

  .auth-header h2 {
    font-size: 18px;
  }

  .stepper {
    margin-bottom: 16px;
    padding: 6px 0;
  }

  .step-content {
    gap: 12px;
  }

  .step-content > div {
    gap: 10px;
  }

  .qr-code-frame {
    width: min(210px, 100%);
  }
}

/* En ventanas estrechas, los controles del encabezado pueden bajar de línea
   y el stepper conserva sus cuatro etapas sin forzar el ancho de la tarjeta. */
@media (max-width: 720px) {
  .auth-header {
    flex-wrap: wrap;
    row-gap: 10px;
  }

  .auth-title-box {
    flex: 1 1 calc(100% - 60px);
  }

  .header-controls {
    width: 100%;
    justify-content: flex-end;
  }

  .stepper {
    gap: 3px;
  }

  .step-item {
    gap: 4px;
  }

  .step-label {
    font-size: 10px;
  }

  .step-divider {
    min-width: 3px;
    margin: 0 1px;
  }
}

/* Va después del bloque de altura para ganar en pantallas pequeñas (móvil en
   vertical, que también puede ser bajito): hueco arriba para el botón flotante
   y una tarjeta que no se salga por abajo. */
@media (max-width: 600px) {
  .auth-overlay {
    padding-top: 64px;
  }

  .auth-card {
    max-height: calc(100dvh - 84px);
  }
}
</style>
