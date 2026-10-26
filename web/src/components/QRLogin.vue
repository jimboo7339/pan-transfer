<script setup>
// 扫码登录弹窗：开始 → 轮询 → 回填账号配置。
import { ref, inject, onBeforeUnmount } from 'vue'
import Modal from './Modal.vue'
import { accountApi } from '../api'

const toast = inject('toast')

const visible = ref(false)
const qrImg = ref('')
const qrHint = ref('')
const qrTitle = ref('扫码登录')
const status = ref('') // waiting/success/failed/expired
const polling = ref(false)
let pollTimer = null
let qrToken = ''
let lastType = ''

const emit = defineEmits(['success'])

function open(driverType) {
  visible.value = true
  lastType = driverType
  start(driverType)
}

async function start(driverType) {
  status.value = ''
  qrImg.value = ''
  qrHint.value = '正在获取二维码…'
  try {
    const res = await accountApi.qrStart(driverType)
    qrToken = res.token
    qrImg.value = res.qr_image_base64
    qrTitle.value = res.title || '扫码登录'
    qrHint.value = res.hint || '请使用网盘 App 扫码并确认登录'
    status.value = 'waiting'
    schedulePoll(driverType)
  } catch (e) {
    qrHint.value = ''
    status.value = 'failed'
    toast(e.message, 'error')
  }
}

function schedulePoll(driverType) {
  stopPoll()
  polling.value = true
  const tick = async () => {
    if (!visible.value || !qrToken) return
    try {
      const res = await accountApi.qrPoll(qrToken)
      if (res.status === 'waiting') {
        pollTimer = setTimeout(tick, 2000)
        return
      }
      if (res.status === 'success' && res.config_json) {
        status.value = 'success'
        qrHint.value = '登录成功！'
        emit('success', JSON.parse(res.config_json))
        setTimeout(() => { visible.value = false }, 800)
        return
      }
      status.value = res.status
      qrHint.value = res.status === 'expired' ? '二维码已过期' : (res.message || '登录失败')
    } catch (e) {
      status.value = 'failed'
      qrHint.value = e.message
      return
    }
  }
  pollTimer = setTimeout(tick, 1800)
}

function stopPoll() {
  if (pollTimer) { clearTimeout(pollTimer); pollTimer = null }
}

function close() {
  stopPoll()
  qrToken = ''
  visible.value = false
}

defineExpose({ open })
onBeforeUnmount(stopPoll)
</script>

<template>
  <Teleport to="body">
    <div v-if="visible" class="modal-overlay" @mousedown.self="close">
      <div class="modal" style="width: 400px">
        <div class="modal-head">
          <div class="modal-title">{{ qrTitle }}</div>
          <button class="modal-close" @click="close">✕</button>
        </div>
        <div class="modal-body">
          <div class="qr-box">
            <div v-if="!qrImg" class="loading-block"><span class="spinner"></span></div>
            <img v-else class="qr-img" :src="qrImg" alt="二维码" />
            <div class="qr-hint">
              {{ qrHint }}
              <div v-if="status === 'waiting'" class="flex" style="justify-content: center; margin-top: 8px">
                <span class="spinner"></span><span class="muted" style="font-size: 12px">等待扫码确认…</span>
              </div>
              <div v-if="status === 'expired' || status === 'failed'" style="margin-top: 10px">
                <button class="btn btn-sm" @click="start(lastType)">刷新二维码</button>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  </Teleport>
</template>
