<script setup>
// 主题三档切换：浅色 / 深色 / 自动（跟随系统）。
import { theme, setTheme } from '../theme'

defineProps({ compact: Boolean })

const opts = [
  { v: 'light', label: '浅色' },
  { v: 'dark', label: '深色' },
  { v: 'auto', label: '自动' },
]
</script>

<template>
  <div class="theme-toggle" :class="{ compact }" role="group" aria-label="主题">
    <button v-for="o in opts" :key="o.v" :class="{ on: theme === o.v }" :title="o.label"
      @click="setTheme(o.v)">
      <svg v-if="o.v === 'light'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="4.2"/><path d="M12 2.5v2.4M12 19.1v2.4M2.5 12h2.4M19.1 12h2.4M4.9 4.9l1.7 1.7M17.4 17.4l1.7 1.7M19.1 4.9l-1.7 1.7M6.6 17.4l-1.7 1.7"/></svg>
      <svg v-else-if="o.v === 'dark'" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20.5 14.5A8.5 8.5 0 1 1 9.5 3.5a7 7 0 0 0 11 11z"/></svg>
      <svg v-else viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="12" rx="2"/><path d="M8 20h8M12 16v4"/></svg>
      <span v-if="!compact">{{ o.label }}</span>
    </button>
  </div>
</template>
