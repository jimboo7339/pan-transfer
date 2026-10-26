<script setup>
// 分享链接预览弹窗：目录导航 + 重命名预览（对应 CASX 的"分享链接预览"）。
import { ref, inject } from 'vue'
import Modal from './Modal.vue'
import { shareApi, fmtSize, fmtTime } from '../api'

const toast = inject('toast')

const visible = ref(false)
const loading = ref(false)
const items = ref([])
const pwdID = ref('')
const pdirFID = ref('')
const trail = ref([]) // [{fid, name}]
const total = ref(0)
const truncated = ref(false)
const curDirName = ref('根目录')

// 预览参数（父组件通过 open 传入）
let params = null
const emit = defineEmits(['use-folder'])

function open(p) {
  params = p
  trail.value = []
  visible.value = true
  load('')
}
defineExpose({ open })

async function load(fid) {
  loading.value = true
  try {
    const res = await shareApi.preview({ ...params, pdir_fid: fid, max_items: 500 })
    items.value = res.items || []
    pwdID.value = res.pwd_id
    pdirFID.value = res.pdir_fid
    total.value = res.total
    truncated.value = res.truncated
  } catch (e) {
    toast(e.message, 'error')
    items.value = []
  } finally {
    loading.value = false
  }
}

function enterDir(item) {
  if (!item.is_dir) return
  trail.value.push({ fid: pdirFID.value, name: curDirName.value })
  curDirName.value = item.name
  load(item.fid)
}

function jumpTo(idx) {
  // -1 = 根目录
  if (idx < 0) {
    trail.value = []
    curDirName.value = '根目录'
    load('')
    return
  }
  const target = trail.value[idx]
  trail.value = trail.value.slice(0, idx)
  curDirName.value = target.name
  load(target.fid)
}

function useCurrentFolder() {
  emit('use-folder', { pdir_fid: pdirFID.value, name: curDirName.value })
  visible.value = false
}
</script>

<template>
  <Teleport to="body">
    <div v-if="visible" class="modal-overlay" @mousedown.self="visible = false">
      <div class="modal modal-xl">
        <div class="modal-head">
          <div class="modal-title">分享链接预览 <span class="muted" style="font-size: 13px; font-weight: 400">（共 {{ total }} 个文件）</span></div>
          <button class="modal-close" @click="visible = false">✕</button>
        </div>
        <div class="modal-body" style="padding-top: 14px">
          <div class="flex mb-16" style="justify-content: space-between; flex-wrap: wrap">
            <div class="dir-crumb">
              <a @click="jumpTo(-1)">🏠 根目录</a>
              <template v-for="(t, i) in trail" :key="i">
                <span class="muted">/</span>
                <a @click="jumpTo(i)">{{ t.name }}</a>
              </template>
              <span class="muted">/</span>
              <span class="cur">{{ curDirName }}</span>
            </div>
            <button class="btn btn-primary btn-sm" @click="useCurrentFolder" v-if="trail.length">使用当前文件夹</button>
          </div>

          <div v-if="loading" class="loading-block"><span class="spinner"></span> 加载中…</div>
          <div v-else-if="!items.length" class="empty">
            <div class="empty-title">此目录没有文件</div>
          </div>
          <div v-else class="table-wrap">
            <table class="table">
              <thead>
                <tr>
                  <th style="min-width: 260px">名称</th>
                  <th style="width: 90px">大小</th>
                  <th style="min-width: 220px">重命名预览</th>
                  <th style="width: 150px">时间</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="it in items" :key="it.fid">
                  <td>
                    <div class="preview-filename" :style="it.is_dir ? 'cursor: pointer; font-weight: 500' : ''" @click="enterDir(it)">
                      <svg v-if="it.is_dir" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="var(--warn)" stroke-width="2"><path d="M3 7a2 2 0 012-2h4l2 2h8a2 2 0 012 2v9a2 2 0 01-2 2H5a2 2 0 01-2-2z"/></svg>
                      <svg v-else width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z"/><path d="M14 2v6h6"/></svg>
                      <span class="nm" :title="it.name">{{ it.name }}</span>
                    </div>
                    <span v-if="it.name_saved" class="saved-note">⏸ {{ it.name_saved }}</span>
                  </td>
                  <td class="num">{{ it.is_dir ? '-' : fmtSize(it.size) }}</td>
                  <td>
                    <span v-if="it.name_re && it.name_re !== it.name" class="text-success">{{ it.name_re }}</span>
                    <span v-else-if="it.name_re" class="muted">{{ it.name_re }}</span>
                    <span v-else class="muted">—</span>
                  </td>
                  <td class="num muted">{{ it.updated_at ? fmtTime(it.updated_at * 1000) : '-' }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <div v-if="truncated" class="field-hint mt-8">⚠ 条目过多，仅展示前 {{ items.length }} 项（原 {{ total }} 项）。</div>
        </div>
        <div class="modal-foot">
          <span class="flex-1"></span>
          <button class="btn" @click="visible = false">关闭</button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
