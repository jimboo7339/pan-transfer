<script setup>
// 转存任务主页：上下两块布局（运行中 / 已暂停）+ 筛选工具栏 + 新建/编辑/运行/暂停/删除。
import { ref, inject, onMounted, computed } from 'vue'
import TaskEdit from '../components/TaskEdit.vue'
import TaskRuns from '../components/TaskRuns.vue'
import { taskApi, accountApi, categoryApi, fmtTime } from '../api'
import { cronToText } from '../cron'

const toast = inject('toast')
const confirmDialog = inject('confirmDialog')

const tasks = ref([])
const accounts = ref([])
const categories = ref([])
const loading = ref(true)
const editRef = ref(null)
const runsRef = ref(null)
const runningIds = ref(new Set())

// 筛选条件
const keyword = ref('')
const filterCategory = ref('') // '' = 全部；任务分类 id
const filterDriver = ref('')   // '' = 全部；driver_type

// 网盘类型标识 -> 中文名；未知类型原样显示
const DRIVER_NAMES = { quark: '夸克网盘', guangya: '光鸭云盘' }
const driverLabel = (type) => DRIVER_NAMES[type] || type || '未知'

const catName = (id) => categories.value.find(c => c.id === id)?.name || ''

async function reload() {
  try {
    const [ts, accs, cats] = await Promise.all([taskApi.list(), accountApi.list(), categoryApi.list().catch(() => [])])
    tasks.value = ts || []
    accounts.value = accs || []
    categories.value = cats || []
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

// ---------- 筛选 ----------
// 当前任务列表里出现过的网盘类型，用于动态生成下拉项
const driverOptions = computed(() => {
  const set = new Set(tasks.value.map(t => accountType(t.account_id)).filter(Boolean))
  return [...set]
})

function matchFilter(t) {
  if (filterCategory.value !== '') {
    if (Number(filterCategory.value) !== t.category_id) return false
  }
  if (filterDriver.value && accountType(t.account_id) !== filterDriver.value) return false
  const kw = keyword.value.trim().toLowerCase()
  if (kw) {
    const hay = [t.task_name, accountName(t.account_id), t.save_path, (t.links || []).join(' ')]
      .filter(Boolean).join(' ').toLowerCase()
    if (!hay.includes(kw)) return false
  }
  return true
}

const filteredTasks = computed(() => tasks.value.filter(matchFilter))
const runningTasks = computed(() => filteredTasks.value.filter(t => t.status !== 'paused'))
const pausedTasks = computed(() => filteredTasks.value.filter(t => t.status === 'paused'))
const hasFilter = computed(() => !!(keyword.value.trim() || filterCategory.value !== '' || filterDriver.value))

function resetFilters() {
  keyword.value = ''
  filterCategory.value = ''
  filterDriver.value = ''
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

// 删除任务：破坏性且不可逆（运行与转存历史一并删除），
// 因此用统一确认弹层 + 二次输入任务名，避免误触。
async function removeTask(t) {
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

    <template v-else>
      <!-- 筛选工具栏 -->
      <div class="filter-bar card">
        <div class="filter-item filter-search">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><circle cx="11" cy="11" r="7"/><path d="M20 20l-3.5-3.5"/></svg>
          <input v-model="keyword" class="input" type="text" placeholder="搜索任务名 / 账号 / 保存路径 / 链接" />
        </div>
        <div class="filter-item">
          <span class="filter-label">任务分类</span>
          <select v-model="filterCategory" class="select">
            <option value="">全部分类</option>
            <option value="0">未分类</option>
            <option v-for="c in categories" :key="c.id" :value="String(c.id)">{{ c.name }}</option>
          </select>
        </div>
        <div class="filter-item">
          <span class="filter-label">网盘类型</span>
          <select v-model="filterDriver" class="select">
            <option value="">全部网盘</option>
            <option v-for="d in driverOptions" :key="d" :value="d">{{ driverLabel(d) }}</option>
          </select>
        </div>
        <button v-if="hasFilter" class="btn btn-sm btn-ghost filter-reset" @click="resetFilters">清空筛选</button>
      </div>

      <!-- 筛选后无结果 -->
      <div v-if="!filteredTasks.length" class="empty card">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M4 6h16M7 12h10M10 18h4"/></svg>
        <div class="empty-title">没有符合筛选条件的任务</div>
        <div class="empty-hint">试试换个分类 / 网盘类型，或清空筛选条件</div>
        <button class="btn btn-sm mt-8" @click="resetFilters">清空筛选</button>
      </div>

      <template v-else>
        <!-- 上：运行中的任务 -->
        <section class="task-section">
          <div class="section-head">
            <span class="section-dot dot-running"></span>
            <h2 class="section-title">运行中</h2>
            <span class="section-count">{{ runningTasks.length }}</span>
          </div>
          <div v-if="runningTasks.length" class="grid-cards">
            <div v-for="t in runningTasks" :key="t.id" class="card task-card">
              <div class="task-head">
                <div class="task-name" @click="runsRef.open(t)" :title="'查看运行记录'">{{ t.task_name }}</div>
                <span class="badge" :class="statusBadge(t).cls"><span class="dot"></span>{{ statusBadge(t).text }}</span>
              </div>
              <div class="task-meta">
                <span class="badge" :class="t.status === 'paused' ? 'badge-gray' : 'badge-primary'">
                  {{ t.status === 'paused' ? '⏸ 暂停' : '▶ 启用' }}
                </span>
                <span class="tag">{{ driverLabel(accountType(t.account_id)) }} · {{ accountName(t.account_id) }}</span>
                <span v-if="t.links.length > 1" class="tag">多源 ×{{ t.links.length }}</span>
                <span v-if="t.category_id" class="tag" :title="'分类回调：' + catName(t.category_id)">📁 {{ catName(t.category_id) }}</span>
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
          <div v-else class="section-empty">暂无运行中的任务</div>
        </section>

        <!-- 下：已暂停的任务 -->
        <section class="task-section">
          <div class="section-head">
            <span class="section-dot dot-paused"></span>
            <h2 class="section-title">已暂停</h2>
            <span class="section-count">{{ pausedTasks.length }}</span>
          </div>
          <div v-if="pausedTasks.length" class="grid-cards">
            <div v-for="t in pausedTasks" :key="t.id" class="card task-card task-card-paused">
              <div class="task-head">
                <div class="task-name" @click="runsRef.open(t)" :title="'查看运行记录'">{{ t.task_name }}</div>
                <span class="badge" :class="statusBadge(t).cls"><span class="dot"></span>{{ statusBadge(t).text }}</span>
              </div>
              <div class="task-meta">
                <span class="badge" :class="t.status === 'paused' ? 'badge-gray' : 'badge-primary'">
                  {{ t.status === 'paused' ? '⏸ 暂停' : '▶ 启用' }}
                </span>
                <span class="tag">{{ driverLabel(accountType(t.account_id)) }} · {{ accountName(t.account_id) }}</span>
                <span v-if="t.links.length > 1" class="tag">多源 ×{{ t.links.length }}</span>
                <span v-if="t.category_id" class="tag" :title="'分类回调：' + catName(t.category_id)">📁 {{ catName(t.category_id) }}</span>
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
          <div v-else class="section-empty">暂无已暂停的任务</div>
        </section>
      </template>
    </template>

    <TaskEdit ref="editRef" @saved="reload" />
    <TaskRuns ref="runsRef" />
  </div>
</template>
