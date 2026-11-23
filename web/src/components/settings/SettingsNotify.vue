<script setup>
// 通知推送：渠道列表 + 添加/编辑弹层 + 发送测试。
//
// 这是设置页里状态最多的一个区块：
//   - chForm：编辑中的渠道（有 7 种类型，每种字段不同）
//   - channelFields：类型 → 字段 schema
//   - 保存失败时要保持弹层打开（否则用户填的配置全丢）
import { ref, reactive, computed } from 'vue'
import Modal from '../Modal.vue'
import { notifyApi } from '../../api'
import { inject } from 'vue'

const props = defineProps({
  settings: { type: Object, required: true },
  saving: { type: Boolean, default: false },
})
const emit = defineEmits(['save'])

const toast = inject('toast')
const confirmDialog = inject('confirmDialog')

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

const typeLabel = (v) => channelTypes.find(t => t.value === v)?.label || v

function openAddChannel() {
  editingChannel.value = false
  Object.assign(chForm, { id: 'ch_' + Date.now(), type: 'telegram', name: '', enabled: true, config: {} })
  showChannel.value = true
}

function openEditChannel(c) {
  editingChannel.value = true
  // 深拷贝：直接 Object.assign(c) 会让编辑弹层改到列表里的对象，
  // 用户点"取消"后列表已经变了。
  Object.assign(chForm, JSON.parse(JSON.stringify(c)))
  showChannel.value = true
}

function onTypeChange() { chForm.config = {} }

async function saveChannel() {
  if (!chForm.name.trim()) { toast('请填写渠道名称', 'error'); return }
  const list = [...props.settings.notify_channels]
  const idx = list.findIndex(c => c.id === chForm.id)
  if (idx >= 0) list[idx] = { ...chForm }
  else list.push({ ...chForm })
  // 保存失败时保持弹层打开：否则用户填的渠道配置全部丢失，
  // 只看到一条转瞬即逝的错误提示。
  emit('save', { notify_channels: list }, () => {
    showChannel.value = false
  })
}

async function removeChannel(c) {
  const ok = await confirmDialog({
    title: '删除通知渠道',
    body: `确定删除通知渠道「${c.name}」？`,
    warn: '删除后该渠道不再接收任何转存通知。',
    confirmText: '删除',
    variant: 'danger',
  })
  if (!ok) return
  emit('save', { notify_channels: props.settings.notify_channels.filter(x => x.id !== c.id) })
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
</script>

<template>
  <div class="card card-pad mb-16">
    <div class="flex mb-16">
      <div>
        <div class="card-title" style="margin-bottom: 2px">通知推送</div>
        <div class="page-desc">
          有新文件转存或整次运行失败时推送；本次 0 个新文件（无更新 / 全部已转存）时静默，不推送空跑结果。
        </div>
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
          <span class="badge" :class="c.enabled ? 'badge-success' : 'badge-gray'">
            <span class="dot"></span>{{ c.enabled ? '启用' : '停用' }}
          </span>
        </div>
        <div class="flex">
          <button class="btn btn-sm" @click="openEditChannel(c)">编辑</button>
          <span class="spacer"></span>
          <button class="btn btn-sm btn-danger btn-ghost" @click="removeChannel(c)">删除</button>
        </div>
      </div>
    </div>

    <!-- 渠道编辑 -->
    <Modal v-model="showChannel" :title="editingChannel ? '编辑通知渠道' : '添加通知渠道'">
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
      <template #footer>
        <button class="btn" :disabled="testingChannel" @click="testChannel">
          <span v-if="testingChannel" class="spinner"></span>
          <span v-else>发送测试消息</span>
        </button>
        <span class="flex-1"></span>
        <button class="btn" @click="showChannel = false">取消</button>
        <button class="btn btn-primary" :disabled="saving" @click="saveChannel">保存</button>
      </template>
    </Modal>
  </div>
</template>