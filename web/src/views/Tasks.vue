<script setup>
// 转存任务主页：任务卡片列表 + 新建/编辑/运行/暂停/删除。
import { ref, inject, onMounted } from 'vue'
import TaskEdit from '../components/TaskEdit.vue'
import TaskRuns from '../components/TaskRuns.vue'
import { taskApi, accountApi, fmtTime } from '../api'
import { cronToText } from '../cron'

const toast = inject('toast')

const tasks = ref([])
const accounts = ref([])
const loading = ref(true)
const editRef = ref(null)
const runsRef = ref(null)
const runningIds = ref(new Set())

async function reload() {
  try {
    const [ts, accs] = await Promise.all([taskApi.list(), accountApi.list()])
    tasks.value = ts || []
    accounts.value = accs || []
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    loading.value = false
  }
}
onMounted(reload)

function accountName(id) {
  const a = accounts.value.find(x => x.id === id)
  return a ? a.name : `账号#${id}`
}
function accountType(id) {
  const a = accounts.value.find(x => x.id === id)
  return a ? a.driver_type : ''
}

async function runTask(t) {
  runningIds.value.add(t.id)
  runningIds.value = new Set(runningIds.value)
  toast(`正在运行「${t.task_name}」…`)
  try {
    const res = await taskApi.run(t.id)
    const cls = res.status === 'success' ? 'success' : res.status === 'failed' ? 'error' : 'info'
    toast(`「${t.task_name}」${res.message}`, cls, 6000)
    await reload()
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    runningIds.value.delete(t.id)
    runningIds.value = new Set(runningIds.value)
  }
}

async function toggleStatus(t) {
  try {
    await taskApi.setStatus(t.id, t.status === 'paused' ? 'running' : 'paused')
    t.status = t.status === 'paused' ? 'running' : 'paused'
  } catch (e) {
    toast(e.message, 'error')
  }
}

async function removeTask(t) {
  if (!confirm(`确定删除任务「${t.task_name}」？运行与转存历史将一并删除。`)) return
  try {
    await taskApi.remove(t.id)
    toast('任务已删除', 'success')
    reload()
  } catch (e) {
    toast(e.message, 'error')
  }
}

function scheduleText(t) {
  const mode = t.run_mode || 'manual'
  if (mode === 'global') return '跟随全局'
  if (mode === 'custom') {
    // 优先显示 cron 表达式的人性化文本
    const cron = t.run_cron || (t.run_time ? (() => { const [h, m] = t.run_time.split(':'); return `${m||0} ${h||0} * * ${t.run_week||'*'}` })() : '')
    if (cron) {
      const human = cronToText(cron)
      return human === cron ? cron : `${human}（${cron}）`
    }
    return '自定义'
  }
  return '手动'
}

const statusBadge = (t) => {
  if (t.status === 'paused') return { cls: 'badge-gray', text: '已暂停' }
  if (t.last_run_status === 'success') return { cls: 'badge-success', text: '运行正常' }
  if (t.last_run_status === 'failed') return { cls: 'badge-danger', text: '上次失败' }
  if (t.last_run_status === 'partial') return { cls: 'badge-warn', text: '部分成功' }
  return { cls: 'badge-info', text: '待运行' }
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1 class="page-title">转存任务</h1>
        <div class="page-desc">追剧转存：定时扫描分享链接，新剧集自动转存、重命名、去重。</div>
      </div>
      <div class="page-actions">
        <button class="btn btn-primary" @click="editRef.open(null)">＋ 新建任务</button>
      </div>
    </div>

    <div v-if="loading" class="loading-block"><span class="spinner"></span> 加载中…</div>

    <div v-else-if="!tasks.length" class="empty card">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M4 17l4.5-7 3.5 5 3.5-8 4.5 10"/></svg>
      <div class="empty-title">还没有转存任务</div>
      <div class="empty-hint">新建任务 → 粘贴分享链接 → 选择保存目录，剩下的交给定时调度</div>
      <button class="btn btn-primary mt-8" @click="editRef.open(null)">＋ 创建第一个任务</button>
    </div>

    <div v-else class="grid-cards">
      <div v-for="t in tasks" :key="t.id" class="card task-card">
        <div class="task-head">
          <div class="task-name" @click="runsRef.open(t)" :title="'查看运行记录'">{{ t.task_name }}</div>
          <span class="badge" :class="statusBadge(t).cls"><span class="dot"></span>{{ statusBadge(t).text }}</span>
        </div>
        <div class="task-meta">
          <span class="badge" :class="t.status === 'paused' ? 'badge-gray' : 'badge-primary'">
            {{ t.status === 'paused' ? '⏸ 暂停' : '▶ 启用' }}
          </span>
          <span class="tag">{{ accountType(t.account_id) }} · {{ accountName(t.account_id) }}</span>
          <span v-if="t.links.length > 1" class="tag">多源 ×{{ t.links.length }}</span>
          <span v-if="t.ignore_extension" class="tag">忽略后缀</span>
        </div>
        <div class="task-info">
          <div class="task-info-row"><span class="k">保存路径</span><span class="v mono">{{ t.save_path }}</span></div>
          <div class="task-info-row"><span class="k">链接</span><span class="v mono" :title="t.links.join('\n')">{{ t.links[0] }}</span></div>
          <div class="task-info-row"><span class="k">调度</span>
            <span class="v">{{ scheduleText(t) }}</span>
          </div>
        </div>
        <div v-if="t.last_run_at" class="task-last">
          <span class="muted">{{ fmtTime(t.last_run_at) }}：</span>{{ t.last_run_message || '无' }}
        </div>
        <div class="task-foot">
          <button class="btn btn-sm btn-primary" :disabled="runningIds.has(t.id)" @click="runTask(t)">
            <span v-if="runningIds.has(t.id)" class="spinner" style="border-top-color: #fff"></span>
            <span v-else>▶ 运行</span>
          </button>
          <button class="btn btn-sm" @click="editRef.open(t)">编辑</button>
          <button class="btn btn-sm" @click="runsRef.open(t)">记录</button>
          <span class="spacer"></span>
          <button class="btn btn-sm btn-ghost" @click="toggleStatus(t)">{{ t.status === 'paused' ? '启用' : '暂停' }}</button>
          <button class="btn btn-sm btn-danger btn-ghost" @click="removeTask(t)">删除</button>
        </div>
      </div>
    </div>

    <TaskEdit ref="editRef" @saved="reload" />
    <TaskRuns ref="runsRef" />
  </div>
</template>
