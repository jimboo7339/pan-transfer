<script setup>
// 任务运行记录 + 转存文件历史。
import { ref, inject } from 'vue'
import Modal from './Modal.vue'
import { taskApi, fmtTime } from '../api'

const toast = inject('toast')

const visible = ref(false)
const tab = ref('runs')
const loading = ref(false)
const task = ref(null)
const runs = ref([])
const saved = ref([])

function open(t) {
  task.value = t
  visible.value = true
  tab.value = 'runs'
  reload()
}
defineExpose({ open })

async function reload() {
  if (!task.value) return
  loading.value = true
  try {
    if (tab.value === 'runs') runs.value = (await taskApi.runs(task.value.id)) || []
    else saved.value = (await taskApi.savedNames(task.value.id)) || []
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    loading.value = false
  }
}

function switchTab(t) {
  tab.value = t
  reload()
}
</script>

<template>
  <Teleport to="body">
    <div v-if="visible" class="modal-overlay" @mousedown.self="visible = false">
      <div class="modal modal-lg">
        <div class="modal-head">
          <div class="modal-title">「{{ task?.task_name }}」运行记录</div>
          <button class="modal-close" @click="visible = false">✕</button>
        </div>
        <div class="modal-body">
          <div class="tabs">
            <button class="tab" :class="{ active: tab === 'runs' }" @click="switchTab('runs')">运行记录</button>
            <button class="tab" :class="{ active: tab === 'saved' }" @click="switchTab('saved')">已转存文件</button>
          </div>

          <div v-if="loading" class="loading-block"><span class="spinner"></span></div>

          <template v-else-if="tab === 'runs'">
            <div v-if="!runs.length" class="empty">
              <div class="empty-title">还没有运行记录</div>
              <div class="empty-hint">点击任务卡片上的「运行」立即执行一次</div>
            </div>
            <div v-else style="display: flex; flex-direction: column; gap: 10px">
              <div v-for="r in runs" :key="r.id" class="card card-pad" style="padding: 12px 14px">
                <div class="flex" style="justify-content: space-between">
                  <div class="flex">
                    <span class="badge" :class="{
                      'badge-success': r.status === 'success',
                      'badge-danger': r.status === 'failed',
                      'badge-warn': r.status === 'partial',
                      'badge-gray': r.status === 'running' || r.status === 'skipped',
                    }"><span class="dot"></span>{{ r.status }}</span>
                    <span class="muted" style="font-size: 12.5px">{{ fmtTime(r.started_at) }}</span>
                    <span v-if="r.transfer_count" class="badge badge-primary">+{{ r.transfer_count }} 文件</span>
                  </div>
                  <span class="mono muted ellipsis" style="max-width: 260px" :title="r.link">{{ r.link }}</span>
                </div>
                <div v-if="r.message" class="mt-8" style="font-size: 12.5px; color: var(--text-2)">{{ r.message }}</div>
                <pre v-if="r.tree_summary" class="log-tree mt-8" style="max-height: 200px">{{ r.tree_summary }}</pre>
              </div>
            </div>
          </template>

          <template v-else>
            <div v-if="!saved.length" class="empty">
              <div class="empty-title">暂无转存记录</div>
              <div class="empty-hint">成功转存的文件会记录在此，用于按名跳过重复转存</div>
            </div>
            <div v-else class="table-wrap">
              <table class="table">
                <thead><tr><th>文件名</th><th style="width: 160px">转存时间</th></tr></thead>
                <tbody>
                  <tr v-for="s in saved" :key="s.id">
                    <td class="ellipsis" :title="s.file_name">{{ s.file_name }}</td>
                    <td class="num muted">{{ fmtTime(s.saved_at) }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </template>
        </div>
      </div>
    </div>
  </Teleport>
</template>
