<script setup>
// 设置：通知渠道管理 + 全局开关 + 访问密码。
import Modal from '../components/Modal.vue'
import WeekPicker from '../components/WeekPicker.vue'
import { settingsApi, notifyApi, authApi, cronApi, categoryApi, fmtTime } from '../api'
import { CRON_PRESETS, cronToText } from '../cron'
import { ref, reactive, inject, onMounted, watch } from 'vue'

const toast = inject('toast')
const session = inject('session')
const refreshSession = inject('refreshSession')

const loading = ref(true)
const saving = ref(false)
const settings = reactive({
  notify_channels: [],
  skip_history_global: 'on',
  schedule_enabled: 'on',
  schedule_time: '',
  schedule_week: '',
  schedule_cron: '',
  run_retention_days: '90',
  auth_enabled: false,
})

const globalCronNext = ref('')
const globalCronError = ref('')
watch(() => settings.schedule_cron, (v) => {
  globalCronNext.value = ''; globalCronError.value = ''
  if (!v || !v.trim()) return
  const expr = v.trim()
  if (expr.split(/\s+/).length !== 5) { globalCronError.value = '需要 5 个字段：分 时 日 月 周'; return }
  cronApi.next(expr).then(r => { globalCronNext.value = r.next_run }).catch(e => { globalCronError.value = e.message })
})

const channelTypes = [
  { value: 'telegram', label: 'Telegram Bot' },
  { value: 'bark', label: 'Bark (iOS)' },
  { value: 'ntfy', label: 'ntfy' },
  { value: 'wecom_bot', label: '企业微信机器人' },
  { value: 'feishu_bot', label: '飞书机器人' },
  { value: 'dingtalk_bot', label: '钉钉机器人' },
  { value: 'webhook', label: '自定义 Webhook' },
]

const channelFields = {
  telegram: [
    { key: 'bot_token', label: 'Bot Token', ph: '123456:ABC-DEF…' },
    { key: 'chat_id', label: 'Chat ID', ph: '频道/群组/用户 ID' },
    { key: 'api_base', label: 'API 反代（可选）', ph: 'https://api.telegram.org' },
  ],
  bark: [
    { key: 'device_key', label: 'Device Key', ph: 'Bark App 中的推送 Key' },
    { key: 'server', label: '自建服务（可选）', ph: 'https://api.day.app' },
    { key: 'sound', label: '提示音（可选）', ph: 'minuet' },
  ],
  ntfy: [
    { key: 'topic_url', label: 'Topic URL', ph: 'https://ntfy.sh/my-topic' },
    { key: 'username', label: '用户名（可选）' },
    { key: 'password', label: '密码（可选）' },
  ],
  wecom_bot: [{ key: 'webhook', label: 'Webhook 地址', ph: 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=…' }],
  feishu_bot: [{ key: 'webhook', label: 'Webhook 地址', ph: 'https://open.feishu.cn/open-apis/bot/v2/hook/…' }],
  dingtalk_bot: [{ key: 'webhook', label: 'Webhook 地址', ph: 'https://oapi.dingtalk.com/robot/send?access_token=…' }],
  webhook: [
    { key: 'url', label: 'Webhook URL' },
    { key: 'body_template', label: '消息模板（可选）', ph: '预留 {message} 占位符' },
  ],
}

const showChannel = ref(false)
const editingChannel = ref(false)
const testingChannel = ref(false)
const chForm = reactive({ id: '', type: 'telegram', name: '', enabled: true, config: {} })

// ---------- 任务分类与回调 ----------
const categories = ref([])
const showCatEdit = ref(false)
const editingCat = ref(false)
const savingCat = ref(false)
const testingCat = ref(false)
const catTestResult = ref(null)
const showCatHelp = ref(false)
const showCatLogs = ref(false)
const catLogs = ref([])
const catLogsLoading = ref(false)
const logsCat = ref(null)
const catForm = reactive({
  id: 0, name: '', callback_enabled: false, callback_mode: 'round',
  callback_method: 'POST', callback_url: '', callback_headers: '', callback_body: '',
  callback_timeout_sec: 15, sort_order: 0,
})

async function loadCategories() {
  try {
    categories.value = (await categoryApi.list()) || []
  } catch (e) {
    toast(e.message, 'error')
  }
}

function openCatNew() {
  editingCat.value = false
  Object.assign(catForm, {
    id: 0, name: '', callback_enabled: false, callback_mode: 'round',
    callback_method: 'POST', callback_url: '', callback_headers: '', callback_body: '',
    callback_timeout_sec: 15, sort_order: categories.value.length,
  })
  catTestResult.value = null
  showCatEdit.value = true
}

function openCatEdit(c) {
  editingCat.value = true
  Object.assign(catForm, {
    id: c.id, name: c.name, callback_enabled: c.callback_enabled, callback_mode: c.callback_mode,
    callback_method: c.callback_method, callback_url: c.callback_url,
    callback_headers: c.callback_headers || '', callback_body: c.callback_body || '',
    callback_timeout_sec: c.callback_timeout_sec || 15, sort_order: c.sort_order || 0,
  })
  catTestResult.value = null
  showCatEdit.value = true
}

async function saveCat() {
  if (!catForm.name.trim()) { toast('请填写分类名称', 'error'); return }
  savingCat.value = true
  try {
    await categoryApi.save({ ...catForm, name: catForm.name.trim() })
    toast(editingCat.value ? '分类已更新' : '分类已创建', 'success')
    showCatEdit.value = false
    loadCategories()
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    savingCat.value = false
  }
}

async function removeCat(c) {
  if (!confirm(`删除分类「${c.name}」？其下 ${c.task_count} 个任务将变为未分类，回调配置与日志一并删除。`)) return
  try {
    await categoryApi.remove(c.id)
    toast('分类已删除', 'success')
    loadCategories()
  } catch (e) {
    toast(e.message, 'error')
  }
}

async function testCat() {
  if (!catForm.id) { toast('请先保存分类再测试', 'error'); return }
  testingCat.value = true
  catTestResult.value = null
  try {
    catTestResult.value = await categoryApi.test(catForm.id)
  } catch (e) {
    catTestResult.value = { ok: false, error: e.message }
  } finally {
    testingCat.value = false
  }
}

async function openCatLogs(c) {
  logsCat.value = c
  showCatLogs.value = true
  catLogsLoading.value = true
  try {
    catLogs.value = (await categoryApi.logs(c.id)) || []
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    catLogsLoading.value = false
  }
}

async function reload() {
  loading.value = true
  try {
    const s = await settingsApi.get()
    Object.assign(settings, s)
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    loading.value = false
  }
  loadCategories()
}
onMounted(reload)

function typeLabel(v) { return channelTypes.find(t => t.value === v)?.label || v }

function openAddChannel() {
  editingChannel.value = false
  Object.assign(chForm, { id: 'ch_' + Date.now(), type: 'telegram', name: '', enabled: true, config: {} })
  showChannel.value = true
}

function openEditChannel(c) {
  editingChannel.value = true
  Object.assign(chForm, JSON.parse(JSON.stringify(c)))
  showChannel.value = true
}

function onTypeChange() { chForm.config = {} }

async function saveChannel() {
  if (!chForm.name.trim()) { toast('请填写渠道名称', 'error'); return }
  const list = [...settings.notify_channels]
  const idx = list.findIndex(c => c.id === chForm.id)
  if (idx >= 0) list[idx] = { ...chForm }
  else list.push({ ...chForm })
  await persist({ notify_channels: list })
  showChannel.value = false
}

async function removeChannel(c) {
  if (!confirm(`确定删除通知渠道「${c.name}」？`)) return
  await persist({ notify_channels: settings.notify_channels.filter(x => x.id !== c.id) })
}

async function testChannel() {
  testingChannel.value = true
  try {
    await notifyApi.test({ ...chForm })
    toast('测试消息已发送，请查收', 'success')
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    testingChannel.value = false
  }
}

async function persist(patch) {
  saving.value = true
  try {
    await settingsApi.save(patch)
    Object.assign(settings, await settingsApi.get())
    toast('设置已保存', 'success')
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    saving.value = false
  }
}

function saveSchedule() {
  if (settings.schedule_enabled === 'on' && !settings.schedule_cron.trim() && !settings.schedule_time) {
    toast('全局调度开启时需要填写 cron 表达式或运行时间', 'error')
    return
  }
  persist({
    schedule_enabled: settings.schedule_enabled,
    schedule_cron: settings.schedule_cron,
    schedule_time: settings.schedule_time,
    schedule_week: settings.schedule_week,
  })
}

// 访问密码
const pwdModal = ref(false)
const pwd = ref('')
const pwd2 = ref('')
async function savePassword() {
  if (pwd.value !== pwd2.value) { toast('两次输入的密码不一致', 'error'); return }
  await persist({ password: pwd.value })
  pwd.value = pwd2.value = ''
  pwdModal.value = false
  refreshSession()
}
async function clearPassword() {
  if (!confirm('确定关闭访问密码？任何能访问本服务的人都可以操作。')) return
  await persist({ password: '' })
  pwdModal.value = false
  refreshSession()
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1 class="page-title">设置</h1>
        <div class="page-desc">通知推送、全局去重策略与访问控制。</div>
      </div>
    </div>

    <div v-if="loading" class="loading-block"><span class="spinner"></span> 加载中…</div>
    <template v-else>
      <!-- 通知 -->
      <div class="card card-pad mb-16">
        <div class="flex mb-16">
          <div>
            <div class="card-title" style="margin-bottom: 2px">通知推送</div>
            <div class="page-desc">有新文件转存或整次运行失败时推送；本次 0 个新文件（无更新 / 全部已转存）时静默，不推送空跑结果。</div>
          </div>
          <span class="flex-1"></span>
          <button class="btn btn-primary" @click="openAddChannel">＋ 添加渠道</button>
        </div>
        <div v-if="!settings.notify_channels.length" class="empty" style="padding: 26px">
          <div class="empty-hint">尚未配置通知渠道（支持 Telegram / Bark / ntfy / 企业微信 / 飞书 / 钉钉 / Webhook）</div>
        </div>
        <div v-else style="display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 10px">
          <div v-for="c in settings.notify_channels" :key="c.id" class="channel-card">
            <div class="flex">
              <div style="font-weight: 600">{{ c.name }}</div>
              <span class="tag">{{ typeLabel(c.type) }}</span>
              <span class="flex-1"></span>
              <span class="badge" :class="c.enabled ? 'badge-success' : 'badge-gray'"><span class="dot"></span>{{ c.enabled ? '启用' : '停用' }}</span>
            </div>
            <div class="flex">
              <button class="btn btn-sm" @click="openEditChannel(c)">编辑</button>
              <span class="spacer"></span>
              <button class="btn btn-sm btn-danger btn-ghost" @click="removeChannel(c)">删除</button>
            </div>
          </div>
        </div>
      </div>

      <!-- 调度配置 -->
      <div class="card card-pad mb-16">
        <div class="card-title">调度配置</div>
        <div class="page-desc" style="margin-top: -6px; margin-bottom: 14px">
          任务里调度模式选「跟随全局」时使用此处配置；任务自身选「自定义定时」的按任务配置执行。
        </div>
        <div class="field" style="max-width: 420px">
          <div class="field-label">全局定时调度</div>
          <div class="flex">
            <label class="switch">
              <input type="checkbox" :checked="settings.schedule_enabled !== 'off'"
                @change="settings.schedule_enabled = $event.target.checked ? 'on' : 'off'; saveSchedule()" />
              <span class="track"></span>
            </label>
            <span class="page-desc" style="margin: 0">{{ settings.schedule_enabled === 'off' ? '已关闭：所有任务仅手动运行' : '开启中：到点自动运行「跟随全局」的任务' }}</span>
          </div>
        </div>
        <div class="field" style="max-width: 560px">
          <div class="field-label">Cron 表达式（分 时 日 月 周）</div>
          <input class="input mono" v-model="settings.schedule_cron" placeholder="例如 0 */2 * * *  = 每 2 小时" :disabled="settings.schedule_enabled === 'off'" />
          <div class="flex mt-8" style="flex-wrap: wrap; gap: 6px">
            <button v-for="p in CRON_PRESETS" :key="p.value" class="btn btn-sm" :disabled="settings.schedule_enabled === 'off'" @click="settings.schedule_cron = p.value">{{ p.label }}</button>
          </div>
          <div v-if="globalCronError" class="field-hint text-danger" style="margin-top: 6px">{{ globalCronError }}</div>
          <div v-else-if="globalCronNext" class="field-hint" style="margin-top: 6px">下次执行：{{ globalCronNext }} · {{ cronToText(settings.schedule_cron) }}</div>
          <div v-else-if="settings.schedule_cron" class="field-hint text-warn" style="margin-top: 6px">表达式格式有误，暂无法计算下次执行时间。</div>
          <div class="field-hint" style="margin-top: 6px">示例：每 2 小时 <code class="mono">0 */2 * * *</code>；每天 08:00 <code class="mono">0 8 * * *</code>；周一到五 20:30 <code class="mono">30 20 * * 1-5</code></div>
        </div>
        <details class="mt-8" style="max-width: 560px">
          <summary style="cursor: pointer; font-size: 12.5px; color: var(--text-3); user-select: none">旧版配置（运行时间 + 更新日，兼容保留）</summary>
          <div class="form-row mt-8">
            <div class="field">
              <div class="field-label">运行时间</div>
              <input class="input" type="time" v-model="settings.schedule_time" :disabled="settings.schedule_enabled === 'off'" />
            </div>
            <div class="field">
              <div class="field-label">更新日（不选 = 每天）</div>
              <WeekPicker v-model="settings.schedule_week" />
            </div>
          </div>
          <div class="field-hint">填了「Cron 表达式」时以它为准；否则回退到这里的运行时间 + 更新日。Cron 优先。</div>
        </details>
        <button class="btn btn-primary" :disabled="saving" @click="saveSchedule">保存调度配置</button>
        <div class="field-hint mt-8">到点后有 1–3 分钟随机延迟；截止日期在任务里单独配置。</div>
      </div>

      <!-- 全局策略 -->
      <div class="card card-pad mb-16">
        <div class="card-title">转存策略</div>
        <div class="field" style="max-width: 420px">
          <div class="field-label">跳过已转存历史（默认值）</div>
          <select class="select" v-model="settings.skip_history_global" @change="persist({ skip_history_global: settings.skip_history_global })">
            <option value="on">开启：按转存历史去重（推荐）</option>
            <option value="off">关闭：仅按目标目录现有文件去重</option>
          </select>
          <div class="field-hint">任务里选择「跟随全局」时使用此处取值。</div>
        </div>
        <div class="field" style="max-width: 420px; margin-bottom: 0">
          <div class="field-label">运行记录保留天数</div>
          <input class="input" type="number" min="0" v-model="settings.run_retention_days"
            @change="persist({ run_retention_days: String(parseInt(settings.run_retention_days, 10) || 0) })" />
          <div class="field-hint">超期的运行记录每天自动清理（每个任务至少保留最近 50 条）；填 0 = 永久保留。转存历史（去重依据）不受影响。</div>
        </div>
      </div>

      <!-- 任务分类与回调 -->
      <div class="card card-pad mb-16">
        <div class="flex mb-16">
          <div>
            <div class="flex" style="margin-bottom: 2px">
              <div class="card-title" style="margin-bottom: 0">任务分类与回调</div>
              <button class="btn btn-sm btn-ghost" style="height: 24px" @click="showCatHelp = true">❓ 使用说明</button>
            </div>
            <div class="page-desc">给任务分组；分类可配置转存完成后回调外部接口，通知其他系统处理文件。</div>
          </div>
          <span class="flex-1"></span>
          <button class="btn btn-primary" @click="openCatNew">＋ 添加分类</button>
        </div>
        <div v-if="!categories.length" class="empty" style="padding: 22px">
          <div class="empty-hint">还没有分类。创建分类并在任务编辑里选择，即可按分类触发回调。</div>
        </div>
        <div v-else style="display: grid; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr)); gap: 10px">
          <div v-for="c in categories" :key="c.id" class="channel-card">
            <div class="flex">
              <div style="font-weight: 600">{{ c.name }}</div>
              <span class="tag">{{ c.callback_mode === 'per_task' ? '单任务回调' : '轮次回调' }}</span>
              <span class="flex-1"></span>
              <span class="badge" :class="c.callback_enabled ? 'badge-success' : 'badge-gray'"><span class="dot"></span>{{ c.callback_enabled ? '回调开启' : '仅分组' }}</span>
            </div>
            <div class="muted mono ellipsis" style="font-size: 11.5px; margin-top: 6px" :title="c.callback_url">
              {{ c.callback_enabled ? c.callback_method + ' ' + (c.callback_url || '未配置 URL') : '未启用回调' }}
            </div>
            <div class="flex" style="margin-top: 8px; gap: 6px; flex-wrap: wrap">
              <span class="muted" style="font-size: 12px">{{ c.task_count }} 个任务</span>
              <template v-if="c.last_callback_at">
                <span class="badge" :class="c.last_callback_ok ? 'badge-success' : 'badge-danger'" style="height: 18px; font-size: 11px">
                  <span class="dot"></span>{{ c.last_callback_ok ? '上次回调成功' : '上次回调失败' }} {{ c.last_callback_at }}
                </span>
              </template>
              <span class="flex-1"></span>
              <button class="btn btn-sm" @click="openCatEdit(c)">编辑</button>
              <button class="btn btn-sm" @click="openCatLogs(c)">回调日志</button>
              <button class="btn btn-sm btn-danger btn-ghost" @click="removeCat(c)">删除</button>
            </div>
          </div>
        </div>
      </div>

      <!-- 访问控制 -->
      <div class="card card-pad">
        <div class="card-title">访问控制</div>
        <div class="flex">
          <div class="flex-1">
            <div style="font-weight: 600; font-size: 13.5px">访问密码</div>
            <div class="page-desc">{{ settings.auth_enabled ? '已开启：打开管理台需要输入密码。' : '未开启：本机部署可保持关闭。' }}</div>
          </div>
          <button class="btn" @click="pwdModal = true">{{ settings.auth_enabled ? '修改密码' : '设置密码' }}</button>
          <button v-if="settings.auth_enabled" class="btn btn-danger" @click="clearPassword">关闭</button>
        </div>
      </div>
    </template>

    <!-- 渠道编辑 -->
    <Teleport to="body">
      <div v-if="showChannel" class="modal-overlay" @mousedown.self="showChannel = false">
        <div class="modal">
          <div class="modal-head">
            <div class="modal-title">{{ editingChannel ? '编辑通知渠道' : '添加通知渠道' }}</div>
            <button class="modal-close" @click="showChannel = false">✕</button>
          </div>
          <div class="modal-body">
            <div class="form-row">
              <div class="field">
                <div class="field-label">渠道类型</div>
                <select class="select" v-model="chForm.type" :disabled="editingChannel" @change="onTypeChange">
                  <option v-for="t in channelTypes" :key="t.value" :value="t.value">{{ t.label }}</option>
                </select>
              </div>
              <div class="field">
                <div class="field-label">渠道名称 <span class="req">*</span></div>
                <input class="input" v-model="chForm.name" placeholder="例如：我的 Telegram" />
              </div>
            </div>
            <div v-for="f in channelFields[chForm.type] || []" :key="f.key" class="field">
              <div class="field-label">{{ f.label }}</div>
              <input class="input mono" v-model="chForm.config[f.key]" :placeholder="f.ph || ''" />
            </div>
            <label class="checkbox">
              <input type="checkbox" v-model="chForm.enabled" />
              启用该渠道
            </label>
          </div>
          <div class="modal-foot">
            <button class="btn" :disabled="testingChannel" @click="testChannel">
              <span v-if="testingChannel" class="spinner"></span>
              <span v-else>发送测试消息</span>
            </button>
            <span class="flex-1"></span>
            <button class="btn" @click="showChannel = false">取消</button>
            <button class="btn btn-primary" :disabled="saving" @click="saveChannel">保存</button>
          </div>
        </div>
      </div>

      <!-- 密码 -->
      <div v-if="pwdModal" class="modal-overlay" @mousedown.self="pwdModal = false">
        <div class="modal" style="width: 400px">
          <div class="modal-head">
            <div class="modal-title">{{ settings.auth_enabled ? '修改访问密码' : '设置访问密码' }}</div>
            <button class="modal-close" @click="pwdModal = false">✕</button>
          </div>
          <div class="modal-body">
            <div class="field">
              <div class="field-label">新密码（至少 4 位）</div>
              <input class="input" type="password" v-model="pwd" />
            </div>
            <div class="field">
              <div class="field-label">确认密码</div>
              <input class="input" type="password" v-model="pwd2" @keyup.enter="savePassword" />
            </div>
          </div>
          <div class="modal-foot">
            <button class="btn" @click="pwdModal = false">取消</button>
            <button class="btn btn-primary" @click="savePassword">保存</button>
          </div>
        </div>
      </div>

      <!-- 分类编辑 -->
      <div v-if="showCatEdit" class="modal-overlay" @mousedown.self="showCatEdit = false">
        <div class="modal modal-lg">
          <div class="modal-head">
            <div class="modal-title">{{ editingCat ? '编辑分类' : '添加分类' }}</div>
            <button class="modal-close" @click="showCatEdit = false">✕</button>
          </div>
          <div class="modal-body">
            <div class="form-row">
              <div class="field">
                <div class="field-label">分类名称 <span class="req">*</span></div>
                <input class="input" v-model="catForm.name" placeholder="例如：Emby 刮削刷新" />
              </div>
              <div class="field">
                <div class="field-label">回调超时（秒）</div>
                <input class="input" type="number" min="1" max="120" v-model="catForm.callback_timeout_sec" />
              </div>
            </div>
            <label class="checkbox">
              <input type="checkbox" v-model="catForm.callback_enabled" />
              启用回调（关闭则仅作为任务分组，不调用接口）
            </label>
            <template v-if="catForm.callback_enabled">
              <div class="form-row">
                <div class="field">
                  <div class="field-label">触发时机</div>
                  <select class="select" v-model="catForm.callback_mode">
                    <option value="round">分类轮次完成（该类任务一批跑完后回调一次，汇总结果）</option>
                    <option value="per_task">单任务完成（每个任务转存完成即回调）</option>
                  </select>
                </div>
                <div class="field">
                  <div class="field-label">请求方法</div>
                  <select class="select" v-model="catForm.callback_method">
                    <option value="POST">POST（Body 模板）</option>
                    <option value="GET">GET（参数在 URL 上）</option>
                  </select>
                </div>
              </div>
              <div class="field">
                <div class="field-label">回调 URL <span class="req" v-if="catForm.callback_enabled">*</span>
                  <span class="flex-1"></span>
                  <span class="field-hint" style="margin-top: 0">支持 {变量}，GET 查询值自动 URL 编码；<a style="color: var(--primary); cursor: pointer" @click="showCatHelp = true">查看变量表</a></span>
                </div>
                <input class="input mono" v-model="catForm.callback_url"
                  :placeholder="catForm.callback_method === 'GET' ? 'http://192.168.1.10:8080/api/refresh?dir={save_path}&count={transfer_count}' : 'http://192.168.1.10:8080/api/hook'" />
              </div>
              <div class="field">
                <div class="field-label">自定义请求头（JSON，可选）</div>
                <input class="input mono" v-model="catForm.callback_headers" placeholder='{"Authorization": "Bearer xxx"}' />
              </div>
              <div class="field" v-if="catForm.callback_method === 'POST'">
                <div class="field-label">Body 模板（留空用默认模板）</div>
                <textarea class="textarea mono" rows="5" v-model="catForm.callback_body"
                  placeholder='{"event":"transfer.completed","category":"{category}","status":"{status}","count":{transfer_count},"files":{transferred_files_json},"path":"{save_path}"}'></textarea>
              </div>
              <div v-if="catTestResult" class="card card-pad" style="padding: 10px 12px; font-size: 12.5px"
                :style="catTestResult.ok ? 'background: var(--success-soft)' : 'background: var(--danger-soft)'">
                <div style="font-weight: 600">{{ catTestResult.ok ? '✅ 测试成功' : '❌ 测试失败' }}
                  <span v-if="catTestResult.status_code" class="muted">HTTP {{ catTestResult.status_code }}</span></div>
                <div class="mono" style="margin-top: 4px; overflow-wrap: anywhere; color: var(--text-2)">{{ catTestResult.response || catTestResult.error }}</div>
              </div>
            </template>
          </div>
          <div class="modal-foot">
            <button v-if="catForm.id" class="btn" :disabled="testingCat" @click="testCat">
              <span v-if="testingCat" class="spinner"></span>
              <span v-else>发送测试</span>
            </button>
            <span v-if="!catForm.id" class="field-hint" style="margin-top: 0">保存后可发送测试</span>
            <span class="flex-1"></span>
            <button class="btn" @click="showCatEdit = false">取消</button>
            <button class="btn btn-primary" :disabled="savingCat" @click="saveCat">保存</button>
          </div>
        </div>
      </div>

      <!-- 回调日志 -->
      <div v-if="showCatLogs" class="modal-overlay" @mousedown.self="showCatLogs = false">
        <div class="modal modal-lg">
          <div class="modal-head">
            <div class="modal-title">「{{ logsCat?.name }}」回调日志</div>
            <button class="modal-close" @click="showCatLogs = false">✕</button>
          </div>
          <div class="modal-body">
            <div v-if="catLogsLoading" class="loading-block"><span class="spinner"></span></div>
            <div v-else-if="!catLogs.length" class="empty">
              <div class="empty-title">还没有回调记录</div>
              <div class="empty-hint">分类回调触发后会在这里记录每次调用的结果</div>
            </div>
            <div v-else style="display: flex; flex-direction: column; gap: 8px">
              <div v-for="l in catLogs" :key="l.id" class="card card-pad" style="padding: 10px 12px">
                <div class="flex" style="flex-wrap: wrap; gap: 6px">
                  <span class="badge" :class="l.ok ? 'badge-success' : 'badge-danger'"><span class="dot"></span>{{ l.ok ? '成功' : '失败' }}</span>
                  <span v-if="l.status_code" class="badge badge-gray">HTTP {{ l.status_code }}</span>
                  <span class="tag">{{ l.kind === 'round' ? '轮次' : l.kind === 'test' ? '测试' : '单任务' }}</span>
                  <span class="muted" style="font-size: 12px">{{ fmtTime(l.created_at) }}</span>
                </div>
                <div class="mono ellipsis" style="font-size: 11.5px; margin-top: 6px" :title="l.target">{{ l.target }}</div>
                <pre v-if="l.response" class="log-tree" style="margin-top: 6px; max-height: 120px; padding: 8px 10px">{{ l.response }}</pre>
              </div>
            </div>
          </div>
          <div class="modal-foot">
            <span class="flex-1"></span>
            <button class="btn" @click="showCatLogs = false">关闭</button>
          </div>
        </div>
      </div>

      <!-- 分类回调使用说明 -->
      <div v-if="showCatHelp" class="modal-overlay" @mousedown.self="showCatHelp = false">
        <div class="modal modal-lg">
          <div class="modal-head">
            <div class="modal-title">任务分类与回调 · 使用说明</div>
            <button class="modal-close" @click="showCatHelp = false">✕</button>
          </div>
          <div class="modal-body">
            <div style="font-size: 13.5px; line-height: 1.8; color: var(--text-2)">
              <p style="margin: 0 0 10px"><b style="color: var(--text)">这是什么：</b>把任务归入分类，转存完成后自动按分类配置调用你的接口，让其他系统（媒体库刷新、字幕下载、文件整理脚本…）接着处理文件。</p>
              <p style="margin: 0 0 10px"><b style="color: var(--text)">触发时机：</b></p>
              <ul style="margin: 0 0 10px; padding-left: 20px">
                <li><b>单任务完成</b>：该分类下每个任务转存完成就回调一次，参数里带这个任务的结果。</li>
                <li><b>分类轮次完成</b>：一批定时触发（或一次手动运行）中，该分类全部已启动任务都结束后回调<b>一次</b>，参数汇总每个任务的结果。定时批量任务间有 1–3 分钟随机延迟，轮次会等齐再回调。</li>
              </ul>
              <p style="margin: 0 0 10px"><b style="color: var(--text)">可用变量（URL 和 Body 模板里用 <code class="mono">{变量名}</code> 引用）：</b></p>
              <div class="table-wrap" style="margin-bottom: 10px">
                <table class="table">
                  <thead><tr><th style="width: 190px">变量</th><th>说明</th></tr></thead>
                  <tbody>
                    <tr><td class="mono">{task_name}</td><td>任务名称（轮次模式下无单一任务概念，用 task_results_json）</td></tr>
                    <tr><td class="mono">{task_id}</td><td>任务 ID</td></tr>
                    <tr><td class="mono">{category}</td><td>分类名称</td></tr>
                    <tr><td class="mono">{status}</td><td>success / partial / failed（轮次模式为汇总状态）</td></tr>
                    <tr><td class="mono">{transfer_count}</td><td>本次新转存文件数</td></tr>
                    <tr><td class="mono">{transferred_files_json}</td><td>新文件名 JSON 数组，如 ["a.mkv","b.mkv"]</td></tr>
                    <tr><td class="mono">{transferred_files}</td><td>新文件名，换行分隔</td></tr>
                    <tr><td class="mono">{save_path}</td><td>保存路径</td></tr>
                    <tr><td class="mono">{trigger}</td><td>manual（手动）/ schedule（定时）</td></tr>
                    <tr><td class="mono">{finished_at}</td><td>完成时间</td></tr>
                    <tr><td class="mono">{message}</td><td>结果摘要（如「转存 3 个文件」）</td></tr>
                    <tr><td class="mono">{task_results_json}</td><td>仅轮次模式：每个任务结果的 JSON 数组</td></tr>
                    <tr><td class="mono">{round_id} / {task_count} / {total_transferred}</td><td>仅轮次模式：轮次 ID / 任务数 / 总转存数</td></tr>
                  </tbody>
                </table>
              </div>
              <p style="margin: 0 0 10px"><b style="color: var(--text)">示例：</b></p>
              <pre class="log-tree" style="margin-bottom: 10px">GET  http://192.168.1.10:8080/api/refresh?dir={save_path}&amp;count={transfer_count}

POST Body（默认模板，可自行修改）：
{"event":"transfer.completed","category":"{category}","status":"{status}","count":{transfer_count},"files":{transferred_files_json},"path":"{save_path}"}</pre>
              <p style="margin: 0"><b style="color: var(--text)">注意事项：</b>HTTP 2xx 视为成功；超时默认 15 秒（可配）；调用失败不自动重试，结果记录在「回调日志」里；请求头（可能含密钥）明文存于本地数据库；服务重启会丢失进行中的轮次；下游接口建议使用内网地址。</p>
            </div>
          </div>
          <div class="modal-foot">
            <span class="flex-1"></span>
            <button class="btn" @click="showCatHelp = false">我知道了</button>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>
