<script setup>
// 周几选择器：值是逗号分隔的 1-7（1=周一）；空 = 每天。
const props = defineProps({
  modelValue: { type: String, default: '' },
})
const emit = defineEmits(['update:modelValue'])

const labels = ['一', '二', '三', '四', '五', '六', '日']
const days = () => (props.modelValue ? props.modelValue.split(',').filter(Boolean).map(Number) : [])

function toggle(d) {
  const arr = days()
  const i = arr.indexOf(d)
  if (i >= 0) arr.splice(i, 1)
  else arr.push(d)
  arr.sort()
  emit('update:modelValue', arr.join(','))
}
</script>

<template>
  <div class="week-picker">
    <button v-for="(l, i) in labels" :key="i" type="button"
      class="week-day" :class="{ on: days().includes(i + 1) }"
      @click="toggle(i + 1)">{{ l }}</button>
  </div>
</template>
