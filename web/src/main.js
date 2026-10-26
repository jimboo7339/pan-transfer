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

// ---------- 全局 Toast ----------
const toasts = ref([])
app.provide('toasts', toasts)
let toastId = 0
app.provide('toast', (msg, type = 'info', duration = 3200) => {
  const id = ++toastId
  toasts.value.push({ id, msg, type })
  setTimeout(() => { toasts.value = toasts.value.filter(t => t.id !== id) }, duration)
})

// ---------- 会话状态 ----------
const session = ref({ auth_required: false, authed: true, version: '', loaded: false })
app.provide('session', session)

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
  refreshSession()
})

initTheme()
refreshSession().finally(() => app.mount('#app'))
