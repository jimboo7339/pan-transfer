// 轻量 API 客户端：JSON、错误冒泡、Bearer 会话。
let token = localStorage.getItem('pt_token') || ''
let onUnauthorized = null

export function setToken(t) {
  token = t || ''
  if (token) localStorage.setItem('pt_token', token)
  else localStorage.removeItem('pt_token')
}
export function getToken() { return token }
export function setUnauthorizedHandler(fn) { onUnauthorized = fn }

async function request(method, url, body) {
  const headers = { 'Content-Type': 'application/json' }
  if (token) headers['Authorization'] = 'Bearer ' + token
  const opts = { method, headers }
  if (body !== undefined) opts.body = JSON.stringify(body)
  let resp, data
  try {
    resp = await fetch(url, opts)
    data = await resp.json()
  } catch (e) {
    throw new Error('网络请求失败：' + e.message)
  }
  if (!resp.ok) {
    const msg = data && data.error ? data.error.message : `HTTP ${resp.status}`
    if (resp.status === 401 && onUnauthorized) onUnauthorized()
    const err = new Error(msg)
    err.code = data && data.error ? data.error.code : ''
    throw err
  }
  return data && 'data' in data ? data.data : data
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
  run: (id) => api.post(`/api/tasks/${id}/run`),
  runs: (id, limit = 10) => api.get(`/api/tasks/${id}/runs?limit=${limit}`),
  savedNames: (id, limit = 300) => api.get(`/api/tasks/${id}/saved-names?limit=${limit}`),
  savedCount: (id) => api.get(`/api/tasks/${id}/saved-names/count`),
  clearSavedNames: (id) => api.del(`/api/tasks/${id}/saved-names`),
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
