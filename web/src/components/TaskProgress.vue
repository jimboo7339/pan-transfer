<script setup>
// 任务实时进度面板。
//
// 为什么需要它：POST /api/tasks/{id}/run 现在是异步的（立即返回 job_id），
// 否则多源链接 × 每批 100 文件的转存要跑几分钟到 30 分钟，而反代 60s 就断、
// 手机切后台就"无响应"、页面关掉再也看不到结果、也无法取消。
//
// 交互：
//   - 1s 轮询 job 句柄，展示阶段 + 百分比进度条 + 滚动日志；
//   - 跑完后把最终结果回调给父组件（刷新任务列表），并自动停止轮询；
//   - 失败/取消都能关闭并保留"查看记录"的入口。
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { jobApi, fmtTime } from '../api'

const props = defineProps({
  jobId: { type: [Number, String], default: 0 },
})
const emit = defineEmits(['finished', 'closed'])

const job = ref(null)
const error = ref('')
const polling = ref(false)
let timer = null

const isRunning = computed(() => !!job.value && job.value.status === 'running')
const progress = computed(() => {
  if (!job.value) return 0
  return Math.max(0, Math.min(100, job.value.progress ?? 0))
})
// elapsed 每秒钟重算。
//
// 为什么不用 computed 直接读 Date.now()：computed 的依赖收集只认响应式数据，
// Date.now() 不是响应式的，所以 computed 只在 job 变化时才重算——
// 结果是"已用 X 秒"在任务运行中一直不变，看起来像卡住了。
// 这里用一个每秒 tick 的 ref 驱动重算。
const now = ref(Date.now())
let tickTimer = null

const elapsed = computed(() => {
  if (!job.value?.started_at) return ''
  const start = new Date(job.value.started_at).getTime()
  const end = job.value.finished_at ? new Date(job.value.finished_at).getTime() : now.value
  const sec = Math.max(0, Math.round((end - start) / 1000))
  const m = Math.floor(sec / 60)
  const s = sec % 60
  return m > 0 ? `${m} 分 ${s} 秒` : `${s} 秒`
})

const statusBadge = computed(() => {
  const st = job.value?.status
  if (st === 'running') return { cls: 'badge-info', text: '运行中' }
  if (st === 'success') return { cls: 'badge-success', text: '成功' }
  if (st === 'partial') return { cls: 'badge-warn', text: '部分成功' }
  if (st === 'canceled') return { cls: 'badge-gray', text: '已取消' }
  return { cls: 'badge-danger', text: '失败' }
})

async function poll() {
  if (!props.jobId) return
  // 快照当前 jobId：await 期间 props.jobId 可能已变（watch 会 stop 并清空 job），
// 旧响应回来会写入 job 并 schedule 下一轮，导致显示的是上一个 job 的数据、
// cancel() 也会取消错对象。
  const id = props.jobId
  try {
    const j = await jobApi.get(id)
    if (id !== props.jobId) return
    job.value = j
    error.value = ''
    if (j.status === 'running') {
      schedule()
    } else {
      stop()
      // 终态：通知父组件刷新任务列表与记录
      emit('finished', j)
    }
  } catch (e) {
    if (id !== props.jobId) return
    // 句柄是内存态，重启即清空；这类错误不可重试，直接停止轮询。
    error.value = e.message || '无法获取运行状态'
    stop()
  }
}

function schedule() {
  stop()
  if (!props.jobId) return
  polling.value = true
  timer = setTimeout(poll, 1000)
}

function stop() {
  if (timer) { clearTimeout(timer); timer = null }
  polling.value = false
}

async function cancel() {
  try {
    await jobApi.cancel(props.jobId)
    await poll()
  } catch (e) {
    error.value = e.message
  }
}

function close() {
  stop()
  job.value = null
  emit('closed')
}

watch(() => props.jobId, (v) => {
  stop()
  job.value = null
  error.value = ''
  if (v) poll()
}, { immediate: true })

// 每秒 tick 一次驱动 elapsed 重算；卸载时清理。
onMounted(() => {
  tickTimer = setInterval(() => { now.value = Date.now() }, 1000)
})
onUnmounted(() => {
  stop()
  if (tickTimer) { clearInterval(tickTimer); tickTimer = null }
})
</script>

<template>
  <div v-if="jobId" class="card job-panel">
    <div class="flex" style="align-items: center; gap: 8px; flex-wrap: wrap">
      <span class="badge" :class="statusBadge.cls"><span class="dot"></span>{{ statusBadge.text }}</span>
      <strong class="job-name">{{ job?.task_name || '加载中…' }}</strong>
      <span v-if="job?.stage" class="muted" style="font-size: 12.5px">{{ job.stage }}</span>
      <span class="flex-1"></span>
      <span v-if="job" class="muted" style="font-size: 12px">已用 {{ elapsed }}</span>
      <button class="btn btn-icon btn-ghost" title="关闭" @click="close">✕</button>
    </div>

    <div v-if="error" class="probe-status fail">
      <span class="dot dot-fail"></span>{{ error }}
      <span class="probe-time">运行句柄为内存态，服务重启后会丢失</span>
    </div>

    <template v-else-if="job">
      <div class="progress-track">
        <div class="progress-bar" :class="{ indeterminate: isRunning && progress === 0 }"
          :style="{ width: (isRunning ? Math.max(progress, 3) : 100) + '%' }"></div>
      </div>
      <div class="flex" style="justify-content: space-between; margin-top: 6px">
        <span class="muted" style="font-size: 12px">
          {{ isRunning ? (progress > 0 ? `${progress}%` : '进行中…') : (job.status === 'success' ? '已完成' : job.status) }}
        </span>
        <span v-if="isRunning" class="flex" style="gap: 6px">
          <button class="btn btn-sm btn-danger btn-ghost" @click="cancel">取消运行</button>
        </span>
      </div>

      <div v-if="job.message || job.result?.message" class="task-last" style="margin-top: 10px">
        {{ job.result?.message || job.message }}
      </div>
      <pre v-if="job.log" class="log-tree job-log">{{ job.log }}</pre>
    </template>
  </div>
</template>

<style scoped>
.job-panel { padding: 14px 16px; }
.job-name { font-size: 14px; font-weight: 600; overflow-wrap: anywhere; }

.progress-track {
  margin-top: 12px;
  height: 6px;
  border-radius: 999px;
  background: var(--surface-2);
  border: 1px solid var(--border);
  overflow: hidden;
}
.progress-bar {
  height: 100%;
  background: var(--primary);
  border-radius: 999px;
  transition: width 0.3s ease;
}
/* 无法估算进度时用流动条纹表示"还在动"，避免 0% 卡住的错觉。 */
.progress-bar.indeterminate {
  background-image: linear-gradient(
    90deg,
    var(--primary) 0%,
    var(--primary) 40%,
    color-mix(in srgb, var(--primary) 45%, white) 60%,
    var(--primary) 80%
  );
  background-size: 200% 100%;
  animation: flow 1.2s linear infinite;
}
@keyframes flow {
  from { background-position: 200% 0; }
  to { background-position: 0 0; }
}
@media (prefers-reduced-motion: reduce) {
  .progress-bar.indeterminate { animation: none; }
}
.job-log {
  margin-top: 10px;
  max-height: 260px;
  overflow: auto;
}
</style>