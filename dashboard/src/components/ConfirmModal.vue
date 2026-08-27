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
  background: rgba(2, 10, 19, 0.85);
  backdrop-filter: blur(4px);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}

.modal-content {
  background: #0b1a2a;
  border: 1px solid #1b344b;
  border-radius: 18px;
  width: min(400px, 90%);
  box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.5);
  overflow: hidden;
}

.modal-header {
  padding: 20px 24px;
  display: flex;
  align-items: center;
  gap: 12px;
  border-bottom: 1px solid #193149;
  position: relative;
}

.header-icon {
  width: 36px;
  height: 36px;
  border-radius: 10px;
  display: grid;
  place-items: center;
}

.header-icon.primary { background: #11385b; color: #59baff; }
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
  color: #6f8ba5;
  cursor: pointer;
  padding: 4px;
}

.modal-body {
  padding: 24px;
  color: #8da5bb;
  font-size: 14px;
  line-height: 1.6;
}

.modal-footer {
  padding: 16px 24px;
  background: #0e2032;
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
  background: #1b344b;
  border: 1px solid #294963;
  color: #dbe7f5;
}

.cancel-btn:hover { background: #234765; }

.confirm-btn {
  border: 0;
}

.confirm-btn.primary { background: #42aefa; color: #041322; }
.confirm-btn.primary:hover { background: #62c7ff; box-shadow: 0 4px 15px rgba(66, 174, 250, 0.3); }

.confirm-btn.danger { background: #e88888; color: #041322; }
.confirm-btn.danger:hover { background: #ffadad; box-shadow: 0 4px 15px rgba(232, 136, 136, 0.3); }

.fade-enter-active, .fade-leave-active { transition: opacity 0.25s ease; }
.fade-enter-from, .fade-leave-to { opacity: 0; }
</style>
