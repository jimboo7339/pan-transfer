<script setup>
// 溢出菜单（⋯）。
//
// 为什么需要：任务卡片一行有 5 个按钮（运行/编辑/记录/暂停/删除），
// 表格视图里放不下，而窄屏上会换行导致卡片高度不齐。
// 把「次要动作」收进菜单，主操作留在外面。
//
// 无障碍：
//   - 触发按钮 aria-haspopup="menu" + aria-expanded
//   - 菜单 role="menu"，项 role="menuitem"
//   - Esc 关闭、点击外部关闭、选中后关闭
//   - 键盘 ↑↓ 在菜单项间移动
import { ref, computed, onMounted, onUnmounted } from 'vue'

const props = defineProps({
  // 菜单项：{ key, label, icon?, danger?, disabled? }
  items: { type: Array, default: () => [] },
  // 按钮无障碍标签
  label: { type: String, default: '更多操作' },
})
const emit = defineEmits(['select'])

const open = ref(false)
const root = ref(null)
const menuEl = ref(null)
const activeIndex = ref(0)

const enabledItems = computed(() => props.items.filter(i => !i.disabled))

function toggle() {
  open.value = !open.value
  if (open.value) {
    activeIndex.value = 0
    // 等 DOM 渲染完再聚焦第一个菜单项
    requestAnimationFrame(() => focusItem(0))
  }
}

function focusItem(i) {
  const items = menuEl.value?.querySelectorAll('[role="menuitem"]')
  if (items && items[i]) items[i].focus()
}

function onKeydown(e) {
  if (!open.value) return
  const n = enabledItems.value.length
  if (e.key === 'Escape') {
    e.preventDefault()
    close()
    return
  }
  if (e.key === 'ArrowDown') {
    e.preventDefault()
    activeIndex.value = (activeIndex.value + 1) % n
    focusItem(activeIndex.value)
    return
  }
  if (e.key === 'ArrowUp') {
    e.preventDefault()
    activeIndex.value = (activeIndex.value - 1 + n) % n
    focusItem(activeIndex.value)
    return
  }
  // Home / End
  if (e.key === 'Home') {
    e.preventDefault()
    activeIndex.value = 0
    focusItem(0)
  }
  if (e.key === 'End') {
    e.preventDefault()
    activeIndex.value = n - 1
    focusItem(n - 1)
  }
}

function choose(item) {
  if (item.disabled) return
  close()
  emit('select', item.key)
}

function close() {
  open.value = false
}

// 点击外部关闭：用 mousedown 而不是 click，
// 避免"按下按钮→菜单打开→同一个 click 事件冒泡到 document 立刻关闭"。
function onDocMouseDown(e) {
  if (!open.value) return
  if (root.value && !root.value.contains(e.target)) close()
}

onMounted(() => {
  window.addEventListener('keydown', onKeydown)
  document.addEventListener('mousedown', onDocMouseDown)
})
onUnmounted(() => {
  window.removeEventListener('keydown', onKeydown)
  document.removeEventListener('mousedown', onDocMouseDown)
})
</script>

<template>
  <div class="menu-root" ref="root">
    <button class="btn btn-icon btn-ghost" :aria-label="label"
      :aria-haspopup="'menu'" :aria-expanded="open" @click.stop="toggle">
      <svg width="16" height="16" viewBox="0 0 24 24" fill="currentColor">
        <circle cx="12" cy="5" r="1.6" />
        <circle cx="12" cy="12" r="1.6" />
        <circle cx="12" cy="19" r="1.6" />
      </svg>
    </button>
    <div v-if="open" class="menu-popover" role="menu" ref="menuEl">
      <div v-for="(it, i) in items" :key="it.key"
        class="menu-item" :class="{ danger: it.danger, disabled: it.disabled }"
        :role="it.disabled ? 'none' : 'menuitem'" :tabindex="it.disabled ? -1 : 0"
        @click="choose(it)" @mouseenter="activeIndex = i">
        <span v-if="it.icon" class="menu-icon">{{ it.icon }}</span>
        <span>{{ it.label }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.menu-root { position: relative; display: inline-flex; }
.menu-popover {
  position: absolute; right: 0; top: calc(100% + 4px); z-index: 130;
  min-width: 180px; padding: 4px;
  background: var(--surface); border: 1px solid var(--border);
  border-radius: 8px; box-shadow: var(--shadow-md);
  animation: menuIn 0.12s ease;
}
@keyframes menuIn {
  from { opacity: 0; transform: translateY(-4px); }
  to { opacity: 1; transform: none; }
}
.menu-item {
  display: flex; align-items: center; gap: 8px;
  padding: 7px 10px; border-radius: 6px; cursor: pointer;
  font-size: 13px; color: var(--text-2);
}
.menu-item:hover,
.menu-item:focus-visible {
  background: var(--surface-2); color: var(--text); outline: none;
}
.menu-item.danger { color: var(--danger); }
.menu-item.danger:hover { background: var(--danger-soft); }
.menu-item.disabled { opacity: 0.45; cursor: not-allowed; pointer-events: none; }
.menu-icon { font-size: 12px; width: 14px; text-align: center; flex-shrink: 0; }
@media (prefers-reduced-motion: reduce) {
  .menu-popover { animation: none; }
}
</style>