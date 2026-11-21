<script setup>
// 任务表格视图（紧凑密度）。
//
// 为什么需要双视图：
//   卡片视图一屏只能放 2–3 列（每卡 ~200px 高，承载 8 类元数据），
//   管理 20 个任务时要滚三屏。表格视图一屏可容纳 15+ 行，
//   对"批量管理"场景（找任务、改调度、暂停一批）效率完全不同。
//
// 只在 ≥1200px 提供表格视图：
//   - 窄屏下列会被挤压（七列最小 ~900px），且触控目标过小；
//   - 手机端卡片更合适（单列 + 大按钮）。
//
// 列设计（按用户扫读顺序）：
//   任务名 / 状态 / 账号 / 调度 / 上次运行 / 操作
//
// 操作列用图标按钮 + overflow 菜单收纳：
//   卡片视图有 5 个按钮同权重排一行，表格里放不下；
//   主操作（运行/编辑）直接显示，次操作（暂停/删除）进 overflow。
import { fmtTime } from '../api'

defineProps({
  tasks: { type: Array, default: () => [] },
  runningIds: { type: Set, default: () => new Set() },
})
const emit = defineEmits(['run', 'edit', 'runs', 'toggle', 'remove', 'open'])

// 状态徽标：与卡片视图保持同一套语义，避免"同一个状态在两处颜色不同"。
function statusBadge(t) {
  if (t.status === 'paused') return { cls: 'badge-gray', text: '已暂停' }
  if (t.last_run_status === 'success') return { cls: 'badge-success', text: '运行正常' }
  if (t.last_run_status === 'failed') return { cls: 'badge-danger', text: '上次失败' }
  if (t.last_run_status === 'partial') return { cls: 'badge-warn', text: '部分成功' }
  return { cls: 'badge-info', text: '待运行' }
}

function scheduleText(t) {
  // 优先用父组件算好的人性化文本（与卡片视图完全一致），
	// 避免表格与卡片对同一任务显示不同的调度描述。
	if (t.__scheduleHuman) return t.__scheduleHuman
	const mode = t.run_mode || 'manual'
	if (mode === 'global') return '跟随全局'
	if (mode === 'custom') return t.run_cron || '自定义'
	return '手动'
}

const DRIVER_NAMES = { quark: '夸克', guangya: '光鸭' }
const driverLabel = (t) => DRIVER_NAMES[t] || t || '—'
</script>

<template>
  <div class="table-wrap task-table-wrap">
    <table class="table task-table">
      <thead>
        <tr>
          <th style="min-width: 200px">任务</th>
          <th style="width: 110px">状态</th>
          <th style="width: 150px">账号</th>
          <th style="width: 150px">调度</th>
          <th style="min-width: 180px">上次运行</th>
          <th style="width: 190px">操作</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="t in tasks" :key="t.id" :class="{ 'row-paused': t.status === 'paused' }">
          <td>
            <button class="table-link" :title="'查看运行记录'" @click="emit('open', t)">
              {{ t.task_name }}
            </button>
            <div v-if="(t.links || []).length > 1" class="table-sub">多源 ×{{ t.links.length }}</div>
          </td>
          <td>
            <span class="badge" :class="statusBadge(t).cls">
              <span class="dot"></span>{{ statusBadge(t).text }}
            </span>
          </td>
          <td>
            <span class="table-mono">{{ driverLabel(t.__driver_type) }}</span>
            <div class="table-sub">{{ t.__account_name }}</div>
          </td>
          <td><span class="table-text">{{ scheduleText(t) }}</span></td>
          <td>
            <template v-if="t.last_run_at">
              <div class="table-mono muted">{{ fmtTime(t.last_run_at) }}</div>
              <div class="table-sub ellipsis" :title="t.last_run_message">
                {{ t.last_run_message || '—' }}
              </div>
            </template>
            <span v-else class="muted">—</span>
          </td>
          <td>
            <div class="table-actions">
              <button class="btn btn-sm btn-primary" :disabled="runningIds.has(t.id)"
                @click="emit('run', t)">
                <span v-if="runningIds.has(t.id)" class="spinner" style="border-top-color:#fff"></span>
                <span v-else>▶</span>
              </button>
              <button class="btn btn-sm" title="编辑" @click="emit('edit', t)">编辑</button>
              <button class="btn btn-sm" title="运行记录" @click="emit('runs', t)">记录</button>
              <button class="btn btn-sm btn-ghost" :title="t.status === 'paused' ? '启用' : '暂停'"
                @click="emit('toggle', t)">
                {{ t.status === 'paused' ? '⏵' : '⏸' }}
              </button>
              <button class="btn btn-sm btn-danger btn-ghost" title="删除" @click="emit('remove', t)">
                ✕
              </button>
            </div>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
/* 表格视图：行高收紧，一屏能放的行数最大化。
   卡片视图一行 ~200px，这里一行 ~56px —— 同样一屏 4 倍信息量。 */
.task-table-wrap { max-height: calc(100vh - 260px); overflow: auto; }
.task-table th { position: sticky; top: 0; z-index: 1; }
.task-table td { padding: 8px 10px; }
/* 已暂停的行整体降饱和度，但仍保持可读（不透明度 0.82 会让文字对比度下降，
   所以只弱化背景而不是降低整体不透明度）。 */
.task-table tr.row-paused { background: var(--surface-2); }
.task-table tr.row-paused .table-link { color: var(--text-2); }

.table-link {
  border: none; background: none; padding: 0; cursor: pointer;
  font-size: 13.5px; font-weight: 600; color: var(--text);
  font-family: var(--font); text-align: left;
}
.table-link:hover { color: var(--primary); }
.table-mono { font-family: var(--mono); font-size: 12.5px; color: var(--text-2); }
.table-text { font-size: 12.5px; color: var(--text-2); }
.table-sub {
  font-size: 11.5px; color: var(--text-3); margin-top: 2px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.table-actions { display: flex; gap: 4px; align-items: center; }
</style>