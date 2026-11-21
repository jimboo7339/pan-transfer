<script setup>
// 批量操作浮动条。
//
// 交互要点（这是批量操作能否被用户接受的关键）：
//   1. 平时完全隐藏，不占任何视觉空间；勾选后才浮现在底部中央；
//   2. 显示「已选 N 个」，并提供「全选 / 取消选择」，避免用户数不清选了几个；
//   3. 破坏性动作（删除）用红色，且必须走二次确认弹层；
//   4. 执行期间禁用按钮并显示进度（"处理中 3/10"），
//      而不是让用户以为卡住了；
//   5. 完成后展示结果摘要（成功/失败条数），失败项可展开看原因——
//      批量操作最容易出现的就是"删了 8 个，漏了 2 个不知道为什么"。
import { ref, computed } from 'vue'
import { taskApi } from '../api'

const props = defineProps({
  // 当前筛选后可见的任务列表（用于「全选」的语义：只选可见项）
  visibleTasks: { type: Array, default: () => [] },
  // 可选的任务分类列表（用于"移动到分类"下拉）
  categories: { type: Array, default: () => [] },
})
// change / ask-delete 必须显式声明：
//   - 未声明的事件监听器会残留在 $attrs 并被当作原生 DOM 监听器挂到根元素；
//   - 开发模式下每个未声明事件都会警告。
const emit = defineEmits(['done', 'clear', 'change', 'ask-delete'])

const busy = ref(false)
const progress = ref('')
const lastResult = ref(null)
const showFailed = ref(false)

// 「移动到分类」「改调度」两个动作的目标值。
// '' = 尚未选择。
const targetCategory = ref('')
const targetMode = ref('')
// 是否动过下拉框：批量写 N 个任务是不可逆操作，
// 不能因为下拉框默认值存在就自动执行。
const categoryTouched = ref(false)
const modeTouched = ref(false)

const selectedCount = computed(() => selected.value.size)
const allSelected = computed(() => {
  const v = props.visibleTasks
  return v.length > 0 && v.every(t => selected.value.has(t.id))
})

const selected = ref(new Set())

// emitChange 把当前选中集合同步给父组件。
//
// 为什么必须有这一步：卡片上的勾选框和浮动条是两套状态，
// 父组件（Tasks.vue）用 selectedIds 决定卡片显示勾选态、并据此批量删除。
// 早先 sync() 写好了却从未被调用 → 浮动条永远不出现、
// 批量删除读到的永远是空集合而静默 return（点了没反应）。
function emitChange() {
  emit('change', new Set(selected.value))
}

function toggle(id) {
  const s = new Set(selected.value)
  s.has(id) ? s.delete(id) : s.add(id)
  selected.value = s
  emitChange()
}

function selectAll() {
  selected.value = new Set(props.visibleTasks.map(t => t.id))
  emitChange()
}

function clearSelection() {
  selected.value = new Set()
  lastResult.value = null
  emitChange()
  emit('clear')
}

// sync 供父组件在外部清空选中后回写（保持单一数据源）。
function sync(s) {
  selected.value = s instanceof Set ? new Set(s) : new Set()
  emitChange()
}

async function run(action, extra = {}) {
  if (busy.value) return
  const ids = [...selected.value]
  if (ids.length === 0) return

  busy.value = true
  progress.value = `处理中 0/${ids.length}`
  lastResult.value = null
  try {
    const res = await taskApi.batch({ ids, action, ...extra })
    lastResult.value = res
    if (res.failed > 0) {
      showFailed.value = true
    }
    emit('done', res)
    // 成功的项从选中集合移除，方便连续操作剩余项
    const failedIds = new Set(
      (res.items || []).filter(i => !i.ok).map(i => i.id),
    )
    selected.value = failedIds
    emitChange()
  } catch (e) {
    // 错误由父组件 toast（这里只负责清空进度）
    throw e
  } finally {
    busy.value = false
    progress.value = ''
  }
}

// 把「移动到分类」的两步（选值 + 应用）合并成一个动作。
// 单独抽出来是为了让"选完再点应用"这一步显式发生：
// 批量写 N 个任务不可逆，不能因为下拉框有值就自动执行。
async function applyCategory() {
  if (!categoryTouched.value || targetCategory.value === '') return
  await run('set_category', { category_id: Number(targetCategory.value) || 0 })
}

async function applyMode() {
  if (!modeTouched.value || !targetMode.value) return
  await run('set_mode', { run_mode: targetMode.value })
}

function onCategoryChange() { categoryTouched.value = true }
function onModeChange() { modeTouched.value = true }

defineExpose({ selected, clearSelection, toggle, selectAll, sync })
</script>

<template>
  <Teleport to="body">
    <div v-if="selectedCount > 0 || lastResult" class="batch-bar"
      role="region" aria-label="批量操作">
      <div class="batch-inner">
        <template v-if="lastResult">
          <span class="batch-count">
            <b>{{ lastResult.succeeded }}</b> 成功
            <template v-if="lastResult.failed > 0">
              · <b class="text-danger">{{ lastResult.failed }}</b> 失败
            </template>
          </span>
          <span class="flex-1"></span>
          <button v-if="lastResult.failed > 0" class="btn btn-sm"
            @click="showFailed = !showFailed">
            {{ showFailed ? '收起失败详情' : '查看失败原因' }}
          </button>
          <button class="btn btn-sm btn-primary" @click="clearSelection">完成</button>
        </template>

        <template v-else>
          <span class="batch-count">已选 <b>{{ selectedCount }}</b> 个</span>
          <button class="btn btn-sm btn-ghost" :disabled="allSelected" @click="selectAll">
            全选
          </button>
          <button class="btn btn-sm btn-ghost" @click="clearSelection">取消</button>
          <span class="spacer"></span>
          <span v-if="busy" class="muted" style="font-size: 12.5px">{{ progress }}</span>
          <button class="btn btn-sm btn-primary" :disabled="busy" @click="run('run')">▶ 运行</button>
          <button class="btn btn-sm" :disabled="busy" @click="run('pause')">⏸ 暂停</button>
          <button class="btn btn-sm" :disabled="busy" @click="run('resume')">⏵ 启用</button>
          <button class="btn btn-sm btn-danger" :disabled="busy" @click="$emit('ask-delete')">
            🗑 删除
          </button>

          <!-- 移动分类：选完再点「应用」才执行。
               批量写 N 个任务不可逆，不能因为下拉框有默认值就自动触发。 -->
          <div v-if="categories.length" class="batch-select">
            <select class="select" v-model="targetCategory" @change="onCategoryChange">
              <option value="" disabled>移动到分类…</option>
              <option value="0">（未分类）</option>
              <option v-for="c in categories" :key="c.id" :value="String(c.id)">{{ c.name }}</option>
            </select>
            <button v-if="categoryTouched" class="btn btn-sm" :disabled="busy"
              @click="applyCategory">应用</button>
          </div>

          <!-- 改调度模式 -->
          <div class="batch-select">
            <select class="select" v-model="targetMode" @change="onModeChange">
              <option value="" disabled>调度模式…</option>
              <option value="manual">手动运行</option>
              <option value="global">跟随全局</option>
              <option value="custom">自定义定时</option>
            </select>
            <button v-if="modeTouched" class="btn btn-sm" :disabled="busy"
              @click="applyMode">应用</button>
          </div>
        </template>
      </div>

      <div v-if="showFailed && lastResult" class="batch-failed">
        <div v-for="it in lastResult.items.filter(i => !i.ok)" :key="it.id"
          class="batch-failed-item">
          <span class="mono">{{ it.task_name || ('#' + it.id) }}</span>
          <span class="muted">— {{ it.message }}</span>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
/* 浮动条固定在底部中央：不用 fixed 顶部是因为移动端顶栏已被占用，
   底部更接近拇指热区，且不会遮挡页面标题。 */
.batch-bar {
  position: fixed;
  left: 50%;
  bottom: 24px;
  transform: translateX(-50%);
  z-index: 95;
  width: min(760px, calc(100vw - 32px));
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-lg);
  animation: batchUp 0.2s ease;
}
@keyframes batchUp {
  from { opacity: 0; transform: translate(-50%, 16px); }
  to { opacity: 1; transform: translate(-50%, 0); }
}
.batch-inner {
  display: flex; align-items: center; gap: 8px; flex-wrap: wrap;
  padding: 10px 14px;
}
.batch-count { font-size: 13px; color: var(--text-2); white-space: nowrap; }
/* 批量下拉：窄屏时占满整行，避免七个控件挤成两行 */
.batch-select { display: inline-flex; gap: 6px; align-items: center; }
.batch-select .select { height: 30px; font-size: 12.5px; width: auto; min-width: 110px; }
.batch-failed {
  border-top: 1px solid var(--border);
  padding: 8px 14px 12px;
  max-height: 180px; overflow: auto;
}
.batch-failed-item {
  font-size: 12px; color: var(--text-2); line-height: 1.7;
}
@media (prefers-reduced-motion: reduce) {
  .batch-bar { animation: none; }
}
@media (max-width: 820px) {
  /* 移动端占满宽度并贴底，避免按钮被挤出屏幕 */
  .batch-bar {
    left: 12px; right: 12px; bottom: 12px;
    transform: none;
    width: auto;
    animation: none;
  }
  .batch-inner { padding: 10px 12px; gap: 6px; }
  .batch-inner .btn { flex: 1 1 auto; }
  .batch-inner .btn-ghost { flex: 0 0 auto; }
  /* iOS 底部安全区 */
  .batch-bar { bottom: calc(12px + env(safe-area-inset-bottom, 0px)); }
}
</style>