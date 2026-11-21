<script setup>
// 运行日志：按任务查看最近 10 次转存日志（可展开日志树）。
import { ref, inject, onMounted, watch } from 'vue'
import { taskApi, fmtTime } from '../api'
import ErrorState from '../components/ErrorState.vue'

const toast = inject('toast')

const tasks = ref([])
const taskId = ref(0)
const runs = ref([])
const loading = ref(false)
const expanded = ref(new Set())
// 加载失败要能与「真的没有运行记录」区分开：
// 此前失败也落到同一个空状态，用户会以为"这个任务从没跑过"。
const loadError = ref(null)

async function loadTasks() {
  try {
    tasks.value = (await taskApi.list()) || []
    if (tasks.value.length && !taskId.value) taskId.value = tasks.value[0].id
  } catch (e) {
    toast(e.message, 'error')
  }
}

// loadRuns 加载当前任务的运行记录。
//
// 竞态防护：watch(taskId) 在快速切换任务时会并发触发多次。
// 早先没有序号守卫，任务 A 的日志会在任务 B 的界面里出现——
// 用户以为看到的是 B 的运行记录，实际是 A 的。
let runsSeq = 0

async function loadRuns() {
  const id = taskId.value
  if (!id) { runs.value = []; return }
  const seq = ++runsSeq
  loading.value = true
  loadError.value = null
  try {
    const list = (await taskApi.runs(id, 10)) || []
    if (seq !== runsSeq) return
    runs.value = list
    expanded.value = new Set()
  } catch (e) {
    if (seq !== runsSeq) return
    // 记下错误对象，让 ErrorState 决定文案与是否给重试。
    loadError.value = e
    runs.value = []
  } finally {
    // 只有最新一次请求能解除 loading，否则旧请求先返回会让加载态提前结束
    if (seq === runsSeq) loading.value = false
  }
}

function toggleExpand(id) {
  const s = new Set(expanded.value)
  s.has(id) ? s.delete(id) : s.add(id)
  expanded.value = s
}

watch(taskId, loadRuns)
onMounted(async () => { await loadTasks(); await loadRuns() })

const badge = (st) => ({
  'badge-success': st === 'success',
  'badge-danger': st === 'failed',
  'badge-warn': st === 'partial',
  'badge-gray': st === 'running' || st === 'skipped',
})
const taskName = (id) => tasks.value.find(t => t.id === id)?.task_name || `任务#${id}`
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1 class="page-title">运行日志</h1>
        <div class="page-desc">每个任务最近 10 次转存的执行日志，点击条目展开日志树。</div>
      </div>
      <div class="page-actions">
        <button class="btn" @click="loadRuns">🔄 刷新</button>
      </div>
    </div>

    <div v-if="!tasks.length" class="empty card">
      <div class="empty-title">还没有任务</div>
      <div class="empty-hint">先在「转存任务」页创建任务，运行后这里会出现日志</div>
    </div>

    <template v-else>
      <div class="field" style="max-width: 360px">
        <div class="field-label">选择任务</div>
        <select class="select" v-model="taskId">
          <option v-for="t in tasks" :key="t.id" :value="t.id">{{ t.task_name }}</option>
        </select>
      </div>

      <div v-if="loading" class="loading-block"><span class="spinner"></span> 加载中…</div>

      <ErrorState v-else-if="loadError" :error="loadError" :on-retry="loadRuns"
        hint="请确认服务正在运行，或该任务的运行记录已被清理（默认保留 90 天）。" />

      <div v-else-if="!runs.length" class="empty card">
        <div class="empty-title">「{{ taskName(taskId) }}」还没有运行记录</div>
        <div class="empty-hint">去任务页点「运行」，或等待定时触发</div>
      </div>

      <div v-else style="display: flex; flex-direction: column; gap: 10px">
        <div v-for="r in runs" :key="r.id" class="card" style="overflow: hidden">
          <div class="flex" style="padding: 12px 16px; cursor: pointer; flex-wrap: wrap; gap: 8px"
            @click="toggleExpand(r.id)">
            <span class="badge" :class="badge(r.status)"><span class="dot"></span>{{ r.status }}</span>
            <span class="muted" style="font-size: 12.5px">{{ fmtTime(r.started_at) }}</span>
            <span v-if="r.transfer_count" class="badge badge-primary">+{{ r.transfer_count }} 文件</span>
            <span class="flex-1"></span>
            <span class="muted ellipsis" style="font-size: 12px; max-width: 240px" :title="r.link">{{ r.link }}</span>
            <span class="muted" style="font-size: 12px">{{ expanded.has(r.id) ? '▾' : '▸' }}</span>
          </div>
          <div v-if="r.message" style="padding: 0 16px 10px; font-size: 12.5px; color: var(--text-2)">{{ r.message }}</div>
          <div v-if="expanded.has(r.id) && (r.tree_summary || r.log)" style="padding: 0 16px 14px">
            <pre v-if="r.tree_summary" class="log-tree">{{ r.tree_summary }}</pre>
            <details v-if="r.log" class="run-log-details" style="margin-top: 8px">
              <summary>📄 详细执行日志</summary>
              <pre class="log-tree" style="max-height: 360px">{{ r.log }}</pre>
            </details>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>
