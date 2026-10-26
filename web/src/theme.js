// 主题状态：light / dark / auto（跟随系统 prefers-color-scheme）。
// 持久化在 localStorage('pt_theme')；index.html 里有同步的预置脚本避免首帧闪烁。
import { ref } from 'vue'

const KEY = 'pt_theme'
export const theme = ref(localStorage.getItem(KEY) || 'auto')

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
  localStorage.setItem(KEY, t)
  apply()
}

export function initTheme() { apply() }
