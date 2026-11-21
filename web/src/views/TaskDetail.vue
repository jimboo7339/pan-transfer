<script setup>
// 任务详情页（/tasks/:id）。
//
// 为什么需要独立路由：
//   - 可分享：把某个任务的运行记录发给别人（或自己在另一台设备上打开）；
//   - 可深链：从仪表盘告警、搜索结果、通知消息直接跳到具体任务；
//   - 刷新不丢上下文：此前点任务名只弹一个弹层，刷新就回到列表顶部。
//
// 页面结构（按用户的阅读顺序）：
//   ① 头部：任务名 + 状态 + 快捷操作（运行/编辑/暂停）
//   ② 配置摘要：账号、保存路径、调度、命名规则——折叠展示，默认只显示关键信息
//   ③ 运行记录：时间线（带日志展开）
//   ④ 已转存文件：分页表格
import { ref, inject, onMounted, computed, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { taskApi, accountApi, categoryApi, fmtTime } from '../api'
import { cronToText } from '../cron'

const route = useRoute()
const router = useRouter()
const toast = inject('toast')
const confirmDialog = inject('confirmDialog')

const taskId = computed(() => Number(route.params.id) || 0)
const task = ref(null)
const accounts = ref([])
const categories = ref([])
const runs = ref([])
const saved = ref([])
const savedTotal = ref(0)
const loading = ref(true)
const running = ref(false)
const runsLimit = ref(20)
const tab = ref('runs')
const expanded = ref(new Set())
const showConfig = ref(false)

const DRIVER_NAMES = { quark: '夸克网盘', guangya: '光鸭云盘' }
const driverLabel = (t) => DRIVER_NAMES[t] || t || '未知'

const accountName = computed(() => {
  const a = accounts.value.find(x => x.id === task.value?.account_id)
  return a ? a.name : (task.value ? `#${task.value.account_id}` : '')
})
// 账号的驱动类型，用于显示"夸克网盘"这种中文名。
const accountType = computed(() => {
  const a = accounts.value.find(x => x.id === task.value?.account_id)
  return a ? a.driver_type : ''
})
const categoryName = computed(() => {
  const c = categories.value.find(x => x.id === task.value?.category_id)
  return c ? c.name : ''
})

const scheduleText = computed(() => {
  const t = task.value
  if (!t) return ''
  const mode = t.run_mode || 'manual'
  if (mode === 'global') return '跟随全局'
  const cron = t.run_cron
  if (mode === 'custom' && cron) {
    const human = cronToText(cron)
    return human === cron ? cron : `${human}（${cron}）`
  }
  if (mode === 'custom' && t.run_time) return `每天 ${t.run_time}`
  return '手动'
})

const statusBadge = computed(() => {
  const t = task.value
  if (!t) return { cls: 'badge-gray', text: '' }
  if (t.status === 'paused') return { cls: 'badge-gray', text: '已暂停' }
  if (t.last_run_status === 'success') return { cls: 'badge-success', text: '运行正常' }
  if (t.last_run_status === 'failed') return { cls: 'badge-danger', text: '上次失败' }
  if (t.last_run_status === 'partial') return { cls: 'badge-warn', text: '部分成功' }
  return { cls: 'badge-info', text: '待运行' }
})

async function loadAll() {
  if (!taskId.value) return
  loading.value = true
  try {
    const [tasks, accs, cats] = await Promise.all([
      taskApi.list(),
      accountApi.list().catch(() => []),
      categoryApi.list().catch(() => []),
    ])
    accounts.value = accs || []
    categories.value = cats || []
    task.value = (tasks || []).find(t => t.id === taskId.value) || null
    if (!task.value) {
      toast('任务不存在或已被删除', 'error', 5000)
      return
    }
    await loadRuns()
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    loading.value = false
  }
}

async function loadRuns() {
  if (!taskId.value) return
  try {
    runs.value = (await taskApi.runs(taskId.value, runsLimit.value)) || []
    expanded.value = new Set()
  } catch (e) {
    toast(e.message, 'error')
  }
}

async function loadSaved() {
  try {
    saved.value = (await taskApi.savedNames(taskId.value)) || []
    const c = await taskApi.savedCount(taskId.value)
    savedTotal.value = c?.count ?? saved.value.length
  } catch (e) {
    toast(e.message, 'error')
  }
}

function switchTab(t) {
  tab.value = t
  if (t === 'saved') loadSaved()
}

function toggleRun(id) {
  const s = new Set(expanded.value)
  s.has(id) ? s.delete(id) : s.add(id)
  expanded.value = s
}

async function runNow() {
  running.value = true
  try {
    const res = await taskApi.run(taskId.value)
    if (res && res.job_id) {
      toast(`「${task.value.task_name}」已开始运行`, 'info')
      // 跳回任务页由 TaskProgress 展示实时进度（这里不重复实现轮询）
      router.push('/tasks')
    } else {
      toast(`「${task.value.task_name}」${res.message}`, 'success', 6000)
      await loadAll()
    }
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    running.value = false
  }
}

async function toggleStatus() {
  const t = task.value
  const next = t.status === 'paused' ? 'running' : 'paused'
  try {
    await taskApi.setStatus(t.id, next)
    t.status = next
    toast(next === 'paused' ? '已暂停' : '已启用', 'success')
  } catch (e) {
    toast(e.message, 'error')
  }
}

async function removeTask() {
  const t = task.value
  const ok = await confirmDialog({
    title: '删除转存任务',
    body: `确定删除任务「${t.task_name}」？`,
    warn: '该任务的运行记录与转存历史将一并删除，此操作不可撤销。',
    confirmText: '删除',
    variant: 'danger',
    requireText: t.task_name,
  })
  if (!ok) return
  try {
    await taskApi.remove(t.id)
    toast('任务已删除', 'success')
    router.push('/tasks')
  } catch (e) {
    toast(e.message, 'error')
  }
}

onMounted(loadAll)
watch(taskId, loadAll)
watch(runsLimit, loadRuns)

const runBadge = (st) => ({
  'badge-success': st === 'success',
  'badge-danger': st === 'failed',
  'badge-warn': st === 'partial',
  'badge-gray': st === 'running' || st === 'skipped',
})
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <button class="btn btn-sm btn-ghost" @click="router.push('/tasks')">← 返回任务列表</button>
        <h1 class="page-title" style="margin-top: 6px">
          {{ task?.task_name || (loading ? '加载中…' : '任务不存在') }}
        </h1>
        <div class="page-desc">
          任务详情 · 可分享此页链接（#{{ taskId }}）
        </div>
      </div>
      <div v-if="task" class="page-actions">
        <button class="btn btn-primary" :disabled="running" @click="runNow">
          <span v-if="running" class="spinner" style="border-top-color:#fff"></span>
          <span v-else>▶ 运行</span>
        </button>
        <button class="btn" @click="toggleStatus">
          {{ task.status === 'paused' ? '启用' : '暂停' }}
        </button>
        <button class="btn btn-danger btn-ghost" @click="removeTask">删除</button>
      </div>
    </div>

    <div v-if="loading" class="loading-block"><span class="spinner"></span> 加载中…</div>

    <div v-else-if="!task" class="empty card">
      <div class="empty-title">任务不存在</div>
      <div class="empty-hint">它可能已被删除，或链接里的 ID 有误。</div>
      <button class="btn btn-primary mt-8" @click="router.push('/tasks')">返回任务列表</button>
    </div>

    <template v-else>
      <!-- 状态 + 关键元信息 -->
      <div class="card card-pad mb-16">
        <div class="flex" style="align-items: center; gap: 8px; flex-wrap: wrap">
          <span class="badge" :class="statusBadge.cls"><span class="dot"></span>{{ statusBadge.text }}</span>
          <span class="tag">
            {{ driverLabel(accountType) }} · {{ accountName }}
          </span>
          <span v-if="categoryName" class="tag">📁 {{ categoryName }}</span>
          <span class="flex-1"></span>
          <span class="muted" style="font-size: 12.5px">调度：{{ scheduleText }}</span>
        </div>
        <div v-if="task.last_run_at" class="task-last" style="margin-top: 10px">
          <span class="muted">{{ fmtTime(task.last_run_at) }}：</span>{{ task.last_run_message || '无' }}
        </div>
      </div>

      <!-- 配置摘要：折叠 -->
      <div class="card card-pad mb-16">
        <div class="flex" style="cursor: pointer" @click="showConfig = !showConfig">
          <div class="card-title" style="margin-bottom: 0">配置</div>
          <span class="flex-1"></span>
          <span class="muted" style="font-size: 12.5px">{{ showConfig ? '收起 ▾' : '展开 ▸' }}</span>
        </div>
        <div v-if="showConfig" class="task-info" style="margin-top: 12px">
          <div class="task-info-row"><span class="k">保存路径</span><span class="v mono">{{ task.save_path || '—' }}</span></div>
          <div class="task-info-row"><span class="k">分享链接</span>
            <span class="v mono" :title="(task.links || []).join('\n')">{{ (task.links || [])[0] || '—' }}</span>
          </div>
          <div v-if="(task.links || []).length > 1" class="task-info-row">
            <span class="k">多源</span><span class="v">{{ task.links.length }} 条（按序轮换）</span>
          </div>
          <div class="task-info-row"><span class="k">匹配正则</span><span class="v mono">{{ task.pattern || '（空 = 全量转存）' }}</span></div>
          <div class="task-info-row"><span class="k">替换模板</span><span class="v mono">{{ task.replace || '—' }}</span></div>
          <div class="task-info-row"><span class="k">忽略后缀</span><span class="v">{{ task.ignore_extension ? '开启（mp4/mkv 有一个即视为已有）' : '关闭' }}</span></div>
          <div v-if="task.update_subdir" class="task-info-row">
            <span class="k">子目录同步</span><span class="v mono">{{ task.update_subdir }}</span>
          </div>
          <div v-if="task.start_fid" class="task-info-row">
            <span class="k">起始 fid</span><span class="v mono">{{ task.start_fid }}</span>
          </div>
        </div>
      </div>

      <!-- 运行记录 / 已转存文件 -->
      <div class="tabs">
        <button class="tab" :class="{ active: tab === 'runs' }" @click="switchTab('runs')">运行记录</button>
        <button class="tab" :class="{ active: tab === 'saved' }" @click="switchTab('saved')">已转存文件</button>
      </div>

      <template v-if="tab === 'runs'">
        <div v-if="!runs.length" class="empty card">
          <div class="empty-title">还没有运行记录</div>
          <div class="empty-hint">点右上角「运行」立即执行一次，或等定时触发</div>
        </div>
        <div v-else class="flex-col">
          <div v-for="r in runs" :key="r.id" class="card">
            <div class="flex" style="padding: 12px 16px; cursor: pointer; flex-wrap: wrap; gap: 8px"
              @click="toggleRun(r.id)">
              <span class="badge" :class="runBadge(r.status)"><span class="dot"></span>{{ r.status }}</span>
              <span class="muted" style="font-size: 12.5px">{{ fmtTime(r.started_at) }}</span>
              <span v-if="r.transfer_count" class="badge badge-primary">+{{ r.transfer_count }} 文件</span>
              <span class="flex-1"></span>
              <span class="muted ellipsis" style="font-size: 12px; max-width: 200px" :title="r.link">{{ r.link }}</span>
              <span class="muted" style="font-size: 12px">{{ expanded.has(r.id) ? '▾' : '▸' }}</span>
            </div>
            <div v-if="r.message" style="padding: 0 16px 10px; font-size: 12.5px; color: var(--text-2)">
              {{ r.message }}
            </div>
            <div v-if="expanded.has(r.id)" style="padding: 0 16px 14px">
              <pre v-if="r.tree_summary" class="log-tree">{{ r.tree_summary }}</pre>
              <details v-if="r.log" class="run-log-details" style="margin-top: 8px">
                <summary>📄 详细执行日志</summary>
                <pre class="log-tree" style="max-height: 320px">{{ r.log }}</pre>
              </details>
            </div>
          </div>
        </div>
        <div class="flex mt-8" style="justify-content: center">
          <button class="btn btn-sm" @click="runsLimit += 20">加载更多（当前 {{ runs.length }} 条）</button>
        </div>
      </template>

      <template v-else>
        <div class="flex mb-8">
          <span class="muted" style="font-size: 12.5px">
            共 {{ savedTotal }} 条（列表最多显示 {{ saved.length }} 条）
          </span>
        </div>
        <div v-if="!saved.length" class="empty card">
          <div class="empty-title">暂无转存记录</div>
          <div class="empty-hint">成功转存的文件会记录在此，用于按名跳过重复转存</div>
        </div>
        <div v-else class="table-wrap">
          <table class="table">
            <thead>
              <tr><th>文件名</th><th style="width: 160px">转存时间</th></tr>
            </thead>
            <tbody>
              <tr v-for="s in saved" :key="s.id">
                <td class="ellipsis" :title="s.file_name">{{ s.file_name }}</td>
                <td class="num muted">{{ fmtTime(s.saved_at) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </template>
    </template>
  </div>
</template>

<style scoped>
.flex-col { display: flex; flex-direction: column; gap: 10px; }
</style>