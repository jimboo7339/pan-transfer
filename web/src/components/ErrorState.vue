<script setup>
// 统一的错误态组件。
//
// 为什么需要：此前所有加载失败都落到「空状态」里——
// Tasks.vue 的 loading 在 finally 里无条件置 false，于是"加载失败"和
// "真的没有数据"渲染成同一个空卡片，用户完全无法区分。
//
// 三种状态必须分开：
//   - 加载中：骨架屏 / spinner
//   - 加载失败：说明原因 + 重试按钮（网络/5xx 才给重试）
//   - 真的没有数据：引导用户去创建（空状态）
//
// 同时区分错误类型：网络错误提示"检查网络"，5xx 提示"服务端异常，可重试"，
// 400 提示"请检查输入"，401 交给全局横幅（这里不重复）。
import { computed } from 'vue'
import { ERR, RETRYABLE } from '../api'

const props = defineProps({
  error: { type: [Error, String, Object], default: null },
  // 自定义标题（默认按错误类型生成）。
  title: String,
  // 补充说明（比如"请检查网盘账号是否掉线"）。
  hint: String,
  // 重试回调；不传则不显示重试按钮。
  onRetry: { type: Function, default: null },
})

const emit = defineEmits(['retry'])

const kind = computed(() => {
  const e = props.error
  if (!e) return ''
  if (typeof e === 'string') return ERR.REQUEST
  return e.kind || ERR.REQUEST
})

const title = computed(() => {
  if (props.title) return props.title
  switch (kind.value) {
    case ERR.NETWORK: return '网络不可达'
    case ERR.SERVER: return '服务端异常'
    case ERR.AUTH: return '会话已过期'
    case ERR.VALIDATION: return '请求无效'
    default: return '加载失败'
  }
})

const defaultHint = computed(() => {
  switch (kind.value) {
    case ERR.NETWORK:
      return '请检查网络连接，或服务是否正在运行。'
    case ERR.SERVER:
      return '服务端处理时出错，可能是暂时的，可以稍后重试。'
    case ERR.AUTH:
      return '请重新输入访问密码。'
    case ERR.VALIDATION:
      return '请检查输入内容是否符合要求。'
    default:
      return '请稍后重试，或查看运行日志获取详细信息。'
  }
})

const message = computed(() => {
  const e = props.error
  if (!e) return ''
  if (typeof e === 'string') return e
  // Error 对象：优先用 message，但去掉可能的技术前缀（如 "DRIVER_ERROR: "）
  let m = e.message || ''
  const idx = m.indexOf(': ')
  if (idx > 0 && idx < 40) m = m.slice(idx + 2)
  return m
})

const canRetry = computed(() => RETRYABLE.has(kind.value) && !!props.onRetry)

function retry() {
  emit('retry')
  if (props.onRetry) props.onRetry()
}
</script>

<template>
  <div v-if="error" class="card error-state">
    <div class="error-icon" aria-hidden="true">
      <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor"
        stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
        <path d="M12 9v4M12 17h.01" />
        <path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z" />
      </svg>
    </div>
    <div class="error-body">
      <div class="error-title">{{ title }}</div>
      <div v-if="message" class="error-msg mono">{{ message }}</div>
      <div class="error-hint">{{ hint || defaultHint }}</div>
    </div>
    <button v-if="canRetry" class="btn btn-sm" @click="retry">🔄 重试</button>
    <slot name="extra" />
  </div>
</template>

<style scoped>
.error-state {
  display: flex; align-items: flex-start; gap: 12px;
  padding: 16px 18px; border-color: color-mix(in srgb, var(--danger) 30%, var(--border));
  background: var(--danger-soft);
}
.error-icon { color: var(--danger); flex-shrink: 0; margin-top: 1px; }
.error-body { flex: 1; min-width: 0; }
.error-title { font-size: 13.5px; font-weight: 600; color: var(--danger); }
.error-msg {
  margin-top: 5px; font-size: 12px; color: var(--text-2);
  overflow-wrap: anywhere; max-height: 120px; overflow: auto;
}
.error-hint { margin-top: 5px; font-size: 12.5px; color: var(--text-2); line-height: 1.5; }
</style>