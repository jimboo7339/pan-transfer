// 全局键盘快捷键。
//
// 为什么需要：这个工具的常见操作是"看一圈再改一两个任务"，
// 全程鼠标要跨越整个屏幕。快捷键的价值不是"更快"，而是"不打断"——
// 用户手不离键盘就能完成大部分操作。
//
// 设计取舍（重要）：
//   - 不在输入框里拦截：否则用户打 "n" 这个字母时会突然弹出新建任务框。
//     这是快捷键系统最常见的 bug 来源。
//   - 用 g 前缀做跳页序列（g t / g l / g a ...）而不是单键，
//     避免与浏览器/系统快捷键冲突，也和 GitHub、Gmail 的习惯一致。
//   - 不拦截带修饰键的组合（除我们自己注册的），把 ⌘K 留给搜索面板。
//   - 每个快捷键都能在「?」面板里查到，否则等于不存在（用户不会读文档）。
//
// 重要：useRouter / onMounted 必须在 setup 内调用。
// 早先这里是模块顶层直接调用，结果是：
//   - useRouter() 在模块求值时 currentInstance 为 null → 返回 undefined，
//     六个 g 前缀跳页快捷键全部 TypeError；
//   - onMounted 没有活跃组件实例 → 函数体根本不执行，
//     keydown/keyup 监听从未注册，n / ? / Esc 全部无响应。
// 且不报错、不警告（生产模式静默失败），所以必须靠 useHotkeys() 在
// 组件 setup 里完成注册。
import { ref, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'

// 是否显示快捷键帮助。App 层会渲染一个面板。
const showHelp = ref(false)

// 页面名称：用于「已在 xx 页」的提示文案。
const PAGE_NAMES = {
  '/': '仪表盘',
  '/tasks': '转存任务',
  '/logs': '运行日志',
  '/accounts': '网盘账号',
  '/rules': '命名规则',
  '/settings': '设置',
}
function pageName(path) {
  return PAGE_NAMES[path] || path
}

const isTyping = (el) => {
  if (!el) return false
  const tag = el.tagName
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return true
  // contenteditable 的富文本容器（本应用暂无，但为将来留口子）
  return !!el.isContentEditable
}

// useHotkeys 在组件 setup 内注册全局快捷键，返回帮助面板的开关。
//
// 返回 showHelp 而不是在模块里维护状态：这样组件卸载时能把监听摘干净，
// 也不会出现"路由切换后快捷键还在打旧页面"的问题。
export function useHotkeys(options = {}) {
  const router = useRouter()

  // onFeedback 由 App 层注入（用来弹 toast）。
  //
  // 为什么用回调而不是直接 import toast：hotkeys.js 是纯逻辑模块，
  // 不该依赖全局 provide；且测试时可以传一个只记录不调 UI 的实现。
  const onFeedback = options.onFeedback

  // g 前缀序列的状态：记下按键时间，1.2s 内没接下一个键就重置。
  let pendingG = 0

  // dispatch 处理一次按键，返回 true 表示已处理（调用方应 preventDefault）。
  function dispatch(e) {
    if (e.metaKey || e.ctrlKey) {
      // ⌘K / Ctrl+K 由 CommandPalette 自己处理，这里不重复注册。
      return false
    }
    if (e.altKey) return false

    // g 前缀跳页序列
    if (pendingG > 0 && Date.now() - pendingG < 1200) {
      const routes = {
        d: '/', t: '/tasks', l: '/logs', a: '/accounts', r: '/rules', s: '/settings',
      }
      const key = e.key.toLowerCase()
      const to = routes[key]
      pendingG = 0
      if (!to) {
        // 按了 g 但第二个键不认识：消耗掉这次输入，避免后续误判。
        return false
      }
      // 已在目标页时 router.push 是 no-op，界面完全没反应——
      // 用户会以为"快捷键坏了"（仪表盘是默认页，g d 最容易踩到）。
      // 这里给一条 toast 明确告知，而不是静默无响应。
      const cur = router.currentRoute.value.path
      if (cur === to) {
        onFeedback?.(`已在「${pageName(to)}」`, 'info')
        return true
      }
      router.push(to)
      return true
    }
    if (e.key === 'g') {
      pendingG = Date.now()
      return false // 不阻止默认行为，但要记住状态
    }

    // 单键动作
    if (e.key === '?') {
      showHelp.value = !showHelp.value
      return true
    }
    if (e.key === 'n') {
      // 新建任务：派发自定义事件，由任务页监听并打开编辑弹层。
      // 用事件而不是直接调组件方法：快捷键层不该知道任务页的组件结构。
      window.dispatchEvent(new CustomEvent('pt:new-task'))
      return true
    }
    if (e.key === 'Escape') {
      showHelp.value = false
      return false // Esc 的其它用途（关弹层）由各自组件处理
    }
    return false
  }

  function onKeydown(e) {
    if (isTyping(e.target)) {
      // 在输入框里按 Esc 仍然要能关帮助面板
      if (e.key === 'Escape' && showHelp.value) showHelp.value = false
      return
    }
    if (dispatch(e)) e.preventDefault()
  }

  function onKeyup(e) {
    // g 前缀序列超时重置（用户按了 g 然后改主意）。
    // 只在 keyup 时检查，避免每次 keydown 都算时间。
    if (e.key !== 'g' && pendingG > 0 && Date.now() - pendingG >= 1200) {
      pendingG = 0
    }
  }

  onMounted(() => {
    window.addEventListener('keydown', onKeydown)
    window.addEventListener('keyup', onKeyup)
  })
  onUnmounted(() => {
    window.removeEventListener('keydown', onKeydown)
    window.removeEventListener('keyup', onKeyup)
    pendingG = 0
  })

  return { showHelp }
}

// 快捷键清单，供帮助面板渲染。
// 改动这里时同步更新帮助面板的文案。
export const HOTKEYS = [
  { keys: ['⌘K', 'Ctrl+K'], desc: '打开全局搜索' },
  { keys: ['/'], desc: '聚焦搜索（不在输入框时）' },
  { keys: ['g', 'd'], desc: '跳到仪表盘' },
  { keys: ['g', 't'], desc: '跳到转存任务' },
  { keys: ['g', 'l'], desc: '跳到运行日志' },
  { keys: ['g', 'a'], desc: '跳到网盘账号' },
  { keys: ['g', 'r'], desc: '跳到命名规则' },
  { keys: ['g', 's'], desc: '跳到设置' },
  { keys: ['n'], desc: '新建转存任务' },
  { keys: ['?'], desc: '显示/隐藏这份快捷键清单' },
  { keys: ['Esc'], desc: '关闭弹层 / 取消' },
]