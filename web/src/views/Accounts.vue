<script setup>
// 网盘账号管理：驱动卡片、动态表单、扫码登录、连通性探测。
import { ref, reactive, inject, onMounted, computed } from 'vue'
import Modal from '../components/Modal.vue'
import QRLogin from '../components/QRLogin.vue'
import { accountApi } from '../api'

const toast = inject('toast')

const accounts = ref([])
const drivers = ref([])
const loading = ref(true)

const showEdit = ref(false)
const isEdit = ref(false)
const saving = ref(false)
const probing = ref('')
const probeState = ref({}) // { [id]: { status: 'ok'|'fail', message, at } } 持久显示探测结果
const form = reactive({ id: 0, name: '', driver_type: '', config: '{}' })
const fieldValues = ref({}) // 动态表单值

const qrRef = ref(null)
let qrPendingValues = {}

const currentDriver = computed(() => drivers.value.find(d => d.name === form.driver_type))

async function reload() {
  try {
    const [accs, drs] = await Promise.all([accountApi.list(), accountApi.drivers()])
    accounts.value = accs || []
    drivers.value = drs || []
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    loading.value = false
  }
}
onMounted(reload)

function driverMeta(type) {
  return drivers.value.find(d => d.name === type) || { display_name: type, card_color: '#8a93a6' }
}

function openAdd() {
  isEdit.value = false
  Object.assign(form, { id: 0, name: '', driver_type: drivers.value[0]?.name || '', config: '{}' })
  fieldValues.value = defaultsFor(form.driver_type)
  showEdit.value = true
}

function openEdit(acc) {
  isEdit.value = true
  Object.assign(form, { id: acc.id, name: acc.name, driver_type: acc.driver_type, config: acc.config })
  let parsed = {}
  try { parsed = JSON.parse(acc.config || '{}') } catch { /* ignore */ }
  fieldValues.value = { ...defaultsFor(acc.driver_type), ...parsed }
  showEdit.value = true
}

function defaultsFor(type) {
  const d = drivers.value.find(x => x.name === type)
  const out = {}
  if (d) for (const f of d.fields || []) if (f.default) out[f.name] = f.default
  return out
}

function onDriverChange() {
  fieldValues.value = defaultsFor(form.driver_type)
  if (!form.name) form.name = currentDriver.value?.display_name || ''
}

function onQRSuccess(configJson) {
  qrPendingValues = configJson
  fieldValues.value = { ...defaultsFor(form.driver_type), ...configJson }
  toast('扫码成功，登录凭据已填入', 'success')
}

async function save() {
  if (!form.name.trim()) { toast('请填写账号名称', 'error'); return }
  const cfg = {}
  for (const f of currentDriver.value?.fields || []) {
    const v = (fieldValues.value[f.name] ?? '').toString().trim()
    if (v !== '') cfg[f.name] = v
  }
  // 合并扫码带回的字段（如 device_id/client_id）
  for (const [k, v] of Object.entries(qrPendingValues)) if (!(k in cfg) && v) cfg[k] = v
  saving.value = true
  try {
    const body = { name: form.name.trim(), driver_type: form.driver_type, config: JSON.stringify(cfg) }
    if (isEdit.value) await accountApi.update(form.id, body)
    else await accountApi.create(body)
    toast(isEdit.value ? '账号已更新' : '账号已添加', 'success')
    showEdit.value = false
    reload()
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    saving.value = false
  }
}

async function probe(acc) {
  probing.value = acc.id
  probeState.value[acc.id] = { status: 'loading', message: '正在连接…', at: '' }
  try {
    await accountApi.probe(acc.id)
    probeState.value[acc.id] = { status: 'ok', message: '连接正常', at: timeNow() }
    toast(`「${acc.name}」连接正常`, 'success')
  } catch (e) {
    probeState.value[acc.id] = { status: 'fail', message: e.message || '连接失败', at: timeNow() }
    toast(`「${acc.name}」${e.message}`, 'error')
  } finally {
    probing.value = ''
  }
}

function timeNow() {
  return new Date().toLocaleTimeString('zh-CN', { hour12: false })
}

async function removeAcc(acc) {
  if (!confirm(`确定删除账号「${acc.name}」？`)) return
  try {
    await accountApi.remove(acc.id)
    delete probeState.value[acc.id]
    toast('账号已删除', 'success')
    reload()
  } catch (e) {
    toast(e.message, 'error')
  }
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1 class="page-title">网盘账号</h1>
        <div class="page-desc">管理夸克 / 光鸭账号凭据，支持扫码登录与 Cookie / Token 两种方式。</div>
      </div>
      <div class="page-actions">
        <button class="btn btn-primary" @click="openAdd">＋ 添加账号</button>
      </div>
    </div>

    <div v-if="loading" class="loading-block"><span class="spinner"></span> 加载中…</div>
    <div v-else-if="!accounts.length" class="empty card">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><circle cx="12" cy="8" r="4"/><path d="M4 21c0-4 3.6-6 8-6s8 2 8 6"/></svg>
      <div class="empty-title">还没有网盘账号</div>
      <div class="empty-hint">先添加一个夸克或光鸭账号，才能创建转存任务</div>
      <button class="btn btn-primary mt-8" @click="openAdd">＋ 添加账号</button>
    </div>
    <div v-else class="grid-cards">
      <div v-for="a in accounts" :key="a.id" class="card task-card">
        <div class="task-head">
          <div class="flex" style="gap: 10px">
            <div style="width: 38px; height: 38px; border-radius: 10px; display: flex; align-items: center; justify-content: center; color: #fff; font-weight: 700; font-size: 15px"
              :style="{ background: driverMeta(a.driver_type).card_color || '#8a93a6' }">
              {{ driverMeta(a.driver_type).display_name?.slice(0, 1) }}
            </div>
            <div>
              <div class="task-name" style="cursor: default">{{ a.name }}</div>
              <div class="muted" style="font-size: 12px">{{ driverMeta(a.driver_type).display_name }}（{{ a.driver_type }}）</div>
            </div>
          </div>
          <span class="badge" :class="a.is_active ? 'badge-success' : 'badge-gray'"><span class="dot"></span>{{ a.is_active ? '启用' : '停用' }}</span>
        </div>
        <div v-if="probeState[a.id]" class="probe-status" :class="probeState[a.id].status">
          <template v-if="probeState[a.id].status === 'ok'"><span class="dot dot-ok"></span>{{ probeState[a.id].message }}<span class="probe-time">· {{ probeState[a.id].at }}</span></template>
          <template v-else-if="probeState[a.id].status === 'fail'"><span class="dot dot-fail"></span>{{ probeState[a.id].message }}<span class="probe-time">· {{ probeState[a.id].at }}</span></template>
          <template v-else><span class="spinner spinner-sm"></span> 正在连接…</template>
        </div>
        <div class="task-foot">
          <button class="btn btn-sm" :disabled="probing === a.id" @click="probe(a)">
            <span v-if="probing === a.id" class="spinner"></span>
            <span v-else>⚡ 测试连接</span>
          </button>
          <button class="btn btn-sm" @click="openEdit(a)">编辑</button>
          <span class="spacer"></span>
          <button class="btn btn-sm btn-danger btn-ghost" @click="removeAcc(a)">删除</button>
        </div>
      </div>
    </div>

    <!-- 添加/编辑账号 -->
    <Teleport to="body">
      <div v-if="showEdit" class="modal-overlay" @mousedown.self="showEdit = false">
        <div class="modal">
          <div class="modal-head">
            <div class="modal-title">{{ isEdit ? '编辑账号' : '添加账号' }}</div>
            <button class="modal-close" @click="showEdit = false">✕</button>
          </div>
          <div class="modal-body">
            <div class="field" v-if="!isEdit">
              <div class="field-label">网盘类型</div>
              <div class="flex" style="gap: 10px">
                <div v-for="d in drivers" :key="d.name" @click="form.driver_type = d.name; onDriverChange()"
                  style="flex: 1; padding: 12px; border-radius: 10px; border: 1.5px solid var(--border); cursor: pointer; transition: all .15s"
                  :style="form.driver_type === d.name ? 'border-color: var(--primary); background: var(--primary-soft)' : ''">
                  <div class="flex" style="gap: 8px">
                    <div style="width: 26px; height: 26px; border-radius: 7px; display: flex; align-items: center; justify-content: center; color: #fff; font-weight: 700; font-size: 12px"
                      :style="{ background: d.card_color || '#8a93a6' }">{{ d.display_name?.slice(0, 1) }}</div>
                    <div style="font-weight: 600; font-size: 13.5px">{{ d.display_name }}</div>
                  </div>
                  <div class="flex" style="gap: 4px; margin-top: 8px; flex-wrap: wrap">
                    <span v-for="t in d.card_tags" :key="t" class="tag" style="font-size: 10.5px; height: 18px">{{ t }}</span>
                  </div>
                </div>
              </div>
            </div>
            <div class="field">
              <div class="field-label">账号名称 <span class="req">*</span></div>
              <input class="input" v-model="form.name" placeholder="例如：夸克主号 / 光鸭小号" />
            </div>

            <template v-for="f in currentDriver?.fields || []" :key="f.name">
              <div class="field" v-if="f.type !== 'select' || true">
                <div class="field-label">{{ f.label }}</div>
                <select v-if="f.type === 'select'" class="select" v-model="fieldValues[f.name]">
                  <option v-for="o in f.options" :key="o.value" :value="o.value">{{ o.label }}</option>
                </select>
                <input v-else-if="f.type === 'password'" class="input" type="password" v-model="fieldValues[f.name]" :placeholder="f.default || ''" />
                <input v-else class="input" v-model="fieldValues[f.name]" :placeholder="f.default || ''" />
              </div>
            </template>

            <div v-if="currentDriver?.qr_login" class="card card-pad" style="background: var(--surface-2); border-style: dashed">
              <div class="flex">
                <div class="flex-1">
                  <div style="font-weight: 600; font-size: 13.5px">扫码登录</div>
                  <div class="field-hint" style="margin-top: 4px">不想手动抓 Cookie / Token？用网盘 App 扫码自动获取。</div>
                </div>
                <button class="btn btn-primary" @click="qrRef.open(form.driver_type)">📱 扫码登录</button>
              </div>
            </div>
          </div>
          <div class="modal-foot">
            <span class="flex-1"></span>
            <button class="btn" @click="showEdit = false">取消</button>
            <button class="btn btn-primary" :disabled="saving" @click="save">
              <span v-if="saving" class="spinner" style="border-top-color: #fff"></span>
              <span v-else>{{ isEdit ? '保存' : '添加并验证' }}</span>
            </button>
          </div>
        </div>
      </div>
    </Teleport>

    <QRLogin ref="qrRef" @success="onQRSuccess" />
  </div>
</template>
