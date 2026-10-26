<script setup>
// 网盘目录浏览器：选择保存路径 / 新建文件夹。
import { ref, inject } from 'vue'
import Modal from './Modal.vue'
import { fileApi } from '../api'

const toast = inject('toast')

const visible = ref(false)
const loading = ref(false)
const accountId = ref(0)
const fid = ref('0')
const trail = ref([]) // [{fid, name}]
const items = ref([])
const creating = ref(false)
const newName = ref('')

const emit = defineEmits(['picked'])

function open(accId, initialPath = '') {
  accountId.value = accId
  visible.value = true
  trail.value = []
  if (initialPath) {
    // 从路径解析起点目录
    fileApi.resolve(accId, initialPath).then(res => {
      fid.value = res.fid
      trail.value = [{ fid: '0', name: '根目录' }]
      load(fid.value)
    }).catch(() => { fid.value = '0'; load('0') })
  } else {
    fid.value = '0'
    load('0')
  }
}
defineExpose({ open })

async function load(target) {
  loading.value = true
  try {
    items.value = (await fileApi.list(accountId.value, target)) || []
    items.value = items.value.filter(i => i.is_dir)
    fid.value = target
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    loading.value = false
  }
}

function enter(item) {
  trail.value.push({ fid: fid.value, name: item.name })
  load(item.id)
}

function jump(idx) {
  if (idx < 0) { trail.value = []; load('0'); return }
  const t = trail.value[idx]
  trail.value = trail.value.slice(0, idx)
  load(t.fid)
}

async function mkdir() {
  if (!newName.value.trim()) return
  creating.value = true
  try {
    await fileApi.mkdir(accountId.value, fid.value, newName.value.trim())
    toast('文件夹已创建', 'success')
    newName.value = ''
    load(fid.value)
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    creating.value = false
  }
}

function pick() {
  const path = '/' + trail.value.map(t => t.name).filter(n => n && n !== '根目录').join('/')
  emit('picked', { path: path === '/' ? '' : path, fid: fid.value })
  visible.value = false
}
</script>

<template>
  <Teleport to="body">
    <div v-if="visible" class="modal-overlay" @mousedown.self="visible = false">
      <div class="modal modal-lg">
        <div class="modal-head">
          <div class="modal-title">选择保存目录</div>
          <button class="modal-close" @click="visible = false">✕</button>
        </div>
        <div class="modal-body">
          <div class="dir-crumb mb-8">
            <a @click="jump(-1)">🏠 根目录</a>
            <template v-for="(t, i) in trail" :key="i">
              <span class="muted">/</span>
              <a @click="jump(i)">{{ t.name }}</a>
            </template>
          </div>
          <div class="fb-list">
            <div v-if="loading" class="loading-block"><span class="spinner"></span></div>
            <div v-else-if="!items.length" class="empty" style="padding: 28px"><div class="empty-hint">没有子文件夹</div></div>
            <div v-else v-for="it in items" :key="it.id" class="fb-item" @click="enter(it)">
              <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="var(--warn)" stroke-width="2"><path d="M3 7a2 2 0 012-2h4l2 2h8a2 2 0 012 2v9a2 2 0 01-2 2H5a2 2 0 01-2-2z"/></svg>
              <span class="flex-1 ellipsis">{{ it.name }}</span>
              <span class="muted">›</span>
            </div>
          </div>
          <div class="flex mt-16">
            <input class="input" v-model="newName" placeholder="新建文件夹名称" @keyup.enter="mkdir" />
            <button class="btn" :disabled="creating || !newName.trim()" @click="mkdir">新建</button>
          </div>
        </div>
        <div class="modal-foot">
          <span class="flex-1 mono muted">当前：/{{ trail.map(t => t.name).filter(n => n && n !== '根目录').join('/') }}</span>
          <button class="btn" @click="visible = false">取消</button>
          <button class="btn btn-primary" @click="pick">选择此目录</button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
