<script setup>
// 统一确认弹层，替代 8 处原生 confirm()。
//
// 原生 confirm 的问题不是"丑"，而是：
//   1. 阻塞主线程，且无法展示结构化信息（"将同时删除 3 个任务及其 120 条运行记录"）；
//   2. 部分 WebView / 浏览器设置里被禁用，此时返回 false —— 删除静默失效，
//      用户以为删了其实没删；
//   3. 无法要求二次输入（删任务、关访问密码这类不可逆操作应该有这个）。
//
// 用法（confirmDialog 由 main.js provide）：
//   const ok = await confirmDialog({
//     title: '删除任务',
//     body: '确定删除「xxx」？',
//     warn: '运行与转存历史将一并删除，此操作不可撤销。',
//     confirmText: '删除',
//     variant: 'danger',
//     requireText: 'xxx',   // 可选：必须输入指定文字才能确认
//   })
//
// 安全：body 按纯文本渲染（用 {{ }} 而非 v-html），
// 因为它常包含任务名/账号名这类来自网盘的字符串。
import { ref, reactive } from 'vue'

const visible = ref(false)
const input = ref('')
const opts = reactive({
  title: '',
  body: '',
  warn: '',
  confirmText: '确定',
  cancelText: '取消',
  variant: 'primary', // 'primary' | 'danger'
  requireText: '',
  resolve: null,
})

const canConfirm = () => (opts.requireText ? input.value === opts.requireText : true)

function open(options) {
  return new Promise((resolve) => {
    opts.title = options.title || '请确认'
    opts.body = options.body || ''
    opts.warn = options.warn || ''
    opts.confirmText = options.confirmText || '确定'
    opts.cancelText = options.cancelText || '取消'
    opts.variant = options.variant || 'primary'
    opts.requireText = options.requireText || ''
    opts.resolve = resolve
    input.value = ''
    visible.value = true
  })
}

function settle(ok) {
  const r = opts.resolve
  opts.resolve = null
  visible.value = false
  if (r) r(ok)
}

function confirm() {
  if (canConfirm()) settle(true)
}

defineExpose({ open })
</script>

<template>
  <Teleport to="body">
    <div v-if="visible" class="modal-overlay" @mousedown.self="settle(false)">
      <div class="modal" style="width: 440px" role="alertdialog" aria-modal="true"
        aria-labelledby="confirm-title" aria-describedby="confirm-body">
        <div class="modal-head">
          <div class="modal-title" id="confirm-title">{{ opts.title }}</div>
          <button class="modal-close" aria-label="关闭" @click="settle(false)">✕</button>
        </div>
        <div class="modal-body">
          <div class="confirm-body" id="confirm-body">{{ opts.body }}</div>
          <div v-if="opts.warn" class="confirm-warn">{{ opts.warn }}</div>
          <div v-if="opts.requireText" class="confirm-input">
            <div class="field-label">
              请输入 <code class="mono">{{ opts.requireText }}</code> 以确认
            </div>
            <input class="input mono" v-model="input" :placeholder="opts.requireText"
              autocomplete="off" @keyup.enter="confirm" autofocus />
            <div class="field-hint">此操作不可逆，需要手动输入以避免误触。</div>
          </div>
        </div>
        <div class="modal-foot">
          <button class="btn" @click="settle(false)">{{ opts.cancelText }}</button>
          <button class="btn" :class="opts.variant === 'danger' ? 'btn-danger-solid' : 'btn-primary'"
            :disabled="!canConfirm()" @click="confirm">{{ opts.confirmText }}</button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
/* 危险按钮需要实心红底才能承载"不可逆"的分量；
   .btn-danger 只是红字 + hover 变红，作为确认键不够明确。 */
.btn-danger-solid {
  background: var(--danger);
  border-color: var(--danger);
  color: #fff;
  box-shadow: 0 2px 8px rgba(220, 38, 38, 0.3);
}
.btn-danger-solid:hover {
  background: #b91c1c;
  border-color: #b91c1c;
  color: #fff;
}
</style>