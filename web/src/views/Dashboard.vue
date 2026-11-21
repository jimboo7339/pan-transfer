<script setup>
// 仪表盘：打开面板的第一屏。
//
// 为什么需要这一页：此前默认路由是任务列表，用户打开面板要先扫 N 张卡片的
// badge 才能回答"系统健康吗"。仪表盘把四个最高频的问题放在一屏：
//   1. 账号掉线了吗      → 账号健康卡
//   2. 今天转存了多少    → 数字 + 7 天趋势条
//   3. 有没有失败        → 告警列表 + 最近失败
//   4. 下次什么时候跑    → 调度卡
//
// 设计取舍：
//   - 不做成复杂图表库：这一页只需要"数字 + 一行趋势条"，
//     引 ECharts 会给内嵌前端加几百 KB，与"单二进制轻量"的定位冲突
//   - 告警放在最上面：坏消息优先，用户不需要滚动就能看到
//   - 每个告警都可点击跳到对应任务，减少"告警在上一屏、任务在下一屏"的来回
import { ref, inject, onMounted, computed } from 'vue'
import { useRouter } from 'vue-router'
import { dashboardApi } from '../api'
import { cronToText } from '../cron'

const toast = inject('toast')
const router = useRouter()

const data = ref(null)
const loading = ref(true)

async function reload() {
  loading.value = true
  try {
    data.value = await dashboardApi.get()
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    loading.value = false
  }
}
onMounted(reload)

const maxDay = computed(() => {
  const days = data.value?.transfers?.last_7_days || []
  return Math.max(1, ...days.map(d => d.count))
})

// 把 cron 转成中文；失败就原样显示表达式（后端已给一次，这里兜底）。
const scheduleText = computed(() => {
  const c = data.value?.schedule?.cron
  if (!c) return ''
  const human = cronToText(c)
  return human === c ? c : `${human}（${c}）`
})

// 跳到任务详情（可深链）。
//
// 仪表盘告警点进来直接落到那个任务上，而不是"跳到任务列表再自己找"——
// 后者在任务多的时候等于没跳。
function gotoTask(id) {
  if (id) router.push(`/tasks/${id}`)
}

// 告警图标：error 用红色感叹号，warn 用黄色三角。
function alertIcon(level) {
  return level === 'error' ? '⛔' : '⚠️'
}

const fmt = (t) => (t ? new Date(t).toLocaleString('zh-CN', { hour12: false }) : '—')
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1 class="page-title">仪表盘</h1>
        <div class="page-desc">系统健康总览：账号状态、今日转存、失败告警、下次调度。</div>
      </div>
      <div class="page-actions">
        <button class="btn" @click="reload">🔄 刷新</button>
      </div>
    </div>

    <div v-if="loading" class="loading-block"><span class="spinner"></span> 加载中…</div>

    <template v-else-if="data">
      <!-- 告警优先：坏消息不需要滚动就能看到 -->
      <div v-if="data.alerts && data.alerts.length" class="dash-alerts">
        <div v-for="(a, i) in data.alerts" :key="i"
          class="dash-alert" :class="'dash-alert-' + a.level"
          :style="{ cursor: a.task_id ? 'pointer' : 'default' }"
          @click="gotoTask(a.task_id)">
          <span class="dash-alert-icon">{{ alertIcon(a.level) }}</span>
          <div class="dash-alert-body">
            <div class="dash-alert-title">{{ a.title }}</div>
            <div v-if="a.detail" class="dash-alert-detail">{{ a.detail }}</div>
          </div>
        </div>
      </div>

      <!-- 指标卡 -->
      <div class="dash-grid">
        <div class="card dash-card">
          <div class="dash-card-label">今日转存</div>
          <div class="dash-card-value">{{ data.transfers.today_count }}<span class="dash-unit">个</span></div>
          <div class="dash-card-sub">跑了 {{ data.transfers.today_runs }} 次</div>
          <!-- 7 天趋势条：只用高度表达，不引图表库 -->
          <div class="dash-bars">
            <div v-for="d in data.transfers.last_7_days" :key="d.date"
              class="dash-bar-item" :title="`${d.date}：${d.count} 个`">
              <div class="dash-bar-track">
                <div class="dash-bar-fill"
                  :style="{ height: Math.round((d.count / maxDay) * 100) + '%' }"></div>
              </div>
              <span class="dash-bar-label">{{ d.date.slice(5) }}</span>
            </div>
          </div>
        </div>

        <div class="card dash-card">
          <div class="dash-card-label">网盘账号</div>
          <div class="dash-card-value">{{ data.accounts.active }}<span class="dash-unit">/ {{ data.accounts.total }} 启用</span></div>
          <div class="dash-card-sub">
            <template v-if="data.accounts.total === 0">还没添加账号</template>
            <template v-else-if="data.accounts.active === 0" class="text-danger">全部已停用</template>
            <template v-else>累计转存 {{ data.transfers.total_saved }} 个文件</template>
          </div>
          <button class="btn btn-sm dash-card-action" @click="router.push('/accounts')">
            管理账号 →
          </button>
        </div>

        <div class="card dash-card">
          <div class="dash-card-label">调度</div>
          <div class="dash-card-value dash-card-value-sm">
            {{ data.schedule.enabled ? '开启中' : '已关闭' }}
          </div>
          <div class="dash-card-sub">
            <template v-if="data.schedule.enabled && scheduleText">{{ scheduleText }}</template>
            <template v-else-if="data.schedule.enabled">未配置 cron</template>
            <template v-else>所有任务只能手动运行</template>
          </div>
          <div v-if="data.schedule.next_run" class="dash-card-sub">
            下次：{{ fmt(data.schedule.next_run) }}
          </div>
        </div>

        <div class="card dash-card">
          <div class="dash-card-label">任务</div>
          <div class="dash-card-value">{{ data.tasks.total }}</div>
          <div class="dash-card-sub">
            {{ data.tasks.running }} 运行中 · {{ data.tasks.paused }} 已暂停
          </div>
          <div v-if="data.running.count > 0" class="dash-card-sub text-success">
            <span class="spinner spinner-sm"></span> {{ data.running.count }} 个正在转存
          </div>
        </div>
      </div>

      <!-- 最近失败 -->
      <div v-if="data.recent_failures && data.recent_failures.length" class="card card-pad">
        <div class="card-title">最近失败</div>
        <div class="table-wrap">
          <table class="table">
            <thead>
              <tr>
                <th style="width: 150px">时间</th>
                <th>任务</th>
                <th>原因</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(f, i) in data.recent_failures" :key="i"
                style="cursor: pointer" @click="gotoTask(f.task_id)">
                <td class="num muted">{{ fmt(f.failed_at) }}</td>
                <td>{{ f.task_name || ('#' + f.task_id) }}</td>
                <td class="muted">{{ f.message }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>

    <div v-else class="empty card">
      <div class="empty-title">无法加载仪表盘</div>
      <div class="empty-hint">请确认服务正在运行，然后点「刷新」重试。</div>
    </div>
  </div>
</template>

<style scoped>
.dash-alerts { display: flex; flex-direction: column; gap: 8px; margin-bottom: 16px; }
.dash-alert {
  display: flex; align-items: flex-start; gap: 10px;
  padding: 12px 14px; border-radius: var(--radius);
  border: 1px solid var(--border); background: var(--surface);
  transition: box-shadow 0.15s;
}
.dash-alert:hover { box-shadow: var(--shadow-md); }
.dash-alert-icon { font-size: 15px; line-height: 1.4; flex-shrink: 0; }
.dash-alert-title { font-size: 13.5px; font-weight: 600; }
.dash-alert-detail {
  font-size: 12.5px; color: var(--text-2); margin-top: 3px; line-height: 1.5;
  overflow-wrap: anywhere;
}
.dash-alert-error { background: var(--danger-soft); border-color: color-mix(in srgb, var(--danger) 35%, var(--border)); }
.dash-alert-error .dash-alert-title { color: var(--danger); }
.dash-alert-warn { background: var(--warn-soft); border-color: color-mix(in srgb, var(--warn) 35%, var(--border)); }
.dash-alert-warn .dash-alert-title { color: var(--warn); }

.dash-grid {
  display: grid; gap: 14px; margin-bottom: 16px;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
}
.dash-card { padding: 16px 18px; }
.dash-card-label { font-size: 12.5px; color: var(--text-3); font-weight: 500; }
.dash-card-value {
  font-size: 30px; font-weight: 700; line-height: 1.2; margin-top: 6px;
  font-variant-numeric: tabular-nums;
}
.dash-card-value-sm { font-size: 22px; }
.dash-unit { font-size: 13px; font-weight: 500; color: var(--text-3); margin-left: 4px; }
.dash-card-sub { font-size: 12.5px; color: var(--text-2); margin-top: 5px; line-height: 1.5; }
.dash-card-action { margin-top: 10px; }

/* 7 天趋势条 */
.dash-bars {
  display: flex; align-items: flex-end; gap: 4px;
  margin-top: 12px; height: 48px;
}
.dash-bar-item { flex: 1; display: flex; flex-direction: column; align-items: center; gap: 4px; }
.dash-bar-track {
  width: 100%; height: 36px; border-radius: 4px 4px 0 0;
  background: var(--surface-2); display: flex; align-items: flex-end;
}
.dash-bar-fill {
  width: 100%; border-radius: 4px 4px 0 0; background: var(--primary);
  min-height: 2px; transition: height 0.3s ease;
}
.dash-bar-label { font-size: 10px; color: var(--text-3); white-space: nowrap; }
@media (prefers-reduced-motion: reduce) {
  .dash-bar-fill { transition: none; }
}
</style>