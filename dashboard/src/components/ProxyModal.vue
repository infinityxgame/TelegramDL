<script setup>
import { ref, watch } from 'vue'
import { X, Radio, CheckCircle2, AlertCircle, Loader2, Save } from '../icons'
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

const form = ref({
  proxy_enabled: false,
  proxy_type: 'socks5',
  proxy_host: '',
  proxy_port: 1080,
  proxy_username: '',
  proxy_password: '',
  proxy_secret: ''
})

const testing = ref(false)
const testResult = ref(null)
const saving = ref(false)

watch(
  () => props.show,
  (val) => {
    if (val && props.settings) {
      form.value = {
        proxy_enabled: !!props.settings.proxy_enabled,
        proxy_type: props.settings.proxy_type || 'socks5',
        proxy_host: props.settings.proxy_host || '',
        proxy_port: props.settings.proxy_port || 1080,
        proxy_username: props.settings.proxy_username || '',
        proxy_password: props.settings.proxy_password || '',
        proxy_secret: props.settings.proxy_secret || ''
      }
      testResult.value = null
    }
  },
  { immediate: true }
)

const handleTest = async () => {
  if (!form.value.proxy_host || !form.value.proxy_port) return
  testing.value = true
  testResult.value = null
  try {
    const res = await fetch('/api/proxy/test', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...authHeaders()
      },
      body: JSON.stringify({
        proxy_type: form.value.proxy_type,
        proxy_host: form.value.proxy_host,
        proxy_port: Number(form.value.proxy_port),
        proxy_username: form.value.proxy_username,
        proxy_password: form.value.proxy_password,
        proxy_secret: form.value.proxy_secret
      })
    })
    const data = await res.json()
    if (data.status === 'ok') {
      testResult.value = {
        active: data.active,
        ping_ms: data.ping_ms,
        error: data.error
      }
    } else {
      testResult.value = {
        active: false,
        ping_ms: 0,
        error: data.error || t('common.serverError')
      }
    }
  } catch (err) {
    testResult.value = {
      active: false,
      ping_ms: 0,
      error: err.message || t('common.serverError')
    }
  } finally {
    testing.value = false
  }
}

const handleSave = async () => {
  saving.value = true
  try {
    const payload = {
      proxy_enabled: form.value.proxy_enabled,
      proxy_type: form.value.proxy_type,
      proxy_host: form.value.proxy_host,
      proxy_port: Number(form.value.proxy_port),
      proxy_username: form.value.proxy_username,
      proxy_password: form.value.proxy_password,
      proxy_secret: form.value.proxy_secret
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
              <div class="proxy-type-row">
                <span class="field-label-inline">{{
                  t('settings.proxyType')
                }}</span>
                <div class="radio-group">
                  <label
                    class="radio-option"
                    :class="{ active: form.proxy_type === 'socks5' }"
                    @click="form.proxy_type = 'socks5'"
                  >
                    <span class="radio-circle">
                      <span class="radio-dot"></span>
                    </span>
                    <span class="radio-label">SOCKS5</span>
                  </label>

                  <label
                    class="radio-option"
                    :class="{ active: form.proxy_type === 'mtproto' }"
                    @click="form.proxy_type = 'mtproto'"
                  >
                    <span class="radio-circle">
                      <span class="radio-dot"></span>
                    </span>
                    <span class="radio-label">MTProto</span>
                  </label>

                  <label
                    class="radio-option"
                    :class="{ active: form.proxy_type === 'http' }"
                    @click="form.proxy_type = 'http'"
                  >
                    <span class="radio-circle">
                      <span class="radio-dot"></span>
                    </span>
                    <span class="radio-label">HTTP</span>
                  </label>
                </div>
              </div>

              <div class="proxy-inputs-single-line">
                <div class="field-col host-col">
                  <label class="field-label">{{
                    t('settings.proxyHost')
                  }}</label>
                  <input
                    v-model="form.proxy_host"
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
                    v-model.number="form.proxy_port"
                    type="number"
                    class="proxy-input"
                    placeholder="1080"
                  />
                </div>
                <template
                  v-if="
                    form.proxy_type === 'socks5' || form.proxy_type === 'http'
                  "
                >
                  <div class="field-col user-col">
                    <label class="field-label">{{
                      t('settings.proxyUsername')
                    }}</label>
                    <input
                      v-model="form.proxy_username"
                      type="text"
                      class="proxy-input"
                    />
                  </div>
                  <div class="field-col pass-col">
                    <label class="field-label">{{
                      t('settings.proxyPassword')
                    }}</label>
                    <input
                      v-model="form.proxy_password"
                      type="password"
                      class="proxy-input"
                    />
                  </div>
                </template>
                <template v-else-if="form.proxy_type === 'mtproto'">
                  <div class="field-col secret-col">
                    <label class="field-label">{{
                      t('settings.proxySecret')
                    }}</label>
                    <input
                      v-model="form.proxy_secret"
                      type="text"
                      class="proxy-input"
                      placeholder="ee0123456789..."
                    />
                  </div>
                </template>
              </div>

              <div class="test-section">
                <button
                  type="button"
                  class="test-btn"
                  :disabled="testing || !form.proxy_host || !form.proxy_port"
                  @click="handleTest"
                >
                  <Loader2 v-if="testing" class="spin" :size="16" />
                  <Radio v-else :size="16" />
                  <span>{{
                    testing
                      ? t('settings.proxyTesting')
                      : t('settings.proxyTest')
                  }}</span>
                </button>

                <div
                  v-if="testResult"
                  class="test-result"
                  :class="{
                    active: testResult.active,
                    inactive: !testResult.active
                  }"
                >
                  <CheckCircle2 v-if="testResult.active" :size="16" />
                  <AlertCircle v-else :size="16" />
                  <span v-if="testResult.active">
                    {{
                      t('settings.proxyActive', { ping: testResult.ping_ms })
                    }}
                  </span>
                  <span v-else>
                    {{ t('settings.proxyInactive') }}: {{ testResult.error }}
                  </span>
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
</template>

<style scoped>
.modal-overlay {
  position: fixed;
  top: 0;
  left: 0;
  width: 100%;
  height: 100%;
  background: rgba(2, 8, 19, 0.75);
  backdrop-filter: blur(8px);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 10000;
}

.modal-content.proxy-modal {
  background: var(--user-surface, #111827);
  border: 1px solid var(--user-border-light, rgba(255, 255, 255, 0.12));
  border-radius: 18px;
  width: min(580px, 94%);
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
}

.setting-line.clean-line {
  border-top: none !important;
  border-bottom: none !important;
  padding-top: 0 !important;
  padding-bottom: 0 !important;
  margin-top: 0 !important;
  margin-bottom: 0 !important;
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
  min-width: 140px;
}

.field-col.port-col {
  width: 90px;
  flex: 0 0 90px;
}

.field-col.user-col {
  width: 145px;
  flex: 0 0 145px;
}

.field-col.pass-col {
  width: 145px;
  flex: 0 0 145px;
}

.field-col.secret-col {
  flex: 1;
  min-width: 180px;
}

.field-label {
  font-size: 0.88rem;
  font-weight: 600;
  color: var(--user-text, #f3f4f6);
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
  color: var(--user-text, #f9fafb);
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

.test-section {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-top: 6px;
  padding-top: 0;
  border-top: none !important;
}

.test-btn {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 8px 14px;
  border-radius: 8px;
  border: 1px solid var(--user-border-light, rgba(255, 255, 255, 0.15));
  background: rgba(255, 255, 255, 0.05);
  color: var(--user-text, #e5e7eb);
  font-size: 0.85rem;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.2s;
}

.test-btn:hover:not(:disabled) {
  background: rgba(255, 255, 255, 0.1);
  border-color: var(--user-accent, #3b82f6);
}

.test-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
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
  border: 1px solid var(--user-border-light, rgba(255, 255, 255, 0.12));
  background: transparent;
  color: var(--user-text-muted, #9ca3af);
  font-size: 0.9rem;
  font-weight: 500;
  cursor: pointer;
}

.cancel-btn:hover {
  background: rgba(255, 255, 255, 0.05);
  color: var(--user-text, #fff);
}

.save-btn {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 9px 18px;
  border-radius: 10px;
  border: none;
  background: var(--user-accent, #3b82f6);
  color: #ffffff;
  font-size: 0.9rem;
  font-weight: 600;
  cursor: pointer;
  box-shadow: 0 4px 12px rgba(59, 130, 246, 0.3);
  transition: all 0.2s;
}

.save-btn:hover {
  filter: brightness(1.1);
  box-shadow: 0 6px 16px rgba(59, 130, 246, 0.4);
}

.toggle-switch {
  position: relative;
  display: inline-block;
  width: 44px;
  height: 24px;
}

.toggle-switch input {
  opacity: 0;
  width: 0;
  height: 0;
}

.slider {
  position: absolute;
  cursor: pointer;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background-color: rgba(255, 255, 255, 0.2);
  transition: 0.2s;
  border-radius: 24px;
}

.slider:before {
  position: absolute;
  content: '';
  height: 18px;
  width: 18px;
  left: 3px;
  bottom: 3px;
  background-color: white;
  transition: 0.2s;
  border-radius: 50%;
}

input:checked + .slider {
  background-color: var(--user-accent, #3b82f6);
}

input:checked + .slider:before {
  transform: translateX(20px);
}
</style>
