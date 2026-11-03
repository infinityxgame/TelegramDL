<script setup>
import { computed } from 'vue'
import { useId } from 'vue'

defineOptions({ inheritAttrs: false })

const model = defineModel({ type: [String, Number], default: '' })

const props = defineProps({
  label: { type: String, default: '' },
  id: { type: String, default: '' },
  options: { type: Array, required: true },
  disabled: { type: Boolean, default: false }
})

const localId = props.id || useId()

const normalizedOptions = computed(() =>
  props.options.map((option) => {
    if (option && typeof option === 'object') {
      return {
        value: option.value,
        label: option.label ?? option.value,
        disabled: option.disabled ?? false
      }
    }
    return { value: option, label: option, disabled: false }
  })
)
</script>

<template>
  <div class="app-select">
    <label v-if="label" class="app-select__label" :for="localId">
      {{ label }}
    </label>
    <select
      v-bind="$attrs"
      :id="localId"
      v-model="model"
      class="app-select__control"
      :disabled="disabled"
    >
      <option
        v-for="option in normalizedOptions"
        :key="option.value"
        :value="option.value"
        :disabled="option.disabled"
      >
        {{ option.label }}
      </option>
    </select>
  </div>
</template>

<style>
.app-select {
  display: inline-flex;
}

.app-select__label {
  font-size: 10px;
  font-weight: 600;
  color: var(--user-text-dim, #94a8bc);
  text-transform: uppercase;
  letter-spacing: 0.3px;
}

.app-select__control {
  cursor: pointer;
  appearance: none;
  -webkit-appearance: none;
  -moz-appearance: none;
  background-color: var(--user-bg-base);
  background-image:
    linear-gradient(45deg, transparent 50%, currentColor 50%),
    linear-gradient(135deg, currentColor 50%, transparent 50%);
  background-position:
    calc(100% - 10px) 52%,
    calc(100% - 6px) 52%;
  background-size:
    4px 4px,
    4px 4px;
  background-repeat: no-repeat;
  border: 1px solid var(--user-border-light);
  color: var(--user-text-main, #dbe7f5);
  border-radius: 8px;
  padding: 0 18px 0 8px;
  font-size: 11px;
  height: 32px;
  outline: none;
  box-sizing: border-box;
  margin: 0;
  cursor: pointer;
}

.app-select__control option {
  background-color: var(--user-bg-base);
  color: var(--user-text-main, #dbe7f5);
}

html.theme-light .app-select__control {
  color: #0f172a !important;
  background-color: var(--user-bg-base, #ffffff) !important;
  border-color: var(--light-border, rgba(0, 0, 0, 0.15)) !important;
  background-image:
    linear-gradient(45deg, transparent 50%, var(--light-ink-soft, #64748b) 50%),
    linear-gradient(135deg, var(--light-ink-soft, #64748b) 50%, transparent 50%) !important;
}

html.theme-light .app-select__control option {
  color: #0f172a !important;
  background-image:
    linear-gradient(45deg, transparent 50%, var(--light-ink-soft, #64748b) 50%),
    linear-gradient(135deg, var(--light-ink-soft, #64748b) 50%, transparent 50%) !important;
}
</style>
