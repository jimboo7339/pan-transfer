import { createApp, ref } from 'vue'
import { createRouter, createWebHashHistory } from 'vue-router'
import { initTheme } from './theme'
import App from './App.vue'
import { authApi, setToken, setUnauthorizedHandler } from './api'
import './style.css'

// 路由懒加载：所有视图用 () => import() 而不是静态 import。
//
// 为什么：此前 main.js 静态 import 了 5 个视图，导致首屏要下载全部业务代码
// （gzip 后 ~74KB）。改成懒加载后，首屏只需要 Dashboard 一个 chunk，
// 其余页面在用户点击时才加载。
//
// 仪表盘作为默认路由：打开面板的第一诉求是「系统健康吗」，
// 而不是「有哪些任务」（任务列表需要用户先有任务才有意义）。
const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/', component: () => import('./views/Dashboard.vue') },
    { path: '/tasks', component: () => import('./views/Tasks.vue') },
    // 任务详情：可分享、可深链（从仪表盘告警/搜索结果/通知直接跳进来）
    { path: '/tasks/:id', component: () => import('./views/TaskDetail.vue'), props: true },
    { path: '/logs', component: () => import('./views/Logs.vue') },
    { path: '/accounts', component: () => import('./views/Accounts.vue') },
    { path: '/rules', component: () => import('./views/Rules.vue') },
    { path: '/settings', component: () => import('./views/Settings.vue') },
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

// 401 处理：标记会话已过期并清空本地 token。
//
// 这里绝不能回调 refreshSession()：api.js 在抛错前会同步调用 onUnauthorized，
// 而 refreshSession 自身要发 /api/auth/status 请求。若那个请求也 401，
// 就会再次触发 onUnauthorized → 再发一次 → 无限递归的请求风暴
// （每层都 await 真实 HTTP 请求，不是栈溢出，而是把后端打挂）。
//
// 正确做法：只更新本地状态，不发起任何网络请求。
// 用户看到顶部横幅后自行重新登录。
let handling401 = false
setUnauthorizedHandler(() => {
  if (handling401) return
  handling401 = true
  setToken('')
  authExpired.value = true
  // 直接把会话标成"需要登录"，避免再发请求去问。
  session.value = { auth_required: true, authed: false, version: '', loaded: true }
  handling401 = false
})

initTheme()
refreshSession().finally(() => app.mount('#app'))