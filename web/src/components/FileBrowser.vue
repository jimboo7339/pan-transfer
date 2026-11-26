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
// 面包屑：每项是一个已进入的目录 {fid: 它自己的 fid, name}。
// 当前目录 = 最后一项；空数组表示根目录（fid='0'）。
const trail = ref([])
const items = ref([])
const creating = ref(false)
const newName = ref('')

const emit = defineEmits(['picked'])

// 竞态守卫：弹层内连点文件夹 / 换账号重开时，旧请求后返回不能覆盖新状态。
// 旧代码 load() 把 fid.value 写回自己的 target，随后 pick() 会用新路径配旧 fid。
let loadSeq = 0

function open(accId, initialPath = '') {
  accountId.value = accId
  visible.value = true
  trail.value = []
  const seq = ++loadSeq
  // 先重置再解析：否则弹层打开的一瞬间会显示上一次浏览的目录内容
  // （fid 还是旧值），底部"当前：/…"与列表都是脏的。
  fid.value = '0'
  items.value = []
  if (initialPath) {
    // 从路径解析起点目录
    fileApi.resolve(accId, initialPath).then(res => {
      if (seq !== loadSeq || accountId.value !== accId) return
      fid.value = res.fid
      // 整条 initialPath 合成一项（fid 为末级目录自身），pick() 按 trail
      // 拼路径，必须保留每一层，否则从已有路径进入时第一层会被吞掉。
      trail.value = pathTrail(res.fid, initialPath)
      load(fid.value, seq)
    }).catch(() => { if (seq === loadSeq) { fid.value = '0'; load('0', seq) } })
  } else {
    fid.value = '0'
    load('0', seq)
  }
}
defineExpose({ open })

// 把初始路径转成 trail：整条路径合成一项，fid 就是该目录自身的 fid。
// 两个驱动的 ResolvePathToFID 都只返回末级 fid（夸克 path_list 按路径返回、
// 光鸭逐级 ls 返回最后一级），中间层级没有可用 fid，无法拆成多项供面包屑跳转。
function pathTrail(fidValue, path) {
  const parts = path.split('/').map(s => s.trim()).filter(Boolean)
  return parts.length ? [{ fid: fidValue, name: parts.join('/') }] : []
}

async function load(target, seq = loadSeq) {
  loading.value = true
  const acc = accountId.value
  try {
    const list = (await fileApi.list(acc, target)) || []
    if (seq !== loadSeq || acc !== accountId.value) return
    items.value = list.filter(i => i.is_dir)
    fid.value = target
  } catch (e) {
    if (seq === loadSeq) toast(e.message, 'error')
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

// pick 返回当前目录：trail 的最后一项就是当前目录（open/enter 都保证这点）。
// 旧代码 trail.slice(1) 把第一层真实目录当成"根目录哨兵"切掉，导致
// /影视资源/电视剧/xxx 变成 /电视剧/xxx。
function pick() {
  const cur = trail.value[trail.value.length - 1]
  const path = cur && cur.name ? '/' + trail.value.map(t => t.name).join('/') : ''
  emit('picked', { path, fid: fid.value })
  visible.value = false
}

function enter(item) {
  trail.value.push({ fid: item.id, name: item.name })
  load(item.id)
}

function jump(idx) {
  // idx<0 = 回根目录；否则跳到 trail[idx] 这个目录本身（fid 就是它自己的）。
  if (idx < 0) { trail.value = []; load('0'); return }
  const t = trail.value[idx]
  trail.value = trail.value.slice(0, idx + 1)
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
</script>

<template>
  <Modal v-model="visible" title="选择保存目录" size="lg">
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
    <template #footer>
      <span class="flex-1 mono muted">当前：/{{ trail.map(t => t.name).join('/') }}</span>
      <button class="btn" @click="visible = false">取消</button>
      <button class="btn btn-primary" @click="pick">选择此目录</button>
    </template>
  </Modal>
</template>
