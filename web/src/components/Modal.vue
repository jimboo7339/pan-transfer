<script setup>
// 通用弹层。
//
// 此前这个组件存在但**从未被使用**：8 个文件 import 了它却各自手搓
// class="modal-overlay"（13 处），而它恰恰是唯一实现了 ESC 关闭的组件。
// 这次把它补齐成「可用的弹层」并逐步替换那些手写模板。
//
// 无障碍要点（原实现全部缺失）：
//   - role="dialog" + aria-modal + aria-labelledby：读屏器能识别这是一个弹层
//   - 打开时记住触发元素、初始聚焦弹层内第一个可聚焦控件
//   - 关闭时把焦点还给触发元素（否则键盘用户会"丢失位置"）
//   - 打开时锁 body 滚动（否则背景会跟着滚）
//   - 焦点陷阱：Tab 不会跑到弹层外面的页面元素上
import { watch, nextTick, ref } from 'vue'

const props = defineProps({
  modelValue: Boolean,
  title: String,
  size: { type: String, default: '' }, // '' | 'lg' | 'xl'
})
const emit = defineEmits(['update:modelValue'])

const panel = ref(null)
const titleId = `modal-title-${Math.random().toString(36).slice(2, 9)}`
let lastFocused = null

const FOCUSABLE = [
  'a[href]', 'button:not([disabled])', 'input:not([disabled])',
  'select:not([disabled])', 'textarea:not([disabled])', '[tabindex]:not([tabindex="-1"])',
].join(',')

function focusables() {
  if (!panel.value) return []
  return Array.from(panel.value.querySelectorAll(FOCUSABLE)).filter(
    (el) => el.offsetParent !== null || el === document.activeElement,
  )
}

function close() {
  emit('update:modelValue', false)
}

function onKey(e) {
  if (!props.modelValue) return
  if (e.key === 'Escape') {
    e.preventDefault()
    close()
    return
  }
  // 焦点陷阱：Tab / Shift+Tab 循环限制在弹层内。
  if (e.key !== 'Tab') return
  const items = focusables()
  if (items.length === 0) {
    e.preventDefault()
    return
  }
  const first = items[0]
  const last = items[items.length - 1]
  if (e.shiftKey && document.activeElement === first) {
    e.preventDefault()
    last.focus()
  } else if (!e.shiftKey && document.activeElement === last) {
		e.preventDefault()
		first.focus()
	}
}

function lockScroll(locked) {
  // 保存原来的 overflow，避免覆盖别的弹层设置的值。
  if (locked) {
    document.body.dataset.modalPrevOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
  } else if (document.body.dataset.modalPrevOverflow !== undefined) {
    document.body.style.overflow = document.body.dataset.modalPrevOverflow
    delete document.body.dataset.modalPrevOverflow
  }
}

watch(
  () => props.modelValue,
  async (open) => {
    if (open) {
      lastFocused = document.activeElement
      window.addEventListener('keydown', onKey)
      lockScroll(true)
      await nextTick()
      // 初始聚焦：优先第一个可聚焦元素，否则聚焦弹层自身。
      const items = focusables()
      if (items.length) items[0].focus()
      else panel.value && panel.value.focus()
    } else {
      window.removeEventListener('keydown', onKey)
      lockScroll(false)
      // 焦点还给触发按钮。
      if (lastFocused && typeof lastFocused.focus === 'function') {
        lastFocused.focus()
      }
      lastFocused = null
    }
  },
)
</script>

<template>
  <Teleport to="body">
    <div v-if="modelValue" class="modal-overlay" @mousedown.self="close">
      <div class="modal" :class="size ? 'modal-' + size : ''"
        ref="panel" role="dialog" aria-modal="true" :aria-labelledby="titleId" tabindex="-1">
        <div class="modal-head">
          <!-- title 是纯文本（无障碍需要 aria-labelledby 指向它）；
               需要富文本标题（如"分享链接预览（共 N 个文件）"）时传 #title 插槽，
               此时插槽内容放在 span 里，无障碍名回退到 title 或 slot 文本。 -->
          <div class="modal-title" :id="titleId">
            <slot name="title">{{ title }}</slot>
          </div>
          <button class="modal-close" aria-label="关闭" @click="close">✕</button>
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