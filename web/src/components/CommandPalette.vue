<script setup>
// 全局搜索面板（⌘K / Ctrl+K / /）。
//
// 交互要点（这类面板好用与否全在细节）：
//   1. 键盘优先：↑↓ 选择、Enter 跳转、Esc 关闭。鼠标只是备选。
//   2. 无结果时明确说明「没搜到 X」，而不是显示一个空列表。
//   3. 分组标题常驻，用户能一眼看出"这条是任务、那条是文件"。
//   4. 输入防抖 250ms：不防抖会让每敲一个键都打一次请求。
//   5. 结果是"可跳转的"：点一条就跳到对应页面（任务/账号/规则/设置）。
//
// 为什么用 Teleport 挂到 body：避免被父级的 overflow 容器裁剪。
import { ref, computed, watch, onMounted, onUnmounted, nextTick } from 'vue'
import { useRouter } from 'vue-router'
import { searchApi } from '../api'

const router = useRouter()

const visible = ref(false)
const q = ref('')
const results = ref(null)
const loading = ref(false)
const error = ref('')
const activeIndex = ref(0)
let debounceTimer = null
let searchSeq = 0 // 请求序号守卫：旧请求后返回不能覆盖新结果

const emit = defineEmits(['close'])

// 扁平化的命中列表，用于键盘上下移动。
// 分组只影响渲染，不影响导航顺序（用户不希望"跳过一整个分组"）。
const flatHits = computed(() => {
  if (!results.value || !results.value.groups) return []
  return results.value.groups.flatMap(g => g.hits.map(h => ({ ...h, group: g })))
})

const canSearch = computed(() => q.value.trim().length > 0)

function open() {
  searchSeq++ // 作废上次打开时的在途请求
  visible.value = true
  q.value = ''
  results.value = null
  error.value = ''
  activeIndex.value = 0
  nextTick(() => focusInput())
}

function close() {
  visible.value = false
  emit('close')
}

const inputEl = ref(null)
function focusInput() {
  if (inputEl.value) inputEl.value.focus()
}

async function runSearch() {
  const term = q.value.trim()
  const seq = ++searchSeq
  if (!term) {
    results.value = null
    loading.value = false
    return
  }
  loading.value = true
  error.value = ''
  try {
    const res = await searchApi.query(term)
    if (seq !== searchSeq || term !== q.value.trim()) return
    results.value = res
    activeIndex.value = 0
  } catch (e) {
    if (seq !== searchSeq || term !== q.value.trim()) return
    error.value = e.message
    results.value = null
  } finally {
    // 只有最新请求能清 loading；旧请求提前清会让界面显示"搜完了"
    // 而新请求还在飞。
    if (seq === searchSeq) loading.value = false
  }
}

// 输入防抖：250ms 是"感觉即时"与"不浪费请求"的折中。
watch(q, (v) => {
  if (debounceTimer) clearTimeout(debounceTimer)
  if (!v.trim()) {
    searchSeq++ // 清空输入：作废所有在途请求
    results.value = null
    loading.value = false
    return
  }
  loading.value = true
  debounceTimer = setTimeout(runSearch, 250)
})

// 跳到任务详情（可深链）。
// 搜索结果的 route 是后端给的前端路径（如 /tasks 或 /accounts），
// 但任务类型的命中可以直接跳到 /tasks/:id，比落到列表更精确。
function goto(hit) {
  close()
  if (!hit.route) return
  if (hit.kind === 'task' && hit.id) {
    router.push(`/tasks/${hit.id}`)
    return
  }
  router.push(hit.route)
}

function onKeydown(e) {
  if (!visible.value) return
  const hits = flatHits.value
  if (e.key === 'Escape') {
    e.preventDefault()
    close()
    return
  }
  if (e.key === 'ArrowDown') {
    e.preventDefault()
    if (hits.length) activeIndex.value = (activeIndex.value + 1) % hits.length
    return
  }
  if (e.key === 'ArrowUp') {
    e.preventDefault()
    if (hits.length) activeIndex.value = (activeIndex.value - 1 + hits.length) % hits.length
    return
  }
  if (e.key === 'Enter') {
    e.preventDefault()
    const hit = hits[activeIndex.value]
    if (hit) goto(hit)
  }
}

// 全局快捷键：⌘K / Ctrl+K 打开；/ 在不在输入框时打开。
function onGlobalKey(e) {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
    e.preventDefault()
    open()
    return
  }
  const tag = (e.target && e.target.tagName) || ''
  const typing = tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' ||
    (e.target && e.target.isContentEditable)
  if (e.key === '/' && !typing) {
    e.preventDefault()
    open()
  }
}

onMounted(() => {
  window.addEventListener('keydown', onGlobalKey)
  window.addEventListener('keydown', onKeydown)
})
onUnmounted(() => {
  window.removeEventListener('keydown', onGlobalKey)
  window.removeEventListener('keydown', onKeydown)
  if (debounceTimer) clearTimeout(debounceTimer)
})

defineExpose({ open })

// 图标：不同域给不同图标，帮助快速识别（颜色/形状之外还有语义）。
const kindIcon = (kind) => {
  switch (kind) {
    case 'task': return '📋'
    case 'account': return '☁️'
    case 'rule': return '🔤'
    case 'category': return '📁'
    case 'run': return '📄'
    case 'file': return '🎞️'
    default: return '•'
  }
}
</script>

<template>
  <Teleport to="body">
    <div v-if="visible" class="cmdk-overlay" @mousedown.self="close">
      <div class="cmdk" role="dialog" aria-modal="true" aria-label="全局搜索">
        <div class="cmdk-input-row">
          <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor"
            stroke-width="2" stroke-linecap="round" class="cmdk-search-icon">
            <circle cx="11" cy="11" r="7" />
            <path d="M20 20l-3.5-3.5" />
          </svg>
          <input ref="inputEl" v-model="q" class="cmdk-input" type="text"
            placeholder="搜索任务、账号、规则、已转存文件…"
            autocomplete="off" spellcheck="false" />
          <kbd class="cmdk-esc">Esc</kbd>
        </div>

        <div class="cmdk-results">
          <div v-if="loading" class="cmdk-state">
            <span class="spinner spinner-sm"></span> 搜索中…
          </div>
          <div v-else-if="error" class="cmdk-state cmdk-state-error">{{ error }}</div>
          <div v-else-if="!canSearch" class="cmdk-state cmdk-state-hint">
            输入关键词开始搜索，或按 <kbd>Esc</kbd> 关闭
          </div>
          <div v-else-if="!results || !flatHits.length" class="cmdk-state">
            没有找到「{{ q }}」
            <div class="cmdk-state-sub">换个关键词试试，或检查是否在别的网盘账号下</div>
          </div>

          <template v-else>
            <div v-for="g in results.groups" :key="g.kind" class="cmdk-group">
              <div class="cmdk-group-title">{{ g.label }}</div>
              <div v-for="hit in g.hits" :key="g.kind + ':' + hit.id"
                class="cmdk-item"
                :class="{ active: flatHits[activeIndex] && flatHits[activeIndex].id === hit.id && flatHits[activeIndex].group.kind === g.kind }"
                @mouseenter="activeIndex = flatHits.findIndex(h => h.id === hit.id && h.group.kind === g.kind)"
                @click="goto(hit)">
                <span class="cmdk-icon">{{ kindIcon(g.kind) }}</span>
                <div class="cmdk-body">
                  <div class="cmdk-title">{{ hit.title }}</div>
                  <div v-if="hit.subtitle" class="cmdk-sub">{{ hit.subtitle }}</div>
                </div>
                <span v-if="hit.badge && hit.badge !== 'success'" class="tag">{{ hit.badge }}</span>
                <span class="cmdk-go">↵</span>
              </div>
              <div v-if="g.more" class="cmdk-more">{{ g.more }}</div>
            </div>
            <div v-if="results.truncated" class="cmdk-foot">
              部分结果已省略，请用更具体的关键词缩小范围
            </div>
          </template>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.cmdk-overlay {
  position: fixed; inset: 0; z-index: 210;
  background: rgba(18, 24, 43, 0.45); backdrop-filter: blur(2px);
  display: flex; align-items: flex-start; justify-content: center;
  padding: 12vh 16px 16px;
  animation: fadeIn 0.16s ease;
}
.cmdk {
  width: min(620px, 100%);
  background: var(--surface); border-radius: var(--radius-lg);
  box-shadow: var(--shadow-lg); overflow: hidden;
  animation: slideUp 0.2s ease;
}
.cmdk-input-row {
  display: flex; align-items: center; gap: 10px;
  padding: 14px 16px; border-bottom: 1px solid var(--border);
}
.cmdk-search-icon { color: var(--text-3); flex-shrink: 0; }
.cmdk-input {
  flex: 1; border: none; outline: none; background: transparent;
  font-size: 15px; font-family: var(--font); color: var(--text);
}
.cmdk-input::placeholder { color: var(--text-3); }
.cmdk-esc, .cmdk kbd {
  font-size: 11px; color: var(--text-3); border: 1px solid var(--border);
  border-radius: 5px; padding: 1px 5px; font-family: var(--mono);
  background: var(--surface-2);
}
.cmdk-results {
  max-height: min(52vh, 420px); overflow-y: auto; padding: 6px 0 8px;
}
.cmdk-state {
  padding: 26px 18px; text-align: center; color: var(--text-3);
  font-size: 13px; display: flex; flex-direction: column; gap: 8px; align-items: center;
}
.cmdk-state-error { color: var(--danger); }
.cmdk-state-sub { font-size: 12px; color: var(--text-3); }
.cmdk-state-hint { color: var(--text-3); }
.cmdk-group-title {
  padding: 8px 16px 4px; font-size: 11.5px; color: var(--text-3);
  font-weight: 600; letter-spacing: 0.3px;
}
.cmdk-item {
  display: flex; align-items: center; gap: 10px;
  padding: 8px 16px; cursor: pointer; border-left: 3px solid transparent;
}
.cmdk-item:hover { background: var(--surface-2); }
.cmdk-item.active { background: var(--primary-soft); border-left-color: var(--primary); }
.cmdk-icon { font-size: 14px; flex-shrink: 0; }
.cmdk-body { flex: 1; min-width: 0; }
.cmdk-title { font-size: 13.5px; color: var(--text); overflow-wrap: anywhere; }
.cmdk-sub {
  font-size: 12px; color: var(--text-3); margin-top: 2px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.cmdk-go { color: var(--text-3); font-size: 12px; opacity: 0; }
.cmdk-item.active .cmdk-go { opacity: 1; }
.cmdk-more {
  padding: 4px 16px 8px 42px; font-size: 11.5px; color: var(--text-3);
}
.cmdk-foot {
  padding: 8px 16px; font-size: 11.5px; color: var(--text-3);
  border-top: 1px solid var(--border);
}
@media (max-width: 820px) {
  /* 移动端：占满宽度，从顶部滑入，输入框字号 16px 防 iOS 缩放 */
  .cmdk-overlay { padding: 0; align-items: stretch; }
  .cmdk {
    width: 100%; height: 100dvh; border-radius: 0;
    display: flex; flex-direction: column;
  }
  .cmdk-input-row { padding: 12px 14px; }
  .cmdk-input { font-size: 16px; }
  .cmdk-results { flex: 1; max-height: none; }
}
</style>