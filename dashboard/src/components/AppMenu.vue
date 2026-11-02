<script setup>
import { computed, nextTick, ref, watch } from 'vue'

// La vista sigue siendo dueña del estado `open`: el menú solo calcula la
// posición anclada al disparador y teletransporta el panel al `body`.
const open = defineModel('open', { type: Boolean, default: false })

const props = defineProps({
  // Alineación horizontal del panel respecto al disparador.
  align: {
    type: String,
    default: 'left',
    validator: (value) => ['left', 'right'].includes(value)
  },
  // Separación vertical entre el disparador y el panel.
  offset: {
    type: Number,
    default: 6
  },
  // Espacio mínimo debajo del disparador antes de abrir hacia arriba.
  dropUpThreshold: {
    type: Number,
    default: 170
  },
  // Clases heredadas del menú actual para no romper los temas existentes.
  menuClass: {
    type: [String, Array, Object],
    default: ''
  }
})

const emit = defineEmits(['toggle'])

const menuRef = ref(null)
const position = ref({
  dropUp: false,
  x: 0,
  top: 0,
  bottom: null
})

const resolveAnchor = (event) => {
  if (event) {
    let target = event.currentTarget || event.target
    if (target && target.closest) {
      target = target.closest('button') || target
    }
    if (target && target.getBoundingClientRect) {
      return target
    }
  }
  return menuRef.value
}

const updatePosition = (event) => {
  const anchor = resolveAnchor(event)
  if (!anchor || !anchor.getBoundingClientRect) return

  const rect = anchor.getBoundingClientRect()
  const spaceBelow = window.innerHeight - rect.bottom
  const spaceAbove = rect.top
  const dropUp = spaceBelow < props.dropUpThreshold && spaceAbove > spaceBelow

  position.value = {
    dropUp,
    x: props.align === 'right' ? window.innerWidth - rect.right : rect.left,
    top: dropUp ? null : rect.bottom + props.offset,
    bottom: dropUp ? window.innerHeight - rect.top + props.offset : null
  }
}

const toggleMenu = (event) => {
  const next = !open.value
  if (next) {
    updatePosition(event)
  }
  open.value = next
  emit('toggle', next)
}

// Si la vista abre el menú de forma programática, la posición se recalcula
// cuando el disparador ya está pintado.
watch(open, async (value) => {
  if (value) {
    await nextTick()
    updatePosition()
  }
})

const panelStyle = computed(() => {
  const style = {
    position: 'fixed',
    zIndex: 9999
  }

  if (props.align === 'right') {
    style.right = `${position.value.x}px`
  } else {
    style.left = `${position.value.x}px`
  }

  if (position.value.dropUp) {
    style.top = 'auto'
    style.bottom = `${position.value.bottom}px`
  } else {
    style.top = `${position.value.top}px`
    style.bottom = 'auto'
  }

  return style
})
</script>

<template>
  <!-- El disparador y las opciones llegan por slot para que el CSS con scope
       de la vista siga aplicándose a esos nodos; este componente solo pinta
       el contenedor y el panel teletransportado. -->
  <div ref="menuRef" class="app-menu">
    <slot name="trigger" :toggle="toggleMenu" />
    <Teleport to="body">
      <div
        v-if="open"
        class="app-menu__panel"
        :class="[
          menuClass,
          `app-menu__panel--${align}`,
          { 'drop-up': position.dropUp }
        ]"
        :style="panelStyle"
        @click.stop
      >
        <slot />
      </div>
    </Teleport>
  </div>
</template>

<!-- Estilos globales a propósito: los nodos que renderiza un componente hijo
     no reciben el scope de la vista, así que la estructura del panel vive
     aquí con prefijo `.app-menu`. Las clases heredadas (`name-dropdown`,
     etc.) se conservan para los temas existentes. -->
<style>
.app-menu {
  position: relative;
  display: inline-flex;
}

.app-menu__panel {
  min-width: 200px;
  max-width: 320px;
  background: var(--user-surface);
  border: 2px solid var(--user-primary);
  border-radius: 10px;
  padding: 6px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.4);
}

.app-menu__panel::before {
  content: '';
  position: absolute;
  top: -6px;
  width: 10px;
  height: 10px;
  background: var(--user-surface);
  border-left: 2px solid var(--user-primary);
  border-top: 2px solid var(--user-primary);
  transform: rotate(45deg);
}

.app-menu__panel--left::before {
  left: 16px;
}

.app-menu__panel--right::before {
  right: 16px;
}

.app-menu__panel.drop-up::before {
  top: auto;
  bottom: -6px;
  border-left: none;
  border-top: none;
  border-right: 2px solid var(--user-primary);
  border-bottom: 2px solid var(--user-primary);
}
</style>
