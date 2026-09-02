<script setup>
import { X, AlertCircle, HelpCircle } from 'lucide-vue-next'

const props = defineProps({
  show: Boolean,
  title: String,
  message: String,
  confirmText: { type: String, default: 'Confirmar' },
  cancelText: { type: String, default: 'Cancelar' },
  type: { type: String, default: 'primary' } // 'primary', 'danger'
})

const emit = defineEmits(['confirm', 'cancel'])
</script>

<template>
  <Teleport to="body">
    <Transition name="fade">
      <div v-if="show" class="modal-overlay" @click.self="emit('cancel')">
        <div class="modal-content">
          <div class="modal-header">
            <div class="header-icon" :class="type">
              <AlertCircle v-if="type === 'danger'" :size="20" />
              <HelpCircle v-else :size="20" />
            </div>
            <h3>{{ title }}</h3>
            <button class="close-btn" @click="emit('cancel')"><X :size="18" /></button>
          </div>
          <div class="modal-body">
            <p>{{ message }}</p>
          </div>
          <div class="modal-footer">
            <button class="cancel-btn" @click="emit('cancel')">{{ cancelText }}</button>
            <button class="confirm-btn" :class="type" @click="emit('confirm')">{{ confirmText }}</button>
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

.modal-content {
  background: var(--user-surface);
  border: 1px solid var(--user-border-light);
  border-radius: 18px;
  width: min(420px, 90%);
  box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.5), 0 0 35px var(--user-glow);
  overflow: hidden;
}

.modal-header {
  padding: 20px 24px;
  display: flex;
  align-items: center;
  gap: 12px;
  border-bottom: 1px solid var(--user-border);
  position: relative;
}

.header-icon {
  width: 36px;
  height: 36px;
  border-radius: 10px;
  display: grid;
  place-items: center;
}

.header-icon.primary { background: var(--user-icon-bg); color: var(--user-accent); }
.header-icon.danger { background: #48252c; color: #ffadad; }

.modal-header h3 {
  margin: 0;
  font-size: 16px;
  font-family: 'Space Grotesk', sans-serif;
  color: #f1f7ff;
}

.close-btn {
  margin-left: auto;
  background: transparent;
  border: 0;
  color: var(--user-text-dim);
  cursor: pointer;
  padding: 4px;
}

.close-btn:hover {
  color: var(--user-accent);
}

.modal-body {
  padding: 24px;
  color: var(--user-text-dim);
  font-size: 14px;
  line-height: 1.6;
}

.modal-footer {
  padding: 16px 24px;
  background: var(--user-bg-base);
  border-top: 1px solid var(--user-border);
  display: flex;
  justify-content: flex-end;
  gap: 12px;
}

button {
  padding: 10px 18px;
  border-radius: 10px;
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
  transition: all 0.2s;
}

.cancel-btn {
  background: var(--user-surface-light);
  border: 1px solid var(--user-border-light);
  color: var(--user-text-dim);
}

.cancel-btn:hover { background: var(--user-border); color: #fff; }

.confirm-btn {
  border: 0;
}

.confirm-btn.primary { background: var(--user-gradient); color: var(--user-bg-base); }
.confirm-btn.primary:hover { opacity: 0.9; box-shadow: 0 4px 15px var(--user-glow); }

.confirm-btn.danger { background: #e88888; color: #041322; }
.confirm-btn.danger:hover { background: #ffadad; box-shadow: 0 4px 15px rgba(232, 136, 136, 0.3); }

.fade-enter-active, .fade-leave-active { transition: opacity 0.25s ease; }
.fade-enter-from, .fade-leave-to { opacity: 0; }
</style>
