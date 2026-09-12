import { ref } from 'vue'

// Chequeo, aviso e instalacion de actualizaciones de la app. Se aisla del
// resto de App.vue porque su ciclo (poll de progreso, dialogo de
// confirmacion, posposicion) es independiente del estado de las descargas
// o del websocket. Requiere que quien la use ya tenga listos api(),
// showMessage() y openConfirm() (del composable useConfirmModal).
export function useUpdater({ api, showMessage, openConfirm }) {
  const version = ref('')
  const updateInfo = ref(null)
  const isUpdating = ref(false)
  const isUpdateForced = ref(false)
  const updatePostponedVersion = ref(null)
  const updateProgress = ref({ status: 'idle', downloaded: 0, total: 0, percentage: 0 })

  const installUpdate = async () => {
    try {
      isUpdating.value = true
      isUpdateForced.value = true
      await api('/api/update/install', { method: 'POST' })

      const pollProgress = async () => {
        try {
          const res = await api('/api/update/progress')
          updateProgress.value = res
          if (res.status.startsWith('error')) {
            isUpdating.value = false
            showMessage('Error: ' + res.status, true)
            return
          }
          if (res.status !== 'finishing') {
            setTimeout(pollProgress, 500)
          }
        } catch (e) {
          setTimeout(pollProgress, 1000)
        }
      }
      pollProgress()
    } catch (err) {
      isUpdating.value = false
      showMessage('Error al iniciar la actualización: ' + err.message, true)
    }
  }

  const checkForUpdates = async (force = false) => {
    try {
      const data = await api('/api/update/check')
      version.value = data.current
      if (data.update_available) {
        updateInfo.value = data
        if (force) {
          isUpdateForced.value = true
        } else if (!isUpdateForced.value && updatePostponedVersion.value !== data.latest) {
          openConfirm({
            title: 'Nueva versión disponible',
            message: `Hay una actualización lista (${data.latest}). Se recomienda actualizar para obtener las mejoras.\n\nIMPORTANTE: No debe haber descargas activas durante el proceso para evitar que se corrompan. Si tienes tareas en curso, pospón la actualización y se aplicará automáticamente la próxima vez que inicies la aplicación.`,
            confirmText: 'Actualizar ahora',
            cancelText: 'Posponer',
            type: 'primary',
            action: () => {
              isUpdateForced.value = true
              installUpdate()
            },
            cancelAction: () => {
              updatePostponedVersion.value = data.latest
            }
          })
        }
      }
    } catch (err) {
      console.error('Error al buscar actualizaciones:', err)
    }
  }

  return {
    version,
    updateInfo,
    isUpdating,
    isUpdateForced,
    updatePostponedVersion,
    updateProgress,
    installUpdate,
    checkForUpdates,
  }
}
