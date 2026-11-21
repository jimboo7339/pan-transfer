import { createApp, ref } from 'vue'
import { createRouter, createWebHashHistory } from 'vue-router'
import { initTheme } from './theme'
import App from './App.vue'
import Tasks from './views/Tasks.vue'
import Logs from './views/Logs.vue'
import Accounts from './views/Accounts.vue'
import Rules from './views/Rules.vue'
import Settings from './views/Settings.vue'
import { authApi, setToken, setUnauthorizedHandler } from './api'
import './style.css'

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/', component: Tasks },
    { path: '/logs', component: Logs },
    { path: '/accounts', component: Accounts },
    { path: '/rules', component: Rules },
    { path: '/settings', component: Settings },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

const app = createApp(App)
app.use(router)

// 全局 toast 支持 action（撤销入口）。
// 清空转存历史会让已转存文件被重复转存，属于不可逆的数据损坏，
// 必须给一条"后悔"的路径而不只是一个确认框。
const toasts = ref([])
app.provide('toasts', toasts)
let toastId = 0
app.provide('toast', (msg, type = 'info', duration = 3200, action = null) => {
  const id = ++toastId
  toasts.value.push({ id, msg, type, action })
  const ttl = action ? Math.max(duration, 8000) : duration
  setTimeout(() => { toasts.value = toasts.value.filter(t => t.id !== id) }, ttl)
})

// ---------- 会话状态 ----------
const session = ref({ auth_required: false, authed: true, version: '', loaded: false })
app.provide('session', session)

// 全局错误横幅：401 时用户看到 "HTTP 401" 并不知道要重新输入密码。
// 顶部横幅比 toast 更合适——它需要一直留着直到用户处理。
const authExpired = ref(false)
app.provide('authExpired', authExpired)

async function refreshSession() {
  try {
    const s = await authApi.status()
    session.value = { ...s, loaded: true }
  } catch {
    session.value = { auth_required: false, authed: true, version: '', loaded: true }
  }
}
app.provide('refreshSession', refreshSession)

setUnauthorizedHandler(() => {
  setToken('')
  authExpired.value = true
  refreshSession()
})

initTheme()
refreshSession().finally(() => app.mount('#app'))