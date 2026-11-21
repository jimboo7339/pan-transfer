// 主题状态：light / dark / auto（跟随系统 prefers-color-scheme）。
// 持久化在 localStorage('pt_theme')；index.html 里有同步的预置脚本避免首帧闪烁。
//
// localStorage 必须 try-catch：Safari 隐私模式、沙箱 iframe、file:// 协议下
// 读 localStorage 会抛 SecurityError。这个模块在 main.js 里被 import，
// 一旦抛异常整个应用无法启动（#app 永远空白）。
import { ref } from 'vue'

const KEY = 'pt_theme'

function readStored() {
  try {
    return localStorage.getItem(KEY) || 'auto'
  } catch {
    return 'auto'
  }
}

export const theme = ref(readStored())

const mql = window.matchMedia('(prefers-color-scheme: dark)')
mql.addEventListener('change', apply)

function apply() {
  const dark = theme.value === 'dark' || (theme.value === 'auto' && mql.matches)
  document.documentElement.classList.toggle('dark', dark)
  const meta = document.querySelector('meta[name="theme-color"]')
  if (meta) meta.setAttribute('content', dark ? '#161c2c' : '#f4f6fb')
}

export function setTheme(t) {
  theme.value = t
  try {
    localStorage.setItem(KEY, t)
  } catch {
    // 隐私模式下写入失败：主题只在本次会话生效，不影响使用
  }
  apply()
}

export function initTheme() { apply() }
