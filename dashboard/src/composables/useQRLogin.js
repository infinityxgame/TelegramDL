import { onUnmounted, ref } from 'vue'

export function useQRLogin({ authHeaders, t, authStatus, emit, apiCall }) {
  const qrImage = ref('')
  const qrStatus = ref('')
  const qrError = ref('')
  let pollTimer = null

  const stopQRPolling = () => {
    if (pollTimer) clearInterval(pollTimer)
    pollTimer = null
  }

  const pollQRStatus = async () => {
    try {
      const res = await fetch('/api/auth/qr/status', { headers: authHeaders() })
      const data = await res.json()
      if (!res.ok) throw new Error(data.detail || t('auth.errProcess'))

      qrStatus.value = data.status || ''
      if (data.image) qrImage.value = data.image

      if (data.status === 'WAITING_2FA') {
        stopQRPolling()
        qrImage.value = ''
        // No reutilizar authStatus: el composable puede conservar una copia
        // anterior a la configuración de credenciales. El servidor es la
        // fuente de verdad y debe conservar has_credentials=true para que
        // AuthWizard vaya directamente al paso de 2FA.
        const statusRes = await fetch('/api/auth/status', {
          headers: authHeaders()
        })
        const authData = await statusRes.json()
        emit('auth-success', {
          ...authData,
          authenticated: false,
          state: 'WAITING_2FA'
        })
      } else if (data.status === 'LOGGED_IN') {
        stopQRPolling()
        qrImage.value = ''
        const statusRes = await fetch('/api/auth/status', {
          headers: authHeaders()
        })
        const authData = await statusRes.json()
        emit('auth-success', {
          ...authData,
          authenticated: true,
          state: 'LOGGED_IN'
        })
      } else if (data.status === 'ERROR') {
        stopQRPolling()
        qrError.value = data.error || t('auth.errProcess')
      } else if (data.status === 'EXPIRED' || data.status === 'CANCELLED') {
        stopQRPolling()
        qrImage.value = ''
      }
    } catch (err) {
      stopQRPolling()
      qrError.value = err.message
      console.error('QR login status polling failed:', err)
    }
  }

  const startQRLogin = async () => {
    qrError.value = ''
    qrImage.value = ''
    try {
      const data = await apiCall('/api/auth/qr/start', {})
      qrStatus.value = data.status || 'STARTING'
      await pollQRStatus()
      if (qrStatus.value === 'WAITING_SCAN' || qrStatus.value === 'STARTING') {
        stopQRPolling()
        pollTimer = setInterval(pollQRStatus, 3000)
      }
    } catch (err) {
      qrError.value = err.message
      console.error('Could not start QR login:', err)
    }
  }

  const cancelQRLogin = async () => {
    stopQRPolling()
    try {
      await apiCall('/api/auth/qr/cancel', {})
    } catch (err) {
      qrError.value = err.message
      console.error('Could not cancel QR login:', err)
    } finally {
      qrImage.value = ''
      qrStatus.value = 'CANCELLED'
    }
  }

  onUnmounted(() => {
    stopQRPolling()
    if (qrStatus.value === 'WAITING_SCAN' || qrStatus.value === 'STARTING') {
      void cancelQRLogin()
    }
  })

  return {
    qrImage,
    qrStatus,
    qrError,
    startQRLogin,
    cancelQRLogin
  }
}
