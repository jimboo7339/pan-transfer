<script setup>
// 分享链接预览弹窗：目录导航 + 重命名预览（对应 CASX 的"分享链接预览"）。
import { ref, inject, computed } from 'vue'
import Modal from './Modal.vue'
import { shareApi, fmtSize, fmtTime } from '../api'
import { extractShareFid, stripShareFid } from '../shareurl'

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
const loadError = ref('')
const filter = ref('all') // all | todo | saved 转存状态筛选

// name_saved 承载三类含义：已存在文件名 / "起始及之前" / "重命名冲突（保留最大）"
function savedKind(it) {
  if (!it.name_saved) return ''
  if (it.name_saved === '起始及之前') return 'skip'
  if (it.name_saved.startsWith('重命名冲突')) return 'conflict'
  return 'saved'
}

const stats = computed(() => {
  let todo = 0, saved = 0, skip = 0, conflict = 0
  for (const it of items.value) {
    const k = savedKind(it)
    if (it.name_re) todo++
    else if (k === 'saved') saved++
    else if (k === 'skip') skip++
    else if (k === 'conflict') conflict++
  }
  return { total: items.value.length, todo, saved, skip, conflict }
})

const shownItems = computed(() => {
  if (filter.value === 'todo') return items.value.filter(it => !!it.name_re)
  if (filter.value === 'saved') return items.value.filter(it => savedKind(it) === 'saved')
  return items.value
})

// 预览参数（父组件通过 open 传入）
let params = null
const emit = defineEmits(['use-folder'])

function open(p) {
  visible.value = true
  // 每条链接各自携带文件夹：#/list/share/<fid> 由链接自己解析，互不影响。
  const startFID = extractShareFid(p.share_url)
  // 浏览用链接去掉 fid，保证「🏠 根目录」能真正回到分享根目录。
  params = { ...p, share_url: stripShareFid(p.share_url) }
  trail.value = []
  curDirName.value = startFID ? '指定文件夹' : '根目录'
  filter.value = 'all'
  load(startFID)
}
defineExpose({ open })

async function load(fid) {
  loading.value = true
  loadError.value = ''
  try {
    const res = await shareApi.preview({ ...params, pdir_fid: fid, max_items: 500 })
    items.value = res.items || []
    pwdID.value = res.pwd_id
    pdirFID.value = res.pdir_fid
    total.value = res.total
    truncated.value = res.truncated
  } catch (e) {
    toast(e.message, 'error')
    loadError.value = e.message
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
  const fid = String(pdirFID.value || '').trim()
  const normalized = ['', '0', 'root'].includes(fid.toLowerCase()) ? '' : fid
  emit('use-folder', {
    pdir_fid: normalized,
    name: curDirName.value,
    link_index: params?.link_index ?? 0,
  })
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
            <div class="flex" style="gap: 8px; align-items: center">
              <span class="field-hint" style="margin-top: 0">仅影响当前这条链接</span>
              <button class="btn btn-primary btn-sm" @click="useCurrentFolder">📌 只转存当前文件夹</button>
            </div>
          </div>

          <!-- 转存状态统计与筛选：已转存 / 待转存一目了然 -->
          <div v-if="!loadError && items.length" class="flex mb-16" style="gap: 8px; flex-wrap: wrap; align-items: center">
            <span class="badge badge-primary">待转存 {{ stats.todo }}</span>
            <span class="badge badge-success">已转存 {{ stats.saved }}</span>
            <span v-if="stats.skip" class="badge badge-gray">起始前 {{ stats.skip }}</span>
            <span v-if="stats.conflict" class="badge badge-warn">重命名冲突 {{ stats.conflict }}</span>
            <span class="flex-1"></span>
            <div class="flex" style="gap: 6px">
              <button class="btn btn-sm" :class="filter === 'all' ? 'btn-primary' : ''" @click="filter = 'all'">全部</button>
              <button class="btn btn-sm" :class="filter === 'todo' ? 'btn-primary' : ''" @click="filter = 'todo'">只看待转存</button>
              <button class="btn btn-sm" :class="filter === 'saved' ? 'btn-primary' : ''" @click="filter = 'saved'">只看已转存</button>
            </div>
          </div>

          <div v-if="loading" class="loading-block"><span class="spinner"></span> 加载中…</div>
          <div v-if="loadError" class="card card-pad" style="border-color: var(--danger); background: var(--danger-soft)">
            <div class="flex" style="justify-content: space-between; align-items: flex-start">
              <div>
                <div style="font-weight: 600; color: var(--danger); font-size: 13px">⚠ 预览失败</div>
                <div class="mono mt-8" style="font-size: 12px; color: var(--text-2); word-break: break-all">{{ loadError }}</div>
                <div class="field-hint mt-8" style="margin-top: 10px">
                  排查顺序：① 账号 Cookie / Token 是否有效（在「网盘账号」页点「测试连接」）；② 分享链接是否失效 / 是否被删除；③ 提取码是否正确（如需提取码在 URL 里带 <code class="mono">?pwd=xxxx</code>）
                </div>
              </div>
            </div>
          </div>
          <div v-else-if="!items.length && !loadError" class="empty">
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
                <tr v-for="it in shownItems" :key="it.fid" :class="{
                  'row-saved': savedKind(it) === 'saved',
                  'row-conflict': savedKind(it) === 'conflict',
                  'row-skip': savedKind(it) === 'skip',
                }">
                  <td>
                    <div class="preview-filename" :style="it.is_dir ? 'cursor: pointer; font-weight: 500' : ''" @click="enterDir(it)">
                      <svg v-if="it.is_dir" width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="var(--warn)" stroke-width="2"><path d="M3 7a2 2 0 012-2h4l2 2h8a2 2 0 012 2v9a2 2 0 01-2 2H5a2 2 0 01-2-2z"/></svg>
                      <svg v-else width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z"/><path d="M14 2v6h6"/></svg>
                      <span class="nm" :title="it.name">{{ it.name }}</span>
                    </div>
                    <span v-if="it.name_saved" class="saved-note" :class="'sn-' + savedKind(it)">
                      <template v-if="savedKind(it) === 'saved'">✅ 已转存 → {{ it.name_saved }}</template>
                      <template v-else-if="savedKind(it) === 'skip'">⏭ {{ it.name_saved }}</template>
                      <template v-else>⚠ {{ it.name_saved }}</template>
                    </span>
                  </td>
                  <td class="num">{{ it.is_dir ? '-' : fmtSize(it.size) }}</td>
                  <td>
                    <span v-if="it.name_re && it.name_re !== it.name" class="text-success">{{ it.name_re }}</span>
                    <span v-else-if="it.name_re" class="muted">{{ it.name_re }}</span>
                    <span v-else-if="savedKind(it) === 'saved'" class="badge badge-success">✅ 已转存</span>
                    <span v-else-if="savedKind(it) === 'conflict'" class="badge badge-warn">⚠ 冲突</span>
                    <span v-else-if="savedKind(it) === 'skip'" class="badge badge-gray">⏭ 跳过</span>
                    <span v-else class="muted">—</span>
                  </td>
                  <td class="num muted">{{ it.updated_at ? fmtTime(it.updated_at * 1000) : '-' }}</td>
                </tr>
              </tbody>
            </table>
            <div v-if="!shownItems.length" class="empty" style="padding: 18px">
              <div class="empty-title">该状态下没有条目</div>
            </div>
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
