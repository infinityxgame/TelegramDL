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
  Radio,
  Check
} from '../icons'
import ProxyModal from './ProxyModal.vue'
import { useAuthToken, isWailsRuntime } from '../composables/useAuthToken'
import { openExternal } from '../composables/useExternalLink'
import { useI18n } from '../i18n'
import { isLoopbackHost } from '../utils/loopback.js'
import { useQRLogin } from '../composables/useQRLogin'

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
const authMethod = ref('phone')

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

const {
  qrImage,
  qrStatus,
  qrError,
  startQRLogin,
  cancelQRLogin
} = useQRLogin({
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
    return
  }
  authMethod.value = 'qr'
  if (qrStatus.value !== 'WAITING_SCAN' && qrStatus.value !== 'STARTING') {
    await startQRLogin()
  }
}

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
  emit('auth-success', data)
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
  await apiCall('/api/auth/send-code', { phone_number: cleanPhone })
  emit('auth-success', {
    ...props.authStatus,
    state: 'WAITING_CODE',
    phone_number: cleanPhone
  })
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
    <div class="auth-card">
      <!-- Header -->
      <div class="auth-header">
        <div class="auth-logo">
          <svg
            width="24"
            height="24"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="2"
          >
            <path d="m22 2-7 20-4-9-9-4Z" />
            <path d="M22 2 11 13" />
          </svg>
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

      <!-- Step 1: Credenciales API -->
      <div v-if="currentStep === 1" class="step-content">
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

      <!-- Step 2: Método de autenticación -->
      <div v-else-if="currentStep === 2" class="step-content">
        <div class="auth-method-switch" role="tablist" :aria-label="t('auth.methodLabel')">
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
            <Radio :size="16" />
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
            <button class="auth-button primary" :disabled="loading" @click="sendCode">
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
            <Radio :size="28" class="icon-accent" />
            <div>
              <h3>{{ t('auth.qrTitle') }}</h3>
              <p>{{ t('auth.qrInstructions') }}</p>
            </div>
          </div>
          <div v-if="qrImage" class="qr-code-frame">
            <img :src="qrImage" :alt="t('auth.qrAlt')" />
          </div>
          <p v-if="qrStatus === 'WAITING_SCAN'">{{ t('auth.qrWaiting') }}</p>
          <p v-else-if="qrStatus === 'STARTING'" class="help-text">{{ t('auth.qrPreparing') }}</p>
          <p v-else-if="!qrError" class="help-text">{{ t('auth.qrPreparing') }}</p>
          <p v-if="qrError" class="auth-alert danger" role="alert">{{ qrError }}</p>
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
            class="auth-button primary qr-retry-btn"
            :disabled="loading"
            @click="startQRLogin"
          >
            <Loader2 v-if="loading" class="spin" :size="18" />
            <span v-else>{{ t('auth.qrRetry') }}</span>
          </button>
          <button
            class="auth-button secondary qr-back-btn"
            :disabled="loading"
            @click="goBack"
          >
            <ArrowLeft :size="16" />
            <span>{{ t('common.back') }}</span>
          </button>
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
  width: min(520px, 92vw);
  background: var(--user-surface);
  border: 1px solid var(--user-border);
  border-radius: 20px;
  padding: 30px;
  box-shadow: 0 25px 60px rgba(0, 0, 0, 0.6);
  animation: modalRise 0.35s cubic-bezier(0.16, 1, 0.3, 1);
  box-sizing: border-box;
}

.auth-header {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 24px;
}

.header-controls {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
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
  border-radius: 12px;
  background: linear-gradient(135deg, #38a7ff, #0077ff);
  color: #fff;
  display: grid;
  place-items: center;
  box-shadow: 0 0 15px rgba(56, 167, 255, 0.4);
}

.auth-header h2 {
  font-family: 'Space Grotesk', sans-serif;
  font-size: 20px;
  font-weight: 700;
  color: #f1f7ff;
  margin: 0;
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
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px;
  padding: 5px;
  background: var(--user-bg-base);
  border: 1px solid var(--user-border);
  border-radius: 12px;
}
.auth-method-tab {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  min-height: 40px;
  padding: 8px 12px;
  border: 1px solid transparent;
  border-radius: 9px;
  background: transparent;
  color: var(--user-text-dim);
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
  transition: all 0.2s ease;
}
.auth-method-tab:hover:not(:disabled) {
  color: var(--user-text);
  background: var(--user-surface-light);
}
.auth-method-tab.active {
  color: #fff;
  background: var(--user-primary, #278bd5);
  border-color: var(--user-primary, #278bd5);
  box-shadow: 0 3px 10px rgba(39, 139, 213, 0.22);
}
.auth-method-tab:disabled { opacity: 0.6; cursor: not-allowed; }
.qr-code-frame {
  display: grid;
  place-items: center;
  width: 100%;
  padding: 14px;
  background: #fff;
  border-radius: 14px;
  box-sizing: border-box;
}
.qr-code-frame img {
  display: block;
  width: min(300px, 100%);
  height: auto;
  aspect-ratio: 1;
  object-fit: contain;
  image-rendering: pixelated;
}
.qr-login-panel .qr-back-btn { align-self: stretch; }
.qr-retry-btn { align-self: stretch; }

.qr-login-option {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding-top: 14px;
  border-top: 1px solid var(--user-border);
}
.qr-login-panel {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  text-align: center;
  color: var(--user-text-dim);
  font-size: 12px;
}
.qr-login-panel img {
  width: 220px;
  height: 220px;
  max-width: 100%;
  background: white;
  padding: 8px;
  border-radius: 12px;
}
.step-content {
  display: flex;
  flex-direction: column;
  gap: 18px;
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
  background: #50b3ff;
  transform: translateY(-2px);
  box-shadow: 0 8px 20px rgba(56, 167, 255, 0.35);
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
</style>
