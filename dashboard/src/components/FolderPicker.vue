<script setup>
import { ref } from 'vue'
import { ArrowLeft, Check, ChevronRight, Folder, HardDrive, X } from 'lucide-vue-next'

defineProps({ modelValue: { type: String, default: '' } })
const emit = defineEmits(['update:modelValue'])

const open = ref(false)
const loading = ref(false)
const error = ref('')
const currentPath = ref('')
const parentPath = ref(null)
const roots = ref([])
const entries = ref([])

const api = async (url) => {
  const response = await fetch(url)
  const data = await response.json().catch(() => ({}))
  if (!response.ok) throw new Error(data.detail || 'No se puede leer la carpeta')
  return data
}

const browse = async path => {
  loading.value = true
  error.value = ''
  try {
    const query = path ? `?path=${encodeURIComponent(path)}` : ''
    const data = await api(`/api/filesystem${query}`)
    roots.value = data.roots || []
    entries.value = data.entries || []
    currentPath.value = data.path || ''
    parentPath.value = data.parent || null
  } catch (err) { error.value = err.message } finally { loading.value = false }
}

const show = async () => { open.value = true; await browse(null) }
const close = () => { open.value = false; error.value = '' }
const select = () => { if (currentPath.value) emit('update:modelValue', currentPath.value); close() }
</script>

<template>
  <div class="folder-picker">
    <div class="setting-label compact"><span>Carpeta de descargas</span></div>
    <button class="folder-current" type="button" @click="show"><Folder :size="16" /><span :title="modelValue">{{ modelValue || 'Seleccionar carpeta' }}</span><ChevronRight :size="15" /></button>
    <small>Las próximas descargas usarán esta ubicación.</small>

    <div v-if="open" class="folder-overlay" @click.self="close">
      <div class="folder-dialog" role="dialog" aria-modal="true" aria-label="Seleccionar carpeta">
        <div class="folder-dialog-header"><div><span class="eyebrow"><Folder :size="12" /> EXPLORADOR</span><h3>Seleccionar carpeta</h3></div><button class="icon-button" type="button" @click="close" aria-label="Cerrar"><X :size="18" /></button></div>
        <div class="folder-path" :title="currentPath">{{ currentPath || 'Este equipo' }}</div>
        <div v-if="error" class="folder-error">{{ error }}</div>
        <div v-if="loading" class="folder-loading"><Folder :size="20" /> Leyendo carpetas…</div>
        <div v-else class="folder-list">
          <button v-for="root in roots" :key="root" class="folder-entry root-entry" type="button" @click="browse(root)"><HardDrive :size="18" /><span>{{ root }}</span><ChevronRight :size="15" /></button>
          <button v-if="parentPath" class="folder-entry parent-entry" type="button" @click="browse(parentPath)"><ArrowLeft :size="17" /><span>Volver a la carpeta anterior</span></button>
          <button v-for="entry in entries" :key="entry.path" class="folder-entry" type="button" @click="browse(entry.path)"><Folder :size="18" /><span>{{ entry.name }}</span><ChevronRight :size="15" /></button>
          <div v-if="!roots.length && !parentPath && !entries.length" class="folder-empty">No hay carpetas disponibles.</div>
        </div>
        <div class="folder-dialog-footer"><button class="secondary-button" type="button" @click="close">Cancelar</button><button class="primary-button compact-button" type="button" :disabled="!currentPath || loading" @click="select"><Check :size="15" /> Usar esta carpeta</button></div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.folder-picker small{display:block;color:var(--user-text-dim);font-size:11px;margin-top:7px}.folder-current{width:100%;display:flex;align-items:center;gap:8px;border:1px solid var(--user-border-light);background:var(--user-bg-base);color:#cfe0ee;border-radius:10px;padding:11px 12px;cursor:pointer;text-align:left}.folder-current:hover{border-color:var(--user-primary);background:var(--user-surface-light)}.folder-current span{flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:12px}.folder-current svg:first-child{color:var(--user-primary);flex:none}.folder-current svg:last-child{color:var(--user-text-dim);flex:none}.folder-overlay{position:fixed;inset:0;z-index:20;display:grid;place-items:center;padding:20px;background:rgba(2, 8, 19, 0.7);backdrop-filter:blur(8px);animation:folderFade .2s ease}.folder-dialog{width:min(620px,100%);max-height:min(720px,90vh);display:flex;flex-direction:column;background:var(--user-surface);border:1px solid var(--user-border-light);border-radius:18px;box-shadow:0 24px 80px rgba(0,0,0,0.5);overflow:hidden;animation:folderPop .25s ease}.folder-dialog-header{display:flex;justify-content:space-between;align-items:start;padding:22px 24px 15px}.folder-dialog-header h3{font:600 19px 'Space Grotesk';color:#f0f6fc;margin:6px 0 0}.icon-button{display:grid;place-items:center;border:0;background:var(--user-bg-base);color:var(--user-text-dim);width:32px;height:32px;border-radius:9px;cursor:pointer}.icon-button:hover{color:#fff;background:var(--user-surface-light)}.folder-path{margin:0 24px 12px;padding:10px 12px;border-radius:8px;background:var(--user-bg-base);border:1px solid var(--user-border);color:var(--user-accent);font:11px monospace;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}.folder-list{min-height:190px;overflow:auto;padding:0 12px 12px}.folder-entry{width:100%;display:flex;align-items:center;gap:10px;border:0;border-bottom:1px solid var(--user-border);background:transparent;color:#c9d9e7;padding:12px;border-radius:7px;cursor:pointer;text-align:left}.folder-entry:hover{background:var(--user-icon-bg);color:#fff}.folder-entry span{flex:1;font-size:12px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.folder-entry svg:first-child{color:var(--user-primary);flex:none}.folder-entry svg:last-child{color:var(--user-text-dim);flex:none}.root-entry{background:var(--user-surface-light);margin-bottom:5px}.parent-entry{color:var(--user-accent)}.folder-loading,.folder-empty{text-align:center;padding:42px 10px;color:var(--user-text-dim);font-size:12px}.folder-loading svg{display:inline-block;vertical-align:middle;color:var(--user-primary);animation:folderSpin 1.2s linear infinite}.folder-error{margin:0 24px 12px;padding:9px 11px;border-radius:8px;background:#48252c;border:1px solid #8c4550;color:#ffb0b0;font-size:11px}.folder-dialog-footer{display:flex;justify-content:flex-end;gap:9px;padding:15px 24px;border-top:1px solid var(--user-border);background:var(--user-bg-base)}.secondary-button{border:1px solid var(--user-border-light);border-radius:9px;background:transparent;color:var(--user-text-dim);padding:10px 14px;font:500 12px 'DM Sans';cursor:pointer}.secondary-button:hover{background:var(--user-surface-light);color:#fff}.compact-button{height:auto;padding:10px 14px}@keyframes folderFade{from{opacity:0}to{opacity:1}}@keyframes folderPop{from{opacity:0;transform:scale(.97) translateY(8px)}to{opacity:1;transform:scale(1) translateY(0)}}@keyframes folderSpin{to{transform:rotate(360deg)}}
</style>
