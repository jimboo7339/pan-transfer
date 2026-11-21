// 轻量 API 客户端：JSON、错误冒泡、Bearer 会话、错误分类。
//
// 错误分类（kind）让 UI 能决定「重试 / 重新登录 / 就地提示」，而不是一律
// 弹一个 toast。此前所有错误都是同一个 toast，401 时用户看到"HTTP 401"
// 并不知道要重新输入密码；网络抖动一次就白屏。
let token = readStoredToken()
let onUnauthorized = null

// localStorage 在某些环境（Safari 隐私模式、沙箱 iframe、file://）会抛
// SecurityError。这个模块被 main.js 直接 import，抛异常等于应用无法启动。
function readStoredToken() {
  try {
    return localStorage.getItem('pt_token') || ''
  } catch {
    return ''
  }
}

export function setToken(t) {
  token = t || ''
  try {
    if (token) localStorage.setItem('pt_token', token)
    else localStorage.removeItem('pt_token')
  } catch {
    // 存不下时 token 只在内存里有效（刷新后需重新登录），但不该崩
  }
}
export function getToken() { return token }
export function setUnauthorizedHandler(fn) { onUnauthorized = fn }

// 错误类型：
//   network     网络不可达 / 请求没发出去 —— 可重试
//   auth        401，会话过期或密码变更 —— 需要重新登录，重试无意义
//   validation  400，请求参数错误 —— 需要用户改输入，重试无意义
//   request     其它 4xx（404/409 等）—— 需要用户处理，重试无意义
//   server      5xx，服务端异常 —— 可重试（可能是暂时的）
export const ERR = {
  NETWORK: 'network',
  AUTH: 'auth',
  VALIDATION: 'validation',
  REQUEST: 'request',
  SERVER: 'server',
}

// 可重试的错误类型。UI 据此决定要不要显示"重试"按钮。
export const RETRYABLE = new Set([ERR.NETWORK, ERR.SERVER])

// request 执行一次请求，失败时抛出带 kind 的 Error。
async function request(method, url, body) {
  const headers = { 'Content-Type': 'application/json' }
  if (token) headers['Authorization'] = 'Bearer ' + token
  const opts = { method, headers }
  if (body !== undefined) opts.body = JSON.stringify(body)
  let resp, data
  try {
    resp = await fetch(url, opts)
  } catch (e) {
    // fetch 只在「请求根本没发出去」时抛（断网、DNS 失败、CORS 被拦）。
    // 这种情况下重试有意义。
    const err = new Error('网络请求失败：' + e.message)
    err.kind = ERR.NETWORK
    throw err
  }
  // 响应体解析失败通常意味着反代返回了 HTML 错误页，
  // 而不是我们的 JSON——这种情况重试无意义，但要给用户可理解的提示。
  try {
    data = await resp.json()
  } catch (e) {
    if (!resp.ok) {
      const err = new Error(`服务返回了非 JSON 响应（HTTP ${resp.status}）`)
      err.kind = resp.status >= 500 ? ERR.SERVER : ERR.REQUEST
      err.status = resp.status
      throw err
    }
    return null
  }
  if (!resp.ok) {
    const msg = data && data.error ? data.error.message : `HTTP ${resp.status}`
    if (resp.status === 401 && onUnauthorized) onUnauthorized()
    const err = new Error(msg)
    err.code = data && data.error ? data.error.code : ''
    if (resp.status === 401) err.kind = ERR.AUTH
    else if (resp.status === 400) err.kind = ERR.VALIDATION
    else if (resp.status >= 500) err.kind = ERR.SERVER
    else err.kind = ERR.REQUEST
    err.status = resp.status
    throw err
  }
  return data && 'data' in data ? data.data : data
}

// withRetry 对可重试的错误做一次指数退避重试。
//
// 只重试一次而不是三次：用户手动触发的操作（保存、删除）重试三次会显得
// "点了没反应"；而 GET 请求在后台被定时任务大量调用，重试成本要克制。
// 调用方也可以自己决定是否重试。
export async function withRetry(fn, { retries = 1, baseDelay = 400 } = {}) {
  let lastErr
  for (let attempt = 0; attempt <= retries; attempt++) {
    try {
      return await fn()
    } catch (e) {
      lastErr = e
      if (!RETRYABLE.has(e.kind) || attempt === retries) throw e
      const delay = baseDelay * Math.pow(2, attempt)
      await new Promise((r) => setTimeout(r, delay))
    }
  }
  throw lastErr
}

export const api = {
  get: (url) => request('GET', url),
  post: (url, body) => request('POST', url, body ?? {}),
  put: (url, body) => request('PUT', url, body ?? {}),
  del: (url) => request('DELETE', url),
}

// ---------- 领域 API ----------

export const authApi = {
  status: () => api.get('/api/auth/status'),
  login: (password) => api.post('/api/auth/login', { password }),
  // 登出会让服务端作废此前签发的全部会话（递增会话版本），
  // 因为 token 是无状态的，只清 Cookie 无法让已泄露的 token 失效。
  logout: () => api.post('/api/auth/logout'),
}

export const accountApi = {
  drivers: () => api.get('/api/drivers'),
  list: () => api.get('/api/accounts'),
  create: (d) => api.post('/api/accounts', d),
  update: (id, d) => api.put(`/api/accounts/${id}`, d),
  remove: (id) => api.del(`/api/accounts/${id}`),
  probe: (id) => api.post(`/api/accounts/${id}/probe`),
  profile: (id) => api.get(`/api/accounts/${id}/profile`),
  qrStart: (driverType, config) => api.post('/api/accounts/qr/start', { driver_type: driverType, config: config || '{}' }),
  qrPoll: (token) => api.post('/api/accounts/qr/poll', { token }),
}

export const taskApi = {
  list: () => api.get('/api/tasks'),
  create: (d) => api.post('/api/tasks', d),
  update: (id, d) => api.put(`/api/tasks/${id}`, d),
  remove: (id) => api.del(`/api/tasks/${id}`),
  setStatus: (id, status) => api.post(`/api/tasks/${id}/status`, { status }),
  // 异步启动：返回 { job_id, status: 'running', stage, ... }
  run: (id) => api.post(`/api/tasks/${id}/run`),
  runs: (id, limit = 10) => api.get(`/api/tasks/${id}/runs?limit=${limit}`),
  savedNames: (id, limit = 300) => api.get(`/api/tasks/${id}/saved-names?limit=${limit}`),
  savedCount: (id) => api.get(`/api/tasks/${id}/saved-names/count`),
  clearSavedNames: (id) => api.del(`/api/tasks/${id}/saved-names`),
  // 批量操作：{ ids, action, category_id?, run_mode? } → { total, succeeded, failed, items }
  batch: (d) => api.post('/api/tasks/batch', d),
}

// 异步运行句柄（进度 / 取消 / 批量查）。
// 批量查用于任务列表页：N 个任务同时跑时，一个个 GET 会让浏览器
// 每秒发 N 个请求（SQLite 单连接下还会排队）。
export const jobApi = {
  list: () => api.get('/api/jobs'),
  get: (id) => api.get(`/api/jobs/${id}`),
  batch: (ids) => api.post('/api/jobs/batch', { ids }),
  cancel: (id) => api.post(`/api/jobs/${id}/cancel`),
  remove: (id) => api.del(`/api/jobs/${id}`),
}

export const categoryApi = {
  list: () => api.get('/api/categories'),
  save: (d) => api.post('/api/categories', d),
  remove: (id) => api.del(`/api/categories/${id}`),
  test: (id) => api.post(`/api/categories/${id}/test`),
  logs: (id, limit = 20) => api.get(`/api/categories/${id}/callback-logs?limit=${limit}`),
}

export const shareApi = {
  preview: (d) => api.post('/api/share/preview', d),
}

export const fileApi = {
  list: (accountId, fid) => api.get(`/api/files/list?account_id=${accountId}&fid=${encodeURIComponent(fid || '')}`),
  resolve: (accountId, path) => api.get(`/api/files/resolve?account_id=${accountId}&path=${encodeURIComponent(path)}`),
  mkdir: (accountId, parentFid, name) => api.post('/api/files/mkdir', { account_id: accountId, parent_fid: parentFid, name }),
}

export const ruleApi = {
  list: () => api.get('/api/rules'),
  save: (d) => api.post('/api/rules', d),
  remove: (key) => api.del(`/api/rules/${encodeURIComponent(key)}`),
  test: (d) => api.post('/api/rules/test', d),
}

export const cronApi = {
  next: (expr, from) => api.get(`/api/cron/next?expr=${encodeURIComponent(expr)}${from ? '&from=' + encodeURIComponent(from) : ''}`),
}

export const settingsApi = {
  get: () => api.get('/api/settings'),
  save: (d) => api.put('/api/settings', d),
}

// 仪表盘：一次请求聚合"系统健康吗"所需的全部信息。
// 为什么不是让前端发 6 个请求自己拼：首屏并发请求会在 SQLite 单连接
// （SetMaxOpenConns(1)）上排队，少发请求就是少排队。
export const dashboardApi = {
  get: () => api.get('/api/dashboard'),
}

// 全局搜索：一次请求搜多个域，返回分组结果。
// 参数 scope 可限定搜索范围（task/account/rule/category/run/file）。
export const searchApi = {
  query: (q, scope) =>
    api.get(`/api/search?q=${encodeURIComponent(q)}${scope && scope !== 'all' ? '&scope=' + encodeURIComponent(scope) : ''}`),
}

export const backupApi = {
  list: () => api.get('/api/backup'),
  create: () => api.post('/api/backup'),
  // 下载走原生链接（附件下载），避免把二进制塞进 JSON 客户端。
  downloadUrl: (name) => `/api/backup/download${name ? '?name=' + encodeURIComponent(name) : ''}`,
  restore: (name) => api.post('/api/restore', { name }),
}

// 配置导入/导出：跨实例迁移任务、规则、分类（不含凭据）。
// exportUrl 同样走原生链接（带 Content-Disposition，浏览器直接下载 JSON 文件）。
export const configApi = {
  exportUrl: () => '/api/config/export',
  importPreview: (bundle) => api.post('/api/config/import/preview', bundle),
  import: (bundle) => api.post('/api/config/import', bundle),
}

export const notifyApi = {
  test: (channel) => api.post('/api/notify/test', channel),
}

// ---------- 工具 ----------

export function fmtSize(n) {
  if (!n && n !== 0) return '-'
  if (n < 1024) return n + ' B'
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n, i = -1
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return v.toFixed(v >= 100 ? 0 : 1) + ' ' + units[i]
}

export function fmtTime(t) {
  if (!t) return '-'
  const d = new Date(t)
  if (isNaN(d.getTime())) return t
  const pad = (x) => String(x).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}
