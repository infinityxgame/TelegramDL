<script setup>
import { ref, computed } from 'vue'
import { KeyRound, Phone, ShieldCheck, CheckCircle2, AlertCircle, ArrowRight, Loader2, RefreshCw, Info } from 'lucide-vue-next'

const props = defineProps({
  authStatus: {
    type: Object,
    required: true
  }
})

const emit = defineEmits(['auth-success'])

const currentStep = computed(() => {
  if (!props.authStatus.has_credentials || props.authStatus.state === 'UNCONFIGURED') return 1
  if (props.authStatus.state === 'NOT_LOGGED_IN') return 2
  if (props.authStatus.state === 'WAITING_CODE') return 3
  if (props.authStatus.state === 'WAITING_2FA') return 4
  if (props.authStatus.state === 'LOGGED_IN') return 5
  return 1
})

const apiId = ref('')
const apiHash = ref('')
const phoneNumber = ref(props.authStatus.phone_number || '')
const code = ref('')
const password = ref('')

const loading = ref(false)
const errorMessage = ref('')

const apiCall = async (url, body) => {
  loading.value = true
  errorMessage.value = ''
  try {
    const res = await fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body)
    })
    const data = await res.json()
    if (!res.ok) {
      throw new Error(data.detail || data.error || 'Error en el proceso')
    }
    return data
  } catch (err) {
    errorMessage.value = err.message
    throw err
  } finally {
    loading.value = false
  }
}

const saveCredentials = async () => {
  if (!apiId.value || !apiHash.value) {
    errorMessage.value = 'Por favor completa tanto el API ID como el API HASH'
    return
  }
  const data = await apiCall('/api/auth/credentials', {
    api_id: apiId.value.trim(),
    api_hash: apiHash.value.trim()
  })
  emit('auth-success', data)
}

const sendCode = async () => {
  if (!phoneNumber.value) {
    errorMessage.value = 'Ingresa un número de teléfono válido con código de país (ej. +34600112233)'
    return
  }
  let cleanPhone = phoneNumber.value.trim().replace(/\s+/g, '')
  if (!cleanPhone.startsWith('+')) {
    cleanPhone = '+' + cleanPhone
  }
  phoneNumber.value = cleanPhone
  const data = await apiCall('/api/auth/send-code', { phone_number: cleanPhone })
  emit('auth-success', { ...props.authStatus, state: 'WAITING_CODE', phone_number: cleanPhone })
}

const verifyCode = async () => {
  if (!code.value) {
    errorMessage.value = 'Ingresa el código de 5 dígitos recibido en Telegram'
    return
  }
  const data = await apiCall('/api/auth/verify-code', {
    phone_number: phoneNumber.value,
    code: code.value.trim()
  })
  if (data.status === '2fa_required' || data.state === 'WAITING_2FA') {
    emit('auth-success', { ...props.authStatus, state: 'WAITING_2FA' })
  } else {
    emit('auth-success', { ...data, authenticated: true })
  }
}

const verify2FA = async () => {
  if (!password.value) {
    errorMessage.value = 'Ingresa tu contraseña de verificación en dos pasos'
    return
  }
  const data = await apiCall('/api/auth/verify-2fa', { password: password.value })
  emit('auth-success', { ...data, authenticated: true })
}

const resetFlow = async () => {
  try {
    await fetch('/api/auth/logout', { method: 'POST' })
  } catch (e) {}
  phoneNumber.value = ''
  code.value = ''
  password.value = ''
  emit('auth-success', { authenticated: false, state: props.authStatus.has_credentials ? 'NOT_LOGGED_IN' : 'UNCONFIGURED', has_credentials: props.authStatus.has_credentials })
}
</script>

<template>
  <div class="auth-overlay">
    <div class="auth-card">
      <!-- Header -->
      <div class="auth-header">
        <div class="auth-logo">
          <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
            <path d="m22 2-7 20-4-9-9-4Z"/>
            <path d="M22 2 11 13"/>
          </svg>
        </div>
        <div>
          <h2>Instalación y Configuración</h2>
          <p>Conecta tu cuenta de Telegram para comenzar</p>
        </div>
      </div>

      <!-- Stepper -->
      <div class="stepper">
        <div class="step-item" :class="{ active: currentStep === 1, done: currentStep > 1 }">
          <span class="step-num">1</span>
          <span class="step-label">API</span>
        </div>
        <div class="step-divider"></div>
        <div class="step-item" :class="{ active: currentStep === 2, done: currentStep > 2 }">
          <span class="step-num">2</span>
          <span class="step-label">Teléfono</span>
        </div>
        <div class="step-divider"></div>
        <div class="step-item" :class="{ active: currentStep === 3 || currentStep === 4, done: currentStep > 4 }">
          <span class="step-num">3</span>
          <span class="step-label">Código</span>
        </div>
        <div class="step-divider"></div>
        <div class="step-item" :class="{ active: currentStep === 5, done: currentStep === 5 }">
          <span class="step-num">4</span>
          <span class="step-label">Listo</span>
        </div>
      </div>

      <!-- Error alert -->
      <div v-if="errorMessage" class="auth-alert danger">
        <AlertCircle :size="18" />
        <span>{{ errorMessage }}</span>
      </div>

      <!-- Step 1: Credenciales API -->
      <div v-if="currentStep === 1" class="step-content">
        <div class="step-intro">
          <KeyRound :size="28" class="icon-accent" />
          <div>
            <h3>Credenciales de la App (API ID & Hash)</h3>
            <p>Necesitas tus credenciales oficiales de Telegram Developer.</p>
          </div>
        </div>

        <div class="info-banner">
          <Info :size="16" />
          <span>
            ¿No las tienes? Consíguelas gratis en 
            <a href="https://my.telegram.org" target="_blank" rel="noopener">my.telegram.org</a> 
            (sección <i>API development tools</i>).
          </span>
        </div>

        <div class="form-group">
          <label>TGDL_API_ID</label>
          <input 
            v-model="apiId" 
            type="text" 
            placeholder="Ejemplo: 12345678" 
            @keyup.enter="saveCredentials"
          />
        </div>

        <div class="form-group">
          <label>TGDL_API_HASH</label>
          <input 
            v-model="apiHash" 
            type="text" 
            placeholder="Ejemplo: 0123456789abcdef0123456789abcdef" 
            @keyup.enter="saveCredentials"
          />
        </div>

        <button class="auth-button primary" :disabled="loading" @click="saveCredentials">
          <Loader2 v-if="loading" class="spin" :size="18" />
          <template v-else>
            <span>Guardar y Continuar</span>
            <ArrowRight :size="18" />
          </template>
        </button>
      </div>

      <!-- Step 2: Teléfono -->
      <div v-else-if="currentStep === 2" class="step-content">
        <div class="step-intro">
          <Phone :size="28" class="icon-accent" />
          <div>
            <h3>Número de Teléfono</h3>
            <p>Ingresa tu número de teléfono registrado en Telegram.</p>
          </div>
        </div>

        <div class="form-group">
          <label>Número con código de país</label>
          <input 
            v-model="phoneNumber" 
            type="tel" 
            placeholder="+34 600 00 00 00 o +1 555 123 4567" 
            @keyup.enter="sendCode"
          />
          <small class="help-text">Asegúrate de incluir el prefijo internacional (ej. +34 para España, +52 para México, +1 para EE.UU.).</small>
        </div>

        <button class="auth-button primary" :disabled="loading" @click="sendCode">
          <Loader2 v-if="loading" class="spin" :size="18" />
          <template v-else>
            <span>Enviar Código</span>
            <ArrowRight :size="18" />
          </template>
        </button>
      </div>

      <!-- Step 3: Código de Verificación -->
      <div v-else-if="currentStep === 3" class="step-content">
        <div class="step-intro">
          <ShieldCheck :size="28" class="icon-accent" />
          <div>
            <h3>Código de Verificación</h3>
            <p>Telegram ha enviado un código a tu aplicación o por SMS a {{ phoneNumber }}.</p>
          </div>
        </div>

        <div class="form-group">
          <label>Código recibido</label>
          <input 
            v-model="code" 
            type="text" 
            placeholder="12345" 
            maxlength="6"
            class="code-input"
            @keyup.enter="verifyCode"
          />
        </div>

        <div class="button-group">
          <button class="auth-button secondary" :disabled="loading" @click="resetFlow">
            <RefreshCw :size="16" />
            <span>Cambiar número</span>
          </button>
          <button class="auth-button primary" :disabled="loading" @click="verifyCode">
            <Loader2 v-if="loading" class="spin" :size="18" />
            <template v-else>
              <span>Verificar</span>
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
            <h3>Verificación en 2 Pasos (2FA)</h3>
            <p>Tu cuenta tiene activada una contraseña adicional.</p>
          </div>
        </div>

        <div class="form-group">
          <label>Contraseña de 2 Pasos</label>
          <input 
            v-model="password" 
            type="password" 
            placeholder="Tu contraseña de Telegram" 
            @keyup.enter="verify2FA"
          />
        </div>

        <button class="auth-button primary" :disabled="loading" @click="verify2FA">
          <Loader2 v-if="loading" class="spin" :size="18" />
          <template v-else>
            <span>Iniciar Sesión</span>
            <ArrowRight :size="18" />
          </template>
        </button>
      </div>

      <!-- Step 5: Éxito -->
      <div v-else-if="currentStep === 5" class="step-content text-center">
        <div class="success-icon">
          <CheckCircle2 :size="48" />
        </div>
        <h3>¡Conexión Exitosa!</h3>
        <p v-if="authStatus.user">
          Conectado como <strong>{{ authStatus.user.first_name }}</strong> 
          <span v-if="authStatus.user.username">(@{{ authStatus.user.username }})</span>
        </p>
        <p v-else>Tu sesión de Telegram está lista y activa.</p>

        <button class="auth-button primary full-width" @click="$emit('auth-success', { ...authStatus, authenticated: true, state: 'LOGGED_IN' })">
          <span>Ir al Dashboard</span>
          <ArrowRight :size="18" />
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.auth-overlay {
  position: fixed;
  inset: 0;
  z-index: 100;
  background: color-mix(in srgb, var(--user-bg-base), transparent 8%);
  backdrop-filter: blur(12px);
  display: grid;
  place-items: center;
  padding: 20px;
}

.auth-card {
  width: min(480px, 100%);
  background: var(--user-surface);
  border: 1px solid var(--user-border);
  border-radius: 20px;
  padding: 30px;
  box-shadow: 0 25px 60px rgba(0, 0, 0, 0.6);
  animation: modalRise 0.35s cubic-bezier(0.16, 1, 0.3, 1);
}

.auth-header {
  display: flex;
  align-items: center;
  gap: 14px;
  margin-bottom: 24px;
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
  border-bottom: 1px solid #162f47;
}

.step-item {
  display: flex;
  align-items: center;
  gap: 6px;
  opacity: 0.45;
  transition: opacity 0.3s;
}

.step-item.active, .step-item.done {
  opacity: 1;
}

.step-num {
  width: 22px;
  height: 22px;
  border-radius: 50%;
  background: var(--user-bg-base);
  color: var(--user-text-dim);
  font-size: 11px;
  font-weight: 700;
  display: grid;
  place-items: center;
}

.step-item.active .step-num {
  background: var(--user-primary);
  color: var(--user-bg-base);
}

.step-item.done .step-num {
  background: #39db9a;
  color: #061425;
}

.step-label {
  font-size: 12px;
  font-weight: 600;
  color: #dbe7f5;
}

.step-divider {
  flex: 1;
  height: 2px;
  background: var(--user-border);
  margin: 0 8px;
}

/* Step Content */
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

.icon-accent { color: #42aefa; }
.icon-amber { color: #ffc764; }

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
  transition: border-color 0.2s, box-shadow 0.2s;
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

/* Alert */
.auth-alert {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 11px 14px;
  border-radius: 10px;
  font-size: 13px;
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
  transition: transform 0.2s, background 0.2s, box-shadow 0.2s;
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
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}

@keyframes modalRise {
  from { opacity: 0; transform: translateY(16px) scale(0.97); }
  to { opacity: 1; transform: translateY(0) scale(1); }
}
</style>
