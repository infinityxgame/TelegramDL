<script setup>
import { ref } from 'vue'
import { KeyRound, ArrowRight, Loader2, AlertCircle, Eye, EyeOff, ShieldCheck } from 'lucide-vue-next'

const props = defineProps({
  error: { type: String, default: '' }
})
const emit = defineEmits(['submit'])

const tokenInput = ref('')
const showToken = ref(false)
const submitting = ref(false)

const handleSubmit = async () => {
  const value = tokenInput.value.trim()
  if (!value) return
  submitting.value = true
  try {
    await emit('submit', value)
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="auth-overlay">
    <div class="auth-card">
      <div class="auth-header">
        <div class="auth-logo">
          <ShieldCheck :size="22" />
        </div>
        <div>
          <h2>Acceso remoto</h2>
          <p>Este dispositivo necesita el token de acceso de tu TelegramDL</p>
        </div>
      </div>

      <div v-if="error" class="auth-alert danger">
        <AlertCircle :size="18" />
        <span>{{ error }}</span>
      </div>

      <div class="info-banner">
        <ShieldCheck :size="16" />
        <span>
          Encuéntralo en la app de escritorio: <b>Ajustes → Acceso remoto</b>.
          Cópialo y pégalo aquí una sola vez; este navegador lo recordará.
        </span>
      </div>

      <div class="form-group">
        <label>Token de acceso</label>
        <div class="token-input-row">
          <input
            v-model="tokenInput"
            :type="showToken ? 'text' : 'password'"
            placeholder="Pega aquí el token"
            autocomplete="off"
            spellcheck="false"
            @keyup.enter="handleSubmit"
          />
          <button type="button" class="ghost-icon-btn" @click="showToken = !showToken" :aria-label="showToken ? 'Ocultar token' : 'Mostrar token'">
            <EyeOff v-if="showToken" :size="16" />
            <Eye v-else :size="16" />
          </button>
        </div>
      </div>

      <button class="auth-button primary" :disabled="submitting || !tokenInput.trim()" @click="handleSubmit">
        <Loader2 v-if="submitting" class="spin" :size="18" />
        <template v-else>
          <span>Entrar</span>
          <KeyRound :size="18" />
        </template>
      </button>
    </div>
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
  width: min(440px, 100%);
  background: var(--user-surface);
  border: 1px solid var(--user-border);
  border-radius: 20px;
  padding: 30px;
  box-shadow: 0 25px 60px rgba(0, 0, 0, 0.6);
}

.auth-header {
  display: flex;
  align-items: center;
  gap: 14px;
  margin-bottom: 20px;
}

.auth-logo {
  width: 44px;
  height: 44px;
  border-radius: 12px;
  background: linear-gradient(135deg, var(--user-primary), var(--user-accent));
  color: #fff;
  display: grid;
  place-items: center;
  flex-shrink: 0;
}

.auth-header h2 {
  font-family: 'Space Grotesk', sans-serif;
  font-size: 19px;
  font-weight: 700;
  color: #f1f7ff;
  margin: 0;
}

.auth-header p {
  font-size: 13px;
  color: #7d96b0;
  margin: 2px 0 0;
}

.auth-alert.danger {
  display: flex;
  align-items: center;
  gap: 8px;
  background: rgba(220, 60, 60, 0.12);
  border: 1px solid rgba(220, 60, 60, 0.35);
  color: #ff8f8f;
  border-radius: 10px;
  padding: 10px 12px;
  font-size: 13px;
  margin-bottom: 16px;
}

.info-banner {
  display: flex;
  gap: 10px;
  align-items: flex-start;
  background: var(--user-surface-light);
  border: 1px solid var(--user-border);
  border-radius: 12px;
  padding: 12px 14px;
  font-size: 12.5px;
  color: var(--user-text-dim);
  margin-bottom: 18px;
  line-height: 1.5;
}

.form-group {
  margin-bottom: 20px;
}

.form-group label {
  display: block;
  font-size: 12px;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  color: var(--user-text-dim);
  margin-bottom: 8px;
}

.token-input-row {
  display: flex;
  align-items: center;
  gap: 6px;
  background: var(--user-bg-base);
  border: 1px solid var(--user-border);
  border-radius: 10px;
  padding: 0 6px 0 12px;
}

.token-input-row input {
  flex: 1;
  background: transparent;
  border: none;
  outline: none;
  color: #f1f7ff;
  font-size: 14px;
  padding: 11px 0;
  font-family: 'JetBrains Mono', monospace;
}

.ghost-icon-btn {
  background: transparent;
  border: none;
  color: var(--user-text-dim);
  cursor: pointer;
  padding: 8px;
  border-radius: 8px;
  display: grid;
  place-items: center;
}

.ghost-icon-btn:hover {
  background: var(--user-border);
  color: #fff;
}

.auth-button.primary {
  width: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  background: var(--user-gradient, var(--user-primary));
  color: #fff;
  border: none;
  border-radius: 12px;
  padding: 13px;
  font-size: 14px;
  font-weight: 700;
  cursor: pointer;
  transition: opacity 0.2s, transform 0.2s;
}

.auth-button.primary:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.auth-button.primary:not(:disabled):hover {
  transform: translateY(-1px);
}

.spin {
  animation: spin 0.8s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}
</style>
