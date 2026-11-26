// 表单草稿：自动保存到 localStorage + 脏检查。
//
// 为什么需要：TaskEdit 有 20+ 个字段，关闭途径有 4 条（遮罩、✕、取消、
// 路由切换），此前全部静默丢弃。用户填完 20 个字段被误触遮罩 → 全部重来。
//
// 两个能力：
//
//   1. useDraft(key)
//      - 输入后防抖 400ms 写 localStorage
//      - 打开表单时若存在草稿，自动恢复并提示
//      - 提交成功后 clear()，否则下次打开会看到上次的旧内容
//
//   2. confirmDiscard()
//      关闭前做脏检查，脏则弹确认；用户选"放弃"才真正关闭。
//      与 ConfirmDialog 配合，避免用原生 confirm（在部分 WebView 被禁用，
//      会导致关闭静默失效——用户以为关掉了，其实草稿还在）。
//
// 注意：草稿里可能有用户输入的敏感内容（如账号名），因此：
//   - 只在用户主动填写后才写（不写初始值）
//   - 提供"清除草稿"的入口
//   - key 用页面/实体名而不是 ID，避免账号删除后草稿还在
import { ref, watch } from 'vue'

const PREFIX = 'pt_draft_'

// safeGet 读取 localStorage，禁用/隐私模式下返回 null。
// persist/clear 已各自 try-catch，读取也必须一致——否则 Safari 隐私模式下
// draft.start() 在 TaskEdit.open() 里抛错，弹层打开但表单未填充。
function safeGet(key) {
  try { return localStorage.getItem(key) } catch { return null }
}

// safeParse 解析 JSON，失败返回 fallback。
function safeParse(raw, fallback) {
  if (!raw) return fallback
  try {
    return JSON.parse(raw)
  } catch {
    return fallback
  }
}

// useDraft 给一份响应式表单挂上草稿能力。
//
// 用法：
//   const draft = useDraft('task-new', form)
//   draft.start()          // 在 open() 里调用：启动监听
//   draft.restore()        // 可选：恢复上次的草稿
//   draft.markSaved()      // 提交成功后调用
//   draft.stop()           // 关闭表单时调用（组件卸载也会自动停）
//
//   - key：草稿键（同一实体的新建/编辑用不同 key）
//   - target：响应式对象（reactive 对象）
//
// 返回：
//   - dirty：是否有未保存的修改（用于关闭时的脏检查）
//   - hasDraft：localStorage 里是否存在草稿
//   - restore()：把草稿合并回 target
//   - clear()：删除草稿（不重置 dirty 之外的语义）
//   - markSaved()：提交成功后清 dirty 与草稿
//   - start()/stop()：开始/停止监听
export function useDraft(key, target) {
  const storageKey = PREFIX + key
  const dirty = ref(false)
  const hasDraft = ref(false)
  let timer = null
  let stopWatch = null

  const read = () => unrefTarget(target)

  const persist = () => {
    try {
      const raw = JSON.stringify(read())
      if (!raw || raw === '{}' || raw === 'null' || raw.length < 3) {
        localStorage.removeItem(storageKey)
        hasDraft.value = false
        return
      }
      localStorage.setItem(storageKey, raw)
      hasDraft.value = true
    } catch {
      // localStorage 写满或被禁用（隐私模式）时静默失败：
      // 草稿是增强体验，不该让表单因为存不下而报错。
    }
  }

  const clear = () => {
    if (timer) { clearTimeout(timer); timer = null }
    try { localStorage.removeItem(storageKey) } catch { /* ignore */ }
    hasDraft.value = false
    dirty.value = false
  }

  // start 开始监听 target 变化。幂等：重复调用不会叠加监听。
  const start = () => {
    if (stopWatch) return
    hasDraft.value = !!safeParse(safeGet(storageKey), null)
    stopWatch = watch(
      () => read(),
      () => {
        dirty.value = true
        if (timer) clearTimeout(timer)
        timer = setTimeout(persist, 400)
      },
      { deep: true },
    )
  }

  const stop = () => {
    if (timer) { clearTimeout(timer); timer = null }
    if (stopWatch) { stopWatch(); stopWatch = null }
    // 必须清 dirty：否则用户打开表单、什么都没改就关闭，
    // 也会弹「放弃未保存的修改？」——用户明确知道没改，久了就无脑点确认，
    // 这个确认框也就失去了保护意义。
    dirty.value = false
  }

  // restore 把草稿合并回 target。
  // 用合并而不是替换：target 可能有默认值/新字段，直接覆盖会丢掉。
  const restore = () => {
    const raw = safeParse(safeGet(storageKey), null)
    if (!raw || typeof raw !== 'object') return false
    const t = unrefTarget(target)
    for (const [k, v] of Object.entries(raw)) {
      // 数组要整体替换（links 这类），对象要浅合并。
      if (Array.isArray(v)) t[k] = [...v]
      else if (v && typeof v === 'object') t[k] = { ...(t[k] || {}), ...v }
      else t[k] = v
    }
    hasDraft.value = false
    dirty.value = false
    return true
  }

  const markSaved = () => clear()

  return { dirty, hasDraft, restore, clear, markSaved, start, stop }
}

// unrefTarget 取出 ref 里的对象，或原样返回。
function unrefTarget(t) {
  if (t && typeof t === 'object' && 'value' in t && t.value && typeof t.value === 'object') {
    return t.value
  }
  return t
}

// useDirtyGuard 给关闭动作加脏检查。
//
// 用法：
//   const { confirmDiscard } = useDirtyGuard(confirmDialog, dirty)
//   async function close() {
//     if (!(await confirmDiscard())) return
//     visible.value = false
//   }
//
// 为什么用回调而不是直接依赖 ConfirmDialog：
// 保持 composable 与具体 UI 解耦，测试时也能传一个恒返回 true 的假实现。
export function useDirtyGuard(confirmFn, dirtyRef) {
  async function confirmDiscard() {
    if (!dirtyRef || !dirtyRef.value) return true
    if (!confirmFn) return true
    return await confirmFn({
      title: '放弃未保存的修改？',
      body: '表单还没有保存，关闭后这些修改会丢失。',
      warn: '如果不想现在填完，可以选择「继续编辑」。',
      confirmText: '放弃修改',
      cancelText: '继续编辑',
    })
  }
  return { confirmDiscard }
}