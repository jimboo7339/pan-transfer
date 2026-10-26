<script setup>
import { watch } from 'vue'

const props = defineProps({
  modelValue: Boolean,
  title: String,
  size: { type: String, default: '' }, // '' | 'lg' | 'xl'
})
const emit = defineEmits(['update:modelValue'])

function close() {
  emit('update:modelValue', false)
}

function onKey(e) {
  if (e.key === 'Escape' && props.modelValue) close()
}
watch(() => props.modelValue, (v) => {
  if (v) window.addEventListener('keydown', onKey)
  else window.removeEventListener('keydown', onKey)
})
</script>

<template>
  <Teleport to="body">
    <div v-if="modelValue" class="modal-overlay" @mousedown.self="close">
      <div class="modal" :class="size ? 'modal-' + size : ''">
        <div class="modal-head">
          <div class="modal-title">{{ title }}</div>
          <button class="modal-close" @click="close">✕</button>
        </div>
        <div class="modal-body">
          <slot />
        </div>
        <div v-if="$slots.footer" class="modal-foot">
          <slot name="footer" />
        </div>
      </div>
    </div>
  </Teleport>
</template>
