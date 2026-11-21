<script setup>
// 编辑/新建追剧转存任务。
import { ref, reactive, inject, computed, watch, onMounted, onBeforeUnmount } from 'vue'
import Modal from './Modal.vue'
import SharePreview from './SharePreview.vue'
import FileBrowser from './FileBrowser.vue'
import { accountApi, taskApi, ruleApi, settingsApi, cronApi, categoryApi } from '../api'
import { CRON_PRESETS, cronToText } from '../cron'
import WeekPicker from './WeekPicker.vue'
import { extractShareFid, withShareFid, stripShareFid } from '../shareurl'
import { useDraft, useDirtyGuard } from '../composables/useDraft'

const toast = inject('toast')
const confirmDialog = inject('confirmDialog')

const draft = useDraft('task_edit', form)
const { confirmDiscard } = useDirtyGuard(confirmDialog, draft.dirty)

// 表单分区导航。
//
// 为什么是锚点而不是分步向导：
//   分步向导每一步都要校验，但配置类表单的字段依赖很多（账号决定驱动、
//   驱动决定字段、调度模式决定 cron 是否出现），强行分步会把用户卡在某一步。
//   锚点导航只帮定位、不打断填写，是更贴合这个表单的折中。
const SECTIONS = [
  { id: 'sec-basic', label: '基础' },
  { id: 'sec-rename', label: '保存与命名' },
  { id: 'sec-schedule', label: '调度' },
]
const activeSection = ref('sec-basic')

// scrollTo 把目标分区滚到弹层可视区顶部。
//
// 用 scrollIntoView 而不是自己算 offsetTop：
// 弹层内部是独立滚动容器，offsetTop 的参照系不是它。
// 配合 CSS 的 scroll-margin-top，标题不会被吸顶的导航条遮住。
function scrollTo(id) {
  const el = document.getElementById(id)
  if (!el) return
  activeSection.value = id
  el.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

const visible = ref(false)
const saving = ref(false)
const isEdit = ref(false)
const accounts = ref([])
const rules = ref([])
const categories = ref([])

const form = reactive({
  id: 0,
  task_name: '',
  account_id: 0,
  links: [''],
  save_path: '',
  pattern: '',
  replace: '',
  ignore_extension: true,
  run_week: '',
  run_time: '',
  run_cron: '',
  end_date: '',
  update_subdir: '',
  update_subdir_resave_mode: '',
  start_fid: '',
  category_id: 0,
  sort_index: 1,
  skip_history: 'global',
  run_mode: 'manual',
  status: 'running',
})
const scheduleMode = ref('manual') // manual | global | custom
const globalSchedule = ref(null)   // 设置页返回
const nextRunText = ref('')        // 当前编辑的 cron 下次执行时间
const nextRunLoading = ref(false)

const globalCron = computed(() => {
  const g = globalSchedule.value
  if (!g) return ''
  if (g.schedule_cron) return g.schedule_cron
  if (g.schedule_time) {
    const [h, m] = g.schedule_time.split(':')
    const dow = g.schedule_week || '*'
    return `${m} ${h} * * ${dow}`
  }
  return ''
})

const globalHint = computed(() => {
  const g = globalSchedule.value
  if (!g) return ''
  if (g.schedule_enabled === 'off') return '全局调度当前已关闭，跟随全局的任务只能手动运行'
  const cron = g.schedule_cron || (g.schedule_time ? `${(g.schedule_time.split(':').pop()||'0').padStart(2,'0')} ${g.schedule_time.split(':')[0]} * * ${g.schedule_week||'*'}` : '')
  if (!cron) return '全局调度尚未配置 cron 表达式，请在「设置 → 调度配置」中设置'
  return `全局调度：${cronToText(cron)}（原始表达式 ${cron}，在「设置 → 调度配置」中修改）`
})

// cron 输入变化 → 计算下次执行
const cronNext = ref('')
const cronError = ref('')
watch(() => form.run_cron, (v) => {
  cronNext.value = ''; cronError.value = ''
  if (!v || !v.trim()) return
  const expr = v.trim()
  // 简单校验 5 字段
  const parts = expr.split(/\s+/)
  if (parts.length !== 5) { cronError.value = '需要 5 个字段：分 时 日 月 周'; return }
  cronApi.next(expr).then(r => { cronNext.value = r.next_run }).catch(e => { cronError.value = e.message })
}, { immediate: false })

const selectedAccount = computed(() => accounts.value.find(a => a.id === form.account_id))

// 内置规则下拉（来自 /api/rules 中 builtin 项）
const builtinRules = computed(() => rules.value.filter(r => r.builtin))

const emit = defineEmits(['saved'])
const previewRef = ref(null)
const browserRef = ref(null)

// 「n」快捷键：由 App 层派发 pt:new-task 事件触发。
// 只在任务页（组件已挂载）时生效；其它页面没有监听，按下无反应。
function onNewTaskShortcut() {
  open(null)
}
onMounted(() => window.addEventListener('pt:new-task', onNewTaskShortcut))
onBeforeUnmount(() => window.removeEventListener('pt:new-task', onNewTaskShortcut))

async function open(task) {
  visible.value = true
  isEdit.value = !!task
  try {
    const [accs, rls, st, cats] = await Promise.all([accountApi.list(), ruleApi.list(), settingsApi.get(), categoryApi.list().catch(() => [])])
    accounts.value = accs || []
    rules.value = rls || []
    categories.value = cats || []
    globalSchedule.value = st
  } catch (e) {
    toast(e.message, 'error')
  }
  if (task) {
    Object.assign(form, {
      id: task.id,
      task_name: task.task_name,
      account_id: task.account_id,
      links: task.links && task.links.length ? [...task.links] : [''],
      save_path: task.save_path,
      pattern: task.pattern,
      replace: task.replace,
      ignore_extension: task.ignore_extension,
      run_week: task.run_week,
      run_time: task.run_time,
      run_cron: task.run_cron || '',
      end_date: task.end_date,
      update_subdir: task.update_subdir,
      update_subdir_resave_mode: task.update_subdir_resave_mode || '',
      start_fid: task.start_fid,
      category_id: task.category_id || 0,
      sort_index: task.sort_index || 1,
      skip_history: task.skip_history || 'global',
      run_mode: task.run_mode || 'manual',
      status: task.status,
    })
    // 兼容旧数据：老任务只有 run_week+run_time，合成 cron 展示
    if (!form.run_cron && form.run_time && (form.run_mode || 'manual') === 'custom') {
      const [h, m] = form.run_time.split(':')
      form.run_cron = `${m} ${h} * * ${form.run_week || '*'}`
    }
    scheduleMode.value = task.run_mode || (task.run_cron || task.run_time ? 'custom' : 'manual')
  } else {
    Object.assign(form, {
      id: 0, task_name: '', account_id: accounts.value[0]?.id || 0,
      links: [''], save_path: '', pattern: '', replace: '',
      ignore_extension: true, run_week: '', run_time: '', run_cron: '', end_date: '',
      update_subdir: '', start_fid: '', category_id: 0, sort_index: 1, skip_history: 'global',
      update_subdir_resave_mode: '',
      run_mode: 'manual', status: 'running',
    })
    scheduleMode.value = 'manual'
  }

  // 草稿：绑定草稿监听（在表单填好之后再 start，避免把加载中的中间态存进去）。
  //
  // 只对「新建任务」恢复草稿：编辑已有任务时表单已经填好了库里的值，
  // 再叠加草稿会把旧内容和草稿混在一起，反而更乱。
  draft.start()
  if (!task && draft.restore()) {
    toast('已恢复上次未保存的新建任务草稿', 'info', 4500)
  }
}

// 关闭表单：脏检查。
//
// 为什么需要：这个表单有 20+ 字段，关闭途径有 4 条（遮罩、✕、取消、路由切换），
// 此前全部静默丢弃。用户填完 20 个字段被误触遮罩 → 全部重来。
async function close() {
  if (!(await confirmDiscard())) return
  draft.stop()
  visible.value = false
}

defineExpose({ open })

function addLink() { form.links.push('') }
function removeLink(i) {
  if (form.links.length === 1) { form.links[0] = ''; return }
  form.links.splice(i, 1)
}
function moveLink(i, dir) {
  const j = i + dir
  if (j < 0 || j >= form.links.length) return
  const tmp = form.links[i]
  form.links[i] = form.links[j]
  form.links[j] = tmp
}

function applyBuiltinRule(key) {
  const r = builtinRules.value.find(x => x.key === key)
  if (!r) return
  form.pattern = r.pattern
  form.replace = r.replace
}

// 预览指定下标的链接（无参=第一条，兼容旧调用）
function openPreview(i = 0) {
  if (!form.account_id) { toast('请先选择账号', 'error'); return }
  const link = (form.links[i] || '').trim()
  if (!link) { toast('请先填写该分享链接', 'error'); return }
  previewRef.value.open({
    account_id: form.account_id,
    share_url: link,
    link_index: i, // 「只转存当前文件夹」只写回这条链接
    task_name: form.task_name,
    pattern: form.pattern,
    replace: form.replace,
    sort_index: form.sort_index,
    save_path: form.save_path,
    ignore_extension: form.ignore_extension,
    update_subdir: form.update_subdir,
    start_fid: form.start_fid,
  })
}

// 把选中的文件夹写入对应链接（CASX 约定：#/list/share/<fid>），每条链接各自记忆。
function onUseFolder({ pdir_fid, link_index = 0 }) {
  const idx = link_index >= 0 && link_index < form.links.length ? link_index : 0
  const fid = String(pdir_fid || '').trim()
  if (fid) {
    form.links[idx] = withShareFid(form.links[idx], fid)
    toast(`已锁定第 ${idx + 1} 条链接的转存文件夹`, 'success')
  } else {
    form.links[idx] = stripShareFid(form.links[idx])
    toast(`第 ${idx + 1} 条链接已改为转存根目录`, 'success')
  }
}

function onPickPath({ path }) {
  form.save_path = path
}

function applyCronPreset(val) {
  form.run_cron = val
}

async function save() {
  if (!form.task_name.trim()) { toast('请填写任务名称', 'error'); return }
  if (!form.account_id) { toast('请选择网盘账号', 'error'); return }
  if (!form.links.some(s => s.trim())) { toast('至少填写一条分享链接', 'error'); return }
  if (!form.save_path.trim()) { toast('请选择保存路径', 'error'); return }
  saving.value = true
  try {
    // 调度模式决定提交哪些字段：custom 走 cron；manual 清空；global 由后端读全局
    const useCron = scheduleMode.value === 'custom' && form.run_cron.trim()
    const payload = {
      ...form,
      links: form.links.map(s => s.trim()).filter(Boolean),
      run_mode: scheduleMode.value,
      run_cron: useCron ? form.run_cron.trim() : '',
      run_time: useCron ? '' : (scheduleMode.value === 'custom' ? form.run_time : ''),
      run_week: useCron ? '' : (scheduleMode.value === 'custom' ? form.run_week : ''),
    }
    if (isEdit.value) await taskApi.update(form.id, payload)
    else await taskApi.create(payload)
    toast(isEdit.value ? '任务已更新' : '任务已创建', 'success')
    // 提交成功：清草稿。否则下次打开会看到上次的旧内容，
    // 用户会以为"没保存上"。
    draft.markSaved()
    visible.value = false
    emit('saved')
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <Modal v-model="visible" size="lg" :title="isEdit ? '编辑转存任务' : '新建转存任务'">
    <!-- 分区导航：20+ 字段的表单靠滚动找字段太慢。
          sticky 定位在弹层头部下方，滚动时始终可见。 -->
    <nav class="section-nav" aria-label="表单分区">
      <button v-for="s in SECTIONS" :key="s.id"
        class="section-nav-item" :class="{ active: activeSection === s.id }"
        @click="scrollTo(s.id)">{{ s.label }}</button>
    </nav>

          <!-- 基础信息 -->
          <div class="modal-section" id="sec-basic" style="border-top: none; margin-top: -20px; padding-top: 0">
            <div class="modal-section-title">基础信息</div>
            <div class="field">
              <div class="field-label">任务名称 <span class="req">*</span></div>
              <input class="input" v-model="form.task_name" placeholder="例如：爆金钗-{tmbid-300146}" />
            </div>

            <div class="field">
              <div class="field-label">分享链接 <span class="req">*</span>
                <span class="flex-1"></span>
                <button class="btn btn-sm" @click="openPreview(0)" :disabled="!form.account_id || !form.links[0]">🔍 预览第 1 条</button>
              </div>
              <div style="font-size: 12px; color: var(--text-3); margin-bottom: 8px">
                多源链接按从上到下顺序逐条转存（去重互斥），失效自动跳过。可把画质更好的链接放前面。每条链接可单独预览。
              </div>
              <div v-for="(link, i) in form.links" :key="i" style="margin-bottom: 8px">
                <div class="link-item" style="margin-bottom: 0">
                  <span class="link-idx">{{ i + 1 }}</span>
                  <input class="input" v-model="form.links[i]" placeholder="https://pan.quark.cn/s/… 或 https://www.guangyapan.com/s/…（可带提取码）" />
                  <button class="btn btn-icon" title="预览此链接" :disabled="!form.account_id || !link.trim()" @click="openPreview(i)">🔍</button>
                  <button class="btn btn-icon" title="上移" :disabled="i === 0" @click="moveLink(i, -1)">↑</button>
                  <button class="btn btn-icon" title="下移" :disabled="i === form.links.length - 1" @click="moveLink(i, 1)">↓</button>
                  <button class="btn btn-icon btn-danger" title="删除" @click="removeLink(i)">✕</button>
                </div>
                <div v-if="extractShareFid(link)" class="link-fid-line">
                  <span>📌 已锁定文件夹</span>
                  <code class="mono">{{ extractShareFid(link) }}</code>
                  <a @click="form.links[i] = stripShareFid(form.links[i])">改为转存根目录</a>
                </div>
              </div>
              <button class="btn btn-sm" @click="addLink">＋ 添加链接</button>
              <div class="field-hint" style="margin-top: 8px">在预览里点「📌 只转存当前文件夹」只会锁定该条链接的文件夹，每条链接独立记忆。</div>
            </div>

            <div class="form-row">
              <div class="field">
                <div class="field-label">转存账号 <span class="req">*</span></div>
                <select class="select" v-model="form.account_id">
                  <option v-for="a in accounts" :key="a.id" :value="a.id">{{ a.name }}（{{ a.driver_type }}）</option>
                </select>
              </div>
              <div class="field">
                <div class="field-label">保存路径 <span class="req">*</span></div>
                <div class="flex">
                  <input class="input flex-1" v-model="form.save_path" placeholder="/影视资源/电视剧/…" />
                  <button class="btn" @click="browserRef.open(form.account_id, form.save_path)" :disabled="!form.account_id">📁 浏览</button>
                </div>
              </div>
            </div>

            <div class="form-row">
              <div class="field">
                <div class="field-label">任务分类（回调分组）</div>
                <select class="select" v-model="form.category_id">
                  <option :value="0">未分类（不参与回调）</option>
                  <option v-for="c in categories" :key="c.id" :value="c.id">{{ c.name }}{{ c.callback_enabled ? '' : '（仅分组）' }}</option>
                </select>
                <div class="field-hint" style="margin-top: 4px">分类的回调接口在「设置 → 任务分类与回调」配置。</div>
              </div>
              <div class="field">
                <div class="field-label">跳过已转存历史</div>
                <select class="select" v-model="form.skip_history">
                  <option value="global">跟随全局</option>
                  <option value="on">强制开</option>
                  <option value="off">强制关</option>
                </select>
                <div class="field-hint" style="margin-top: 4px">按转存历史去重，防止重复转存。</div>
              </div>
            </div>
          </div>

          <!-- 重命名规则 -->
          <div class="modal-section" id="sec-rename">
            <div class="modal-section-title">重命名规则（可选）</div>
            <div class="field">
              <div class="field-label">内置规则</div>
              <select class="select" @change="applyBuiltinRule($event.target.value); $event.target.value = ''" value="">
                <option value="" disabled selected>选择内置规则快速填充…</option>
                <option v-for="r in builtinRules" :key="r.key" :value="r.key">{{ r.label }}（{{ r.key }}）</option>
              </select>
            </div>
            <div class="form-row">
              <div class="field">
                <div class="field-label">匹配正则（pattern）</div>
                <input class="input mono" v-model="form.pattern" placeholder="留空 = 全量转存不重命名" />
              </div>
              <div class="field">
                <div class="field-label">替换模板（replace）</div>
                <input class="input mono" v-model="form.replace" placeholder="{TASKNAME}.{SXX}E{E}.{EXT}" />
              </div>
            </div>
            <label class="checkbox">
              <input type="checkbox" v-model="form.ignore_extension" />
              忽略扩展名去重（mp4 / mkv 有一个即视为已有，不再转存另一个）
            </label>
            <div class="form-row mt-16">
              <div class="field" style="margin-bottom: 0">
                <div class="field-label">子目录同步正则（可选）</div>
                <input class="input mono" v-model="form.update_subdir" placeholder="如 Season \d+，命中目录递归同步" />
              </div>
              <div class="field" style="margin-bottom: 0">
                <div class="field-label">起始 fid（增量，可选）</div>
                <input class="input mono" v-model="form.start_fid" placeholder="只转存该文件之后的更新（手工填 fid）" />
              </div>
            </div>

            <!-- 起始集号：后端字段 sort_index 一直是通的（planTransfer 用它作为 {I+} 的
                 起始序号），但表单里从未暴露过控件，用户无法指定"从第 1 集开始编号"。
                 例如把第 12 集起的内容单独转存为 S01E01。 -->
            <div class="form-row mt-16">
              <div class="field" style="margin-bottom: 0">
                <div class="field-label">
                  起始集号（{II} 递增序号从几开始）
                </div>
                <input class="input" type="number" min="1" max="9999"
                  v-model.number="form.sort_index" placeholder="默认 1" />
                <div class="field-hint">
                  仅当替换模板里用到 <code class="mono">{II}</code> 时生效。
                  想让「第 12 集」变成 <code class="mono">E01</code> 就填 1 并把该集放在最前。
                </div>
              </div>
              <div class="field" style="margin-bottom: 0">
                <div class="field-label">子目录已存在时</div>
                <select class="select" v-model="form.update_subdir_resave_mode"
                  :disabled="!form.update_subdir">
                  <option value="">增量：跳过已存在的文件</option>
                  <option value="delete_then_resave">先删除再重存（⚠ 会删远端目录）</option>
                </select>
                <div class="field-hint">
                  仅当上面填了「子目录同步正则」时可用。
                  <span v-if="form.update_subdir_resave_mode === 'delete_then_resave'" class="text-danger">
                    ⚠ 该模式会先删除目标目录中命中的子目录再重新转存，请确认目录内容可丢弃。
                  </span>
                  <span v-else>默认增量：已有的文件会被跳过。</span>
                </div>
              </div>
            </div>
          </div>

          <!-- 调度配置 -->
          <div class="modal-section" id="sec-schedule">
            <div class="modal-section-title">调度配置</div>
            <div class="flex mb-16" style="gap: 16px; flex-wrap: wrap">
              <label class="checkbox"><input type="radio" value="manual" v-model="scheduleMode" /> 手动运行</label>
              <label class="checkbox"><input type="radio" value="global" v-model="scheduleMode" /> 跟随全局</label>
              <label class="checkbox"><input type="radio" value="custom" v-model="scheduleMode" /> 自定义定时</label>
            </div>
            <div v-if="scheduleMode === 'global'" class="card card-pad" style="background: var(--surface-2); font-size: 13px; color: var(--text-2)">
              {{ globalHint || '加载全局调度配置中…' }}
            </div>
            <template v-if="scheduleMode === 'custom'">
              <div class="field">
                <div class="field-label">Cron 表达式（分 时 日 月 周）</div>
                <div class="flex" style="gap: 8px; flex-wrap: wrap">
                  <input class="input mono flex-1" style="min-width: 180px" v-model="form.run_cron" placeholder="例如 0 */2 * * *  = 每 2 小时" />
                </div>
                <div class="flex mt-8" style="flex-wrap: wrap; gap: 6px">
                  <button v-for="p in CRON_PRESETS" :key="p.value" class="btn btn-sm" @click="form.run_cron = p.value">{{ p.label }}</button>
                </div>
                <div v-if="cronError" class="field-hint text-danger" style="margin-top: 6px">{{ cronError }}</div>
                <div v-else-if="cronNext" class="field-hint" style="margin-top: 6px">下次执行：{{ cronNext }}</div>
                <div v-else-if="form.run_cron" class="field-hint text-warn" style="margin-top: 6px">表达式格式有误，暂无法计算下次执行时间。</div>
              </div>
              <div class="field" style="margin-bottom: 0">
                <div class="field-label">截止日期（可选）</div>
                <input class="input" type="date" v-model="form.end_date" style="max-width: 220px" />
              </div>
              <div class="field-hint" style="margin-top: 8px">常用：每 2 小时 <code class="mono">0 */2 * * *</code>；每天 08:00 <code class="mono">0 8 * * *</code>；周一/五 20:30 <code class="mono">30 20 * * 1,5</code>。到点后有 1–3 分钟随机延迟，降低风控风险。</div>
            </template>
          </div>
      <template #footer>
        <span class="flex-1"></span>
        <button class="btn" @click="close">取消</button>
        <button class="btn btn-primary" :disabled="saving" @click="save">
          <span v-if="saving" class="spinner" style="border-top-color: #fff"></span>
          <span v-else>{{ isEdit ? '保存修改' : '创建任务' }}</span>
        </button>
      </template>
  </Modal>

  <SharePreview ref="previewRef" @use-folder="onUseFolder" />
  <FileBrowser ref="browserRef" @picked="onPickPath" />
</template>

<style scoped>
/* 分区导航：吸顶在弹层头部下方，滚动时始终可见。
   background 用 surface 而不是透明：滚动时下面的内容会透上来，
   文字叠在文字上完全无法阅读。 */
.section-nav {
  position: sticky;
  top: -1px;
  z-index: 2;
  display: flex;
  gap: 4px;
  padding: 8px 0 10px;
  background: var(--surface);
  border-bottom: 1px solid var(--border);
}
.section-nav-item {
  border: none;
  background: none;
  padding: 5px 12px;
  border-radius: 6px;
  font-size: 13px;
  font-weight: 500;
  color: var(--text-2);
  font-family: var(--font);
  cursor: pointer;
  transition: background-color var(--dur-fast) var(--ease-out),
    color var(--dur-fast) var(--ease-out);
}
.section-nav-item:hover { background: var(--surface-2); color: var(--text); }
.section-nav-item.active {
  background: var(--primary-soft);
  color: var(--primary);
}
/* 锚点跳转时标题不被吸顶导航条遮住 */
:deep(.modal-section) { scroll-margin-top: 56px; }
</style>
