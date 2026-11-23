<script setup>
// 访问控制：访问密码（设置 / 修改 / 关闭）与退出登录。
//
// 这里有两个需要小心的地方：
//   1. 密码长度必须真的校验，而不是只在文案里写「至少 4 位」。
//      后端登录限流是 5 次 / 5 分钟，1 位口令（10 种可能）在限流下
//      也能被很快试出来。后端已有校验（server.go「密码至少 4 位」），
//      前端补上是为了即时反馈，而不是提交后才报错。
//   2. 关闭密码 / 退出登录都要二次确认，且用 danger 样式：
//      这两个操作都会把「谁能访问你的网盘账号」这件事改变。
import { ref, inject } from 'vue'
import Modal from '../Modal.vue'
import { authApi, setToken } from '../../api'

const props = defineProps({
  settings: { type: Object, required: true },
})
const emit = defineEmits(['save', 'refresh-session'])

const toast = inject('toast')
const confirmDialog = inject('confirmDialog')

const pwdModal = ref(false)
const pwd = ref('')
const pwd2 = ref('')
const MIN_PASSWORD_LEN = 4

async function savePassword() {
  if (pwd.value !== pwd2.value) { toast('两次输入的密码不一致', 'error'); return }
  if (pwd.value.length < MIN_PASSWORD_LEN) {
    toast(`密码至少 ${MIN_PASSWORD_LEN} 位`, 'error')
    return
  }
  emit('save', { password: pwd.value }, () => {
    pwd.value = pwd2.value = ''
    pwdModal.value = false
    emit('refresh-session')
  })
}

async function clearPassword() {
  const ok = await confirmDialog({
    title: '关闭访问密码',
    body: '关闭后任何能访问本服务的人都可以操作你的网盘账号。',
    warn: '若服务暴露在局域网或公网（含反向代理），这一步等于把账号控制权开放给所有人。',
    confirmText: '关闭密码',
    variant: 'danger',
  })
  if (!ok) return
  emit('save', { password: '' }, () => {
    pwdModal.value = false
    emit('refresh-session')
  })
}

// doLogout 退出登录。
//
// 服务端会作废此前签发的全部会话（token 是无状态的，只清 Cookie 无法让
// 已经泄露的 token 失效）。因此登出后必须清掉本地 token 并回到登录态。
// 服务端不可达时也要本地登出：留着 token 只会反复 401。
async function doLogout() {
  const ok = await confirmDialog({
    title: '退出登录',
    body: '退出后需要重新输入访问密码。',
    warn: '此操作会同时作废此前签发的所有会话令牌（包括其他设备上的登录）。',
    confirmText: '退出',
  })
  if (!ok) return
  try {
    await authApi.logout()
  } catch (e) {
    toast('服务端登出失败，已本地退出：' + e.message, 'error')
  }
  setToken('')
  await emit('refresh-session')
}

defineExpose({ pwdModal })
</script>

<template>
  <div class="card card-pad mb-16">
    <div class="card-title">访问控制</div>
    <div class="flex">
      <div class="flex-1">
        <div style="font-weight: 600; font-size: 13.5px">访问密码</div>
        <div class="page-desc">{{ settings.auth_enabled ? '已开启：打开管理台需要输入密码。' : '未开启：本机部署可保持关闭。' }}</div>
      </div>
      <button class="btn" @click="pwdModal = true">{{ settings.auth_enabled ? '修改密码' : '设置密码' }}</button>
      <button v-if="settings.auth_enabled" class="btn btn-danger" @click="clearPassword">关闭</button>
    </div>
    <div v-if="settings.auth_enabled" class="field-hint mt-16" style="margin-bottom: 12px">
      密码以 argon2id 哈希存储；修改密码会作废此前签发的所有会话。
    </div>
    <div v-if="settings.auth_enabled" class="flex">
      <button class="btn btn-sm" @click="doLogout">退出登录</button>
      <span class="field-hint" style="margin-top: 0">
        会同时作废所有设备上的会话令牌，用于「怀疑密码泄露」时强制下线。
      </span>
    </div>

    <!-- 密码弹层 -->
    <Modal v-model="pwdModal" :title="settings.auth_enabled ? '修改访问密码' : '设置访问密码'">
      <div class="field">
        <div class="field-label">新密码（至少 {{ MIN_PASSWORD_LEN }} 位）</div>
        <input class="input" type="password" v-model="pwd" />
      </div>
      <div class="field">
        <div class="field-label">确认密码</div>
        <input class="input" type="password" v-model="pwd2" @keyup.enter="savePassword" />
      </div>
      <template #footer>
        <button class="btn" @click="pwdModal = false">取消</button>
        <button class="btn btn-primary" @click="savePassword">保存</button>
      </template>
    </Modal>
  </div>
</template>