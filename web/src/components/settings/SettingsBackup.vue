<script setup>
// 数据备份与配置迁移。
//
// 这两件事放在一起是因为语义相邻（都是"把数据带走"），
// 但必须让用户清楚区别：
//   - 数据备份：连凭据一起打包（密文），用于灾难恢复；
//   - 配置迁移：不含凭据，用于换机 / 多机同步，可安全分享。
// 早先这两块在 Settings.vue 里，与访问控制、通知渠道混在一起。
import { ref, inject, computed, onMounted } from 'vue'
import ConfigTransfer from '../ConfigTransfer.vue'
import { backupApi, fmtTime, fmtSize } from '../../api'

const toast = inject('toast')
const confirmDialog = inject('confirmDialog')

const backups = ref([])
const backupsLoading = ref(false)
const backingUp = ref(false)
const configRef = ref(null)

async function loadBackups() {
  backupsLoading.value = true
  try {
    const r = await backupApi.list()
    backups.value = r?.backups || []
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    backupsLoading.value = false
  }
}

async function createBackup() {
  backingUp.value = true
  try {
    const r = await backupApi.create()
    toast(r?.message || '备份已完成', 'success', 5000)
    loadBackups()
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    backingUp.value = false
  }
}

async function restoreBackup(b) {
  const ok = await confirmDialog({
    title: '从备份还原',
    body: `确定用备份「${b.name}」覆盖当前数据库？`,
    warn: '还原会丢弃还原点之后的所有任务、运行记录与转存历史，且需要重启调度。此操作不可撤销。',
    confirmText: '确认还原',
    variant: 'danger',
    requireText: b.name,
  })
  if (!ok) return
  try {
    await backupApi.restore(b.name)
    toast('已还原，正在重新加载…', 'success', 5000)
    // 还原后服务端换掉了数据库句柄，刷新一次拿到新状态。
    setTimeout(() => window.location.reload(), 1500)
  } catch (e) {
    toast(e.message, 'error', 6000)
  }
}

const newestBackup = computed(() => backups.value[0] || null)

onMounted(loadBackups)
</script>

<template>
  <div class="card card-pad">
    <div class="flex mb-16">
      <div>
        <div class="card-title" style="margin-bottom: 2px">数据备份</div>
        <div class="page-desc">
          备份包含账号凭据、任务、运行记录与转存历史（<b>凭据是加密后的密文</b>，
          但与主密钥一起才能用；<code class="mono">master.key</code> 也要一并保存）。
          服务每天自动备份一次，保留最近 7 份。
        </div>
      </div>
      <span class="flex-1"></span>
      <button class="btn btn-primary" :disabled="backingUp" @click="createBackup">
        <span v-if="backingUp" class="spinner"></span>
        <span v-else>＋ 立即备份</span>
      </button>
    </div>
    <div v-if="backupsLoading" class="loading-block"><span class="spinner"></span></div>
    <div v-else-if="!backups.length" class="empty" style="padding: 22px">
      <div class="empty-hint">还没有备份。建议立即创建一份（首次启动后 5 分钟也会自动创建）。</div>
    </div>
    <div v-else class="table-wrap">
      <table class="table">
        <thead>
          <tr>
            <th>备份文件</th>
            <th style="width: 100px">大小</th>
            <th style="width: 150px">时间</th>
            <th style="width: 200px">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="b in backups" :key="b.name">
            <td>
              <code class="mono">{{ b.name }}</code>
              <span v-if="b.name === newestBackup?.name" class="tag" style="margin-left: 6px">最新</span>
            </td>
            <td class="num muted">{{ fmtSize(b.size) }}</td>
            <td class="num muted">{{ fmtTime(b.mod_time) }}</td>
            <td>
              <a class="btn btn-sm" :href="backupApi.downloadUrl(b.name)" download>下载</a>
              <button class="btn btn-sm btn-danger btn-ghost" @click="restoreBackup(b)">还原</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- 配置导入 / 导出：与备份是一对，但语义不同 -->
    <div class="config-transfer-row">
      <div>
        <div style="font-weight: 600; font-size: 13.5px">配置迁移</div>
        <div class="page-desc">
          把任务、命名规则、分类回调打包成 JSON，用于换机、重装或多机同步。
          <b>不包含凭据</b>，可安全分享；凭据请走上面的「数据备份」。
        </div>
      </div>
      <span class="flex-1"></span>
      <button class="btn" @click="configRef.open()">导入 / 导出配置</button>
    </div>

    <ConfigTransfer ref="configRef" />
  </div>
</template>