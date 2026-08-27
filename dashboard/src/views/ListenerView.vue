<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { Download, Inbox, MessageCircle, Plus, Radio, Trash2 } from 'lucide-vue-next'

const props = defineProps({
  notify: { type: Function, default: () => {} }
})

const enabled = ref(true)
const chatIds = ref([])
const newChatId = ref('')
const items = ref([])
const saving = ref(false)
const error = ref('')
let timer

const api = async (url, options = {}) => {
  const response = await fetch(url, options)
  const data = await response.json().catch(() => ({}))
  if (!response.ok) throw new Error(data.detail || data.error || 'Error en el servidor')
  return data
}

const load = async () => {
  try {
    const [settings, detected] = await Promise.all([api('/api/listener/settings'), api('/api/listener')])
    enabled.value = settings.enabled
    chatIds.value = settings.chat_ids
    items.value = detected
    error.value = ''
  } catch (err) {
  }
}

const save = async () => {
  saving.value = true
  try {
    const data = await api('/api/listener/settings', {
      method: 'PUT', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ enabled: enabled.value, chat_ids: chatIds.value })
    })
    enabled.value = data.enabled
    chatIds.value = data.chat_ids
    error.value = ''
    props.notify('Configuración de escucha guardada')
  } catch (err) { props.notify(err.message, true) } finally { saving.value = false }
}

const addChat = async () => {
  const value = Number(newChatId.value.trim())
  if (!Number.isInteger(value) || value === 0) { error.value = 'Escribe un ID de chat válido'; return }
  if (!chatIds.value.includes(value)) chatIds.value = [...chatIds.value, value]
  newChatId.value = ''
  await save()
}

const removeChat = async id => {
  chatIds.value = chatIds.value.filter(item => item !== id)
  await save()
}
const toggle = async () => { await save() }
const download = async item => {
  try {
    await api('/api/listener/download', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: item.id }) })
    props.notify('Descarga añadida a la cola')
    await load()
  } catch (err) { props.notify(err.message, true) }
}

const removeItem = async item => {
  try {
    await api(`/api/downloads/${encodeURIComponent(item.id)}`, { method: 'DELETE' })
    props.notify('Multimedia descartada')
    await load()
  } catch (err) { props.notify(err.message, true) }
}
const statusText = status => ({ available: 'Disponible', queued: 'En cola', downloading: 'Descargando', completed: 'Completado', failed: 'Fallido' }[status] || status)
const availableCount = computed(() => items.value.filter(item => item.status === 'available').length)

onMounted(async () => { await load(); timer = setInterval(load, 1500) })
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <section class="listener-view">
    <section class="listener-hero"><div><span class="hero-kicker"><Radio :size="13" /> MONITOR DE MENSAJES</span><h2>Escucha multimedia en tiempo real</h2><p>Cuando llegue un archivo a uno de tus chats, aparecerá aquí listo para descargar.</p></div><label class="switch large"><input v-model="enabled" type="checkbox" @change="toggle"><span></span><b>{{ enabled ? 'Escucha activa' : 'Escucha pausada' }}</b></label></section>
    <div class="listener-grid">
      <section class="panel listener-config"><div class="panel-heading"><div><span class="eyebrow"><MessageCircle :size="12" /> ORÍGENES</span><h2>Chats vigilados</h2></div><span class="count-pill">{{ chatIds.length }} configurados</span></div><p class="helper-text">Añade el ID numérico de un grupo o chat privado. El usuario debe pertenecer a ese chat.</p><div class="listener-add"><input v-model="newChatId" @keyup.enter="addChat" placeholder="Ej. -1001234567890"><button class="save-button" :disabled="saving" @click="addChat"><Plus :size="15" /> Añadir</button></div><div v-if="!chatIds.length" class="empty-small">No hay chats configurados.</div><div v-for="id in chatIds" :key="id" class="chat-chip"><MessageCircle :size="14" /><strong>{{ id }}</strong><button :disabled="saving" @click="removeChat(id)" aria-label="Eliminar chat"><Trash2 :size="14" /></button></div><button class="save-button listener-save" :disabled="saving" @click="save">{{ saving ? 'Guardando…' : 'Guardar chats' }}</button><small class="save-hint">Los cambios se guardan en config.json del servidor.</small></section>
      <section class="panel listener-feed"><div class="panel-heading"><div><span class="eyebrow"><Inbox :size="12" /> BANDEJA DE ENTRADA</span><h2>Multimedia detectada</h2></div><span class="count-pill">{{ availableCount }} nuevas</span></div><div v-if="!items.length" class="empty-state"><Inbox :size="28" /><p>Aún no se detectó multimedia</p><small>Deja esta vista abierta o vuelve cuando llegue un archivo.</small></div><div v-for="item in items" :key="item.id" class="listener-item"><div class="file-symbol"><Inbox :size="16" /></div><div class="file-info"><strong>{{ item.file_name }}</strong><span>Chat {{ item.chat_id }} · mensaje {{ item.message_id }} · {{ item.total_str }}</span></div><div class="row-side"><span class="listener-status">{{ statusText(item.status) }}</span><div class="row-actions"><button v-if="item.status === 'available'" class="download-small" @click="download(item)"><Download :size="13" /> Descargar</button><button class="delete-small" @click="removeItem(item)" aria-label="Eliminar"><Trash2 :size="13" /></button></div></div></div></section>
    </div>
  </section>
</template>

<style scoped>
.listener-view{display:flex;flex-direction:column;gap:18px}.listener-hero{display:flex;justify-content:space-between;align-items:center;gap:20px;padding:28px 30px;border:1px solid #234765;border-radius:18px;background:linear-gradient(110deg,#102a43,#0e2033 70%)}.listener-hero h2{font:600 23px 'Space Grotesk';margin:8px 0 5px;color:#f1f7ff}.listener-hero p,.helper-text{margin:0;color:#8da5bb;font-size:13px}.switch.large{display:flex;align-items:center;gap:10px;white-space:nowrap}.switch.large b{font-size:12px;color:#9cc3df;font-weight:500}.listener-grid{display:grid;grid-template-columns:.82fr 1.18fr;gap:18px}.helper-text{line-height:1.5;margin-bottom:17px}.listener-add{display:flex;gap:8px}.listener-add input{flex:1;min-width:0;background:#081725;border:1px solid #294963;color:#dbe7f5;border-radius:10px;padding:11px 12px;outline:none;font:inherit}.listener-add .save-button{width:auto;margin:0;padding:0 15px}.chat-chip{display:flex;align-items:center;gap:9px;border-top:1px solid #193149;padding:12px 0;color:#64bfff;font-size:12px}.chat-chip strong{flex:1;color:#d6e4f1;font-weight:500}.chat-chip button{border:0;background:transparent;color:#e58b91;font-size:20px;cursor:pointer}.save-hint{display:block;color:#5d7890;font-size:10px;margin-top:13px}.listener-item{display:flex;align-items:center;gap:12px;border-top:1px solid #193149;padding:13px 0}.file-info{flex:1;min-width:0}.row-side{display:flex;flex-direction:column;align-items:flex-end;gap:5px;margin-left:auto}.row-actions{display:flex;align-items:center;gap:6px}.listener-status{font-size:10px;color:#7c9bb4}.download-small{border:1px solid #2e8fca;background:#10334d;color:#6ec7ff;border-radius:7px;padding:6px 9px;font-size:10px;cursor:pointer;display:flex;align-items:center;gap:4px}.download-small:hover{background:#164c70}.delete-small{border:1px solid #4a2b2d;background:#251415;color:#e58b91;border-radius:7px;padding:6px 9px;font-size:10px;cursor:pointer;display:flex;align-items:center;justify-content:center}.delete-small:hover{background:#3a1d1f}@media(max-width:900px){.listener-grid{grid-template-columns:1fr}}@media(max-width:580px){.listener-hero{align-items:flex-start;flex-direction:column;padding:22px}.listener-add{flex-direction:column}.listener-add .save-button{height:38px}.listener-item .row-side{min-width:75px}}
</style>
