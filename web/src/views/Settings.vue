<script setup>
// 设置：通知渠道管理 + 全局开关 + 访问密码 + 数据备份。
//
// 这个文件是 6 个互不相关的区块的容器，每个区块已拆成
// components/settings/ 下的独立组件：
//
//   SettingsNotify.vue      通知渠道（列表 + 编辑弹层）
//   SettingsSchedule.vue    调度配置（开关 + cron + 旧版时间/更新日）
//   SettingsPolicy.vue      转存策略（去重 / 保留天数 / 请求间隔）
//   SettingsCategory.vue    任务分类与回调（列表 + 编辑弹层 + 日志 + 帮助）
//   SettingsAccess.vue      访问控制（密码 / 退出登录）
//   SettingsBackup.vue      数据备份与配置迁移
//
// 为什么拆：这个文件曾同时承载 6 个业务域，改任何一个都要在 800 多行里
// 找位置，且任何两个域的状态混在同一个 setup 作用域里，
// 变量名互相干扰（chForm / catForm / pwd / backups…）。
//
// 拆分后容器只保留：
//   - 共享的 settings 对象（后端返回的全局配置）
//   - persist(patch)：唯一的写库入口，返回是否成功
//   - 加载态 / 失败态（整个页面只有一个 spinner / 一个 ErrorState）
//
// 各子组件通过 props 拿 settings 与 saving，通过 emit 请求保存，
// 自己管自己的弹层与临时表单状态——这正是"改通知渠道不会碰到备份代码"的前提。
import { ref, reactive, inject, onMounted } from 'vue'
import ErrorState from '../components/ErrorState.vue'
import SettingsNotify from '../components/settings/SettingsNotify.vue'
import SettingsSchedule from '../components/settings/SettingsSchedule.vue'
import SettingsPolicy from '../components/settings/SettingsPolicy.vue'
import SettingsCategory from '../components/settings/SettingsCategory.vue'
import SettingsAccess from '../components/settings/SettingsAccess.vue'
import SettingsBackup from '../components/settings/SettingsBackup.vue'
import { settingsApi } from '../api'

const toast = inject('toast')
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
  request_interval_ms: 300,
  auth_enabled: false,
})

// reload 加载全局设置。
//
// 加载失败必须能与「设置都是默认值」区分开：
// 早先失败只 toast，然后 settings 保持初始默认值渲染出完整表单，
// 用户会以为"我的配置就是这样"，点保存就把默认值写回库、覆盖真实配置。
const loadError = ref(null)

async function reload() {
  loading.value = true
  loadError.value = null
  try {
    const s = await settingsApi.get()
    Object.assign(settings, s)
  } catch (e) {
    loadError.value = e
  } finally {
    loading.value = false
  }
}
onMounted(reload)

// persist 保存一批设置，成功返回 true、失败返回 false。
//
// 为什么返回布尔而不是静默吞异常：调用方（如通知渠道编辑）需要在失败后
// **保持弹层打开**，否则用户填的整个渠道配置会丢失，
// 只看到一条转瞬即逝的红色 toast。
//
// 这是**唯一**的写库入口：所有子组件的保存都走这里，
// 因此"保存后回填最新值"这件事只有一处实现。
async function persist(patch) {
  saving.value = true
  try {
    await settingsApi.save(patch)
    Object.assign(settings, await settingsApi.get())
    toast('设置已保存', 'success')
    return true
  } catch (e) {
    toast(e.message, 'error')
    return false
  } finally {
    saving.value = false
  }
}

// applySave 是子组件统一的保存入口：patch 是要写的字段，done 是可选的
// 成功回调（子组件用它关闭自己的弹层）。
//
// 为什么让子组件传 done 而不是各自调 API：
// 「保存后回填最新值」这件事只有 persist 一处实现，
// 子组件不需要知道 settingsApi 的存在。
function applySave(patch, done) {
  return persist(patch).then((ok) => {
    if (ok && typeof done === 'function') done()
    return ok
  })
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

    <!-- 加载失败：必须阻止用户在这个状态下点保存。
         否则表单显示的是初始默认值，保存会把真实配置覆盖掉。 -->
    <ErrorState v-else-if="loadError" :error="loadError" :on-retry="reload"
      hint="设置加载失败，为避免用默认值覆盖你的真实配置，已禁用保存。请确认服务正在运行后重试。" />

    <template v-else>
      <!-- 通知渠道：自己管 chForm / showChannel，保存走 emit -->
      <SettingsNotify :settings="settings" :saving="saving"
        @save="applySave" />

      <SettingsSchedule :settings="settings" :saving="saving"
        @save="applySave" @invalid="(msg) => toast(msg, 'error')" />

      <SettingsPolicy :settings="settings" :saving="saving"
        @save="applySave" />

      <!-- 分类与回调：自己拉列表、自己管 3 个弹层 -->
      <SettingsCategory />

      <SettingsAccess :settings="settings"
        @save="applySave" @refresh-session="refreshSession" />

      <SettingsBackup />
    </template>
  </div>
</template>
