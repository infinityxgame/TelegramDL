import { reactive } from 'vue'

// Modal de confirmacion generico, reutilizado por todas las acciones
// destructivas o que requieren aviso previo (borrar descargas, limpiar
// historial, instalar actualizaciones, avisos de espacio en disco, etc.).
// Extraido de App.vue: no depende de nada mas del componente.
export function useConfirmModal() {
  const modal = reactive({
    show: false,
    title: '',
    message: '',
    confirmText: '',
    cancelText: 'Cancelar',
    type: 'primary',
    action: null,
    cancelAction: null
  })

  const openConfirm = (config) => {
    modal.title = config.title
    modal.message = config.message
    modal.confirmText = config.confirmText
    modal.cancelText = config.cancelText !== undefined ? config.cancelText : 'Cancelar'
    modal.type = config.type || 'primary'
    modal.action = config.action
    modal.cancelAction = config.cancelAction
    modal.show = true
  }

  const handleConfirm = () => {
    if (modal.action) modal.action()
    modal.show = false
  }

  const handleCancel = () => {
    if (modal.cancelAction) modal.cancelAction()
    modal.show = false
  }

  return { modal, openConfirm, handleConfirm, handleCancel }
}
