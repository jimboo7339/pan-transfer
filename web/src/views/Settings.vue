<script setup>
// 设置：通知渠道管理 + 全局开关 + 访问密码。
import Modal from '../components/Modal.vue'
import WeekPicker from '../components/WeekPicker.vue'
import { settingsApi, notifyApi, authApi, cronApi } from '../api'
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
    </Teleport>
  </div>
</template>
