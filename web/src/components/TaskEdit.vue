<script setup>
// 编辑/新建追剧转存任务。
import { ref, reactive, inject, computed, watch } from 'vue'
import Modal from './Modal.vue'
import SharePreview from './SharePreview.vue'
import FileBrowser from './FileBrowser.vue'
import { accountApi, taskApi, ruleApi, settingsApi, cronApi } from '../api'
import { CRON_PRESETS, cronToText } from '../cron'
import WeekPicker from './WeekPicker.vue'

const toast = inject('toast')

const visible = ref(false)
const saving = ref(false)
const isEdit = ref(false)
const accounts = ref([])
const rules = ref([])

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
  start_fid: '',
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

async function open(task) {
  visible.value = true
  isEdit.value = !!task
  try {
    const [accs, rls, st] = await Promise.all([accountApi.list(), ruleApi.list(), settingsApi.get()])
    accounts.value = accs || []
    rules.value = rls || []
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
      start_fid: task.start_fid,
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
      update_subdir: '', start_fid: '', sort_index: 1, skip_history: 'global',
      run_mode: 'manual', status: 'running',
    })
    scheduleMode.value = 'manual'
  }
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

function onUseFolder({ pdir_fid }) {
  form.start_fid = pdir_fid
  toast('已设置起始目录（增量转存起点）', 'success')
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
  <Teleport to="body">
    <div v-if="visible" class="modal-overlay" @mousedown.self="visible = false">
      <div class="modal modal-lg">
        <div class="modal-head">
          <div class="modal-title">{{ isEdit ? '编辑转存任务' : '新建转存任务' }}</div>
          <button class="modal-close" @click="visible = false">✕</button>
        </div>
        <div class="modal-body">
          <!-- 基础信息 -->
          <div class="modal-section" style="border-top: none; margin-top: -20px; padding-top: 0">
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
              <div v-for="(link, i) in form.links" :key="i" class="link-item">
                <span class="link-idx">{{ i + 1 }}</span>
                <input class="input" v-model="form.links[i]" placeholder="https://pan.quark.cn/s/… 或 https://www.guangyapan.com/s/…（可带提取码）" />
                <button class="btn btn-icon" title="预览此链接" :disabled="!form.account_id || !link.trim()" @click="openPreview(i)">🔍</button>
                <button class="btn btn-icon" title="上移" :disabled="i === 0" @click="moveLink(i, -1)">↑</button>
                <button class="btn btn-icon" title="下移" :disabled="i === form.links.length - 1" @click="moveLink(i, 1)">↓</button>
                <button class="btn btn-icon btn-danger" title="删除" @click="removeLink(i)">✕</button>
              </div>
              <button class="btn btn-sm" @click="addLink">＋ 添加链接</button>
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

            <div class="field">
              <div class="field-label">跳过已转存历史</div>
              <div class="flex" style="gap: 10px">
                <select class="select" v-model="form.skip_history" style="max-width: 200px">
                  <option value="global">跟随全局</option>
                  <option value="on">强制开</option>
                  <option value="off">强制关</option>
                </select>
                <span class="field-hint" style="margin-top: 0">按转存历史去重，防止重复转存；开启「清理源文件」类工作流时建议强制开。</span>
              </div>
            </div>
          </div>

          <!-- 重命名规则 -->
          <div class="modal-section">
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
                <input class="input mono" v-model="form.start_fid" placeholder="预览中「使用当前文件夹」自动填入" />
              </div>
            </div>
          </div>

          <!-- 调度配置 -->
          <div class="modal-section">
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
        </div>
        <div class="modal-foot">
          <span class="flex-1"></span>
          <button class="btn" @click="visible = false">取消</button>
          <button class="btn btn-primary" :disabled="saving" @click="save">
            <span v-if="saving" class="spinner" style="border-top-color: #fff"></span>
            <span v-else>{{ isEdit ? '保存修改' : '创建任务' }}</span>
          </button>
        </div>
      </div>
    </div>

    <SharePreview ref="previewRef" @use-folder="onUseFolder" />
    <FileBrowser ref="browserRef" @picked="onPickPath" />
  </Teleport>
</template>
