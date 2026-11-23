<script setup>
// 任务分类与回调：分类列表 + 编辑弹层 + 回调日志 + 使用说明。
//
// 这是设置页里第二复杂的区块，有 4 个弹层：
//   ① 分类编辑  ② 回调日志  ③ 使用说明（变量表）  ④ 测试结果展示
// 早先这些都在 Settings.vue 里，变量名（catForm / catLogs / showCatHelp…）
// 与通知渠道的（chForm / showChannel…）混在同一作用域。
import { ref, reactive, inject, onMounted } from 'vue'
import Modal from '../Modal.vue'
import { categoryApi, fmtTime } from '../../api'

const toast = inject('toast')
const confirmDialog = inject('confirmDialog')

const categories = ref([])
const showCatEdit = ref(false)
const editingCat = ref(false)
const savingCat = ref(false)
const testingCat = ref(false)
const catTestResult = ref(null)
const showCatHelp = ref(false)
const showCatLogs = ref(false)
const catLogs = ref([])
const catLogsLoading = ref(false)
const logsCat = ref(null)
const catForm = reactive({
  id: 0, name: '', callback_enabled: false, callback_mode: 'round',
  callback_method: 'POST', callback_url: '', callback_headers: '', callback_body: '',
  callback_timeout_sec: 15, skip_zero: false, sort_order: 0,
})

async function loadCategories() {
  try {
    categories.value = (await categoryApi.list()) || []
  } catch (e) {
    toast(e.message, 'error')
  }
}

function openCatNew() {
  editingCat.value = false
  Object.assign(catForm, {
    id: 0, name: '', callback_enabled: false, callback_mode: 'round',
    callback_method: 'POST', callback_url: '', callback_headers: '', callback_body: '',
    callback_timeout_sec: 15, skip_zero: false, sort_order: categories.value.length,
  })
  catTestResult.value = null
  showCatEdit.value = true
}

function openCatEdit(c) {
  editingCat.value = true
  Object.assign(catForm, {
    id: c.id, name: c.name, callback_enabled: c.callback_enabled, callback_mode: c.callback_mode,
    callback_method: c.callback_method, callback_url: c.callback_url,
    callback_headers: c.callback_headers || '', callback_body: c.callback_body || '',
    callback_timeout_sec: c.callback_timeout_sec || 15, skip_zero: !!c.skip_zero,
    sort_order: c.sort_order || 0,
  })
  catTestResult.value = null
  showCatEdit.value = true
}

async function saveCat() {
  if (!catForm.name.trim()) { toast('请填写分类名称', 'error'); return }
  savingCat.value = true
  try {
    await categoryApi.save({ ...catForm, name: catForm.name.trim() })
    toast(editingCat.value ? '分类已更新' : '分类已创建', 'success')
    showCatEdit.value = false
    loadCategories()
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    savingCat.value = false
  }
}

async function removeCat(c) {
  const ok = await confirmDialog({
    title: '删除任务分类',
    body: `删除分类「${c.name}」？`,
    warn: `其下 ${c.task_count} 个任务将变为未分类，该分类的回调配置与回调日志会一并删除。`,
    confirmText: '删除',
    variant: 'danger',
  })
  if (!ok) return
  try {
    await categoryApi.remove(c.id)
    toast('分类已删除', 'success')
    loadCategories()
  } catch (e) {
    toast(e.message, 'error')
  }
}

async function testCat() {
  if (!catForm.id) { toast('请先保存分类再测试', 'error'); return }
  testingCat.value = true
  catTestResult.value = null
  try {
    catTestResult.value = await categoryApi.test(catForm.id)
  } catch (e) {
    catTestResult.value = { ok: false, error: e.message }
  } finally {
    testingCat.value = false
  }
}

async function openCatLogs(c) {
  logsCat.value = c
  showCatLogs.value = true
  catLogsLoading.value = true
  try {
    catLogs.value = (await categoryApi.logs(c.id)) || []
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    catLogsLoading.value = false
  }
}

defineExpose({ loadCategories })

// 必须自己拉一次：早先这份逻辑在 Settings.vue 的 reload() 里，
// 拆分后父容器不再知道分类的存在。少了这一行会永远显示"还没有分类"。
onMounted(loadCategories)
</script>

<template>
  <div class="card card-pad mb-16">
    <div class="flex mb-16">
      <div>
        <div class="flex" style="margin-bottom: 2px">
          <div class="card-title" style="margin-bottom: 0">任务分类与回调</div>
          <button class="btn btn-sm btn-ghost" style="height: 24px" @click="showCatHelp = true">❓ 使用说明</button>
        </div>
        <div class="page-desc">给任务分组；分类可配置转存完成后回调外部接口，通知其他系统处理文件。</div>
      </div>
      <span class="flex-1"></span>
      <button class="btn btn-primary" @click="openCatNew">＋ 添加分类</button>
    </div>
    <div v-if="!categories.length" class="empty" style="padding: 22px">
      <div class="empty-hint">还没有分类。创建分类并在任务编辑里选择，即可按分类触发回调。</div>
    </div>
    <div v-else style="display: grid; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr)); gap: 10px">
      <div v-for="c in categories" :key="c.id" class="channel-card">
        <div class="flex">
          <div style="font-weight: 600">{{ c.name }}</div>
          <span class="tag">{{ c.callback_mode === 'per_task' ? '单任务回调' : '轮次回调' }}</span>
          <span class="flex-1"></span>
          <span class="badge" :class="c.callback_enabled ? 'badge-success' : 'badge-gray'">
            <span class="dot"></span>{{ c.callback_enabled ? '回调开启' : '仅分组' }}
          </span>
        </div>
        <div class="muted mono ellipsis" style="font-size: 11.5px; margin-top: 6px" :title="c.callback_url">
          {{ c.callback_enabled ? c.callback_method + ' ' + (c.callback_url || '未配置 URL') : '未启用回调' }}
        </div>
        <div class="flex" style="margin-top: 8px; gap: 6px; flex-wrap: wrap">
          <span class="muted" style="font-size: 12px">{{ c.task_count }} 个任务</span>
          <template v-if="c.last_callback_at">
            <span class="badge" :class="c.last_callback_ok ? 'badge-success' : 'badge-danger'" style="height: 18px; font-size: 11px">
              <span class="dot"></span>{{ c.last_callback_ok ? '上次回调成功' : '上次回调失败' }} {{ c.last_callback_at }}
            </span>
          </template>
          <span class="flex-1"></span>
          <button class="btn btn-sm" @click="openCatEdit(c)">编辑</button>
          <button class="btn btn-sm" @click="openCatLogs(c)">回调日志</button>
          <button class="btn btn-sm btn-danger btn-ghost" @click="removeCat(c)">删除</button>
        </div>
      </div>
    </div>

    <!-- 分类编辑 -->
    <Modal v-model="showCatEdit" size="lg" :title="editingCat ? '编辑分类' : '添加分类'">
      <div class="form-row">
        <div class="field">
          <div class="field-label">分类名称 <span class="req">*</span></div>
          <input class="input" v-model="catForm.name" placeholder="例如：Emby 刮削刷新" />
        </div>
        <div class="field">
          <div class="field-label">回调超时（秒）</div>
          <input class="input" type="number" min="1" max="120" v-model="catForm.callback_timeout_sec" />
        </div>
      </div>
      <label class="checkbox">
        <input type="checkbox" v-model="catForm.callback_enabled" />
        启用回调（关闭则仅作为任务分组，不调用接口）
      </label>
      <label class="checkbox" v-if="catForm.callback_enabled">
        <input type="checkbox" v-model="catForm.skip_zero" />
        0 转存不回调（本次没有任何新文件且无失败时不调用接口；有失败仍会回调）
      </label>
      <template v-if="catForm.callback_enabled">
        <div class="form-row">
          <div class="field">
            <div class="field-label">触发时机</div>
            <select class="select" v-model="catForm.callback_mode">
              <option value="round">分类轮次完成（该类任务一批跑完后回调一次，汇总结果）</option>
              <option value="per_task">单任务完成（每个任务转存完成即回调）</option>
            </select>
          </div>
          <div class="field">
            <div class="field-label">请求方法</div>
            <select class="select" v-model="catForm.callback_method">
              <option value="POST">POST（Body 模板）</option>
              <option value="GET">GET（参数在 URL 上）</option>
            </select>
          </div>
        </div>
        <div class="field">
          <div class="field-label">
            回调 URL <span class="req" v-if="catForm.callback_enabled">*</span>
            <span class="flex-1"></span>
            <span class="field-hint" style="margin-top: 0">
              支持 {变量}，GET 查询值自动 URL 编码；
              <a style="color: var(--primary); cursor: pointer" @click="showCatHelp = true">查看变量表</a>
            </span>
          </div>
          <input class="input mono" v-model="catForm.callback_url"
            :placeholder="catForm.callback_method === 'GET'
              ? 'http://192.168.1.10:8080/api/refresh?dir={save_path}&count={transfer_count}'
              : 'http://192.168.1.10:8080/api/hook'" />
        </div>
        <div class="field">
          <div class="field-label">自定义请求头（JSON，可选）</div>
          <input class="input mono" v-model="catForm.callback_headers" placeholder='{"Authorization": "Bearer xxx"}' />
        </div>
        <div class="field" v-if="catForm.callback_method === 'POST'">
          <div class="field-label">Body 模板（留空用默认模板）</div>
          <textarea class="textarea mono" rows="5" v-model="catForm.callback_body"
            placeholder='{"event":"transfer.completed","category":"{category}","status":"{status}","count":{transfer_count},"files":{transferred_files_json},"path":"{save_path}"}'></textarea>
        </div>
        <div v-if="catTestResult" class="card card-pad" style="padding: 10px 12px; font-size: 12.5px"
          :style="catTestResult.ok ? 'background: var(--success-soft)' : 'background: var(--danger-soft)'">
          <div style="font-weight: 600">{{ catTestResult.ok ? '✅ 测试成功' : '❌ 测试失败' }}
            <span v-if="catTestResult.status_code" class="muted">HTTP {{ catTestResult.status_code }}</span>
          </div>
          <div class="mono" style="margin-top: 4px; overflow-wrap: anywhere; color: var(--text-2)">
            {{ catTestResult.response || catTestResult.error }}
          </div>
        </div>
      </template>
      <template #footer>
        <button v-if="catForm.id" class="btn" :disabled="testingCat" @click="testCat">
          <span v-if="testingCat" class="spinner"></span>
          <span v-else>发送测试</span>
        </button>
        <span v-if="!catForm.id" class="field-hint" style="margin-top: 0">保存后可发送测试</span>
        <span class="flex-1"></span>
        <button class="btn" @click="showCatEdit = false">取消</button>
        <button class="btn btn-primary" :disabled="savingCat" @click="saveCat">保存</button>
      </template>
    </Modal>

    <!-- 回调日志 -->
    <Modal v-model="showCatLogs" size="lg" :title="`「${logsCat?.name || ''}」回调日志`">
      <div v-if="catLogsLoading" class="loading-block"><span class="spinner"></span></div>
      <div v-else-if="!catLogs.length" class="empty">
        <div class="empty-title">还没有回调记录</div>
        <div class="empty-hint">分类回调触发后会在这里记录每次调用的结果</div>
      </div>
      <div v-else style="display: flex; flex-direction: column; gap: 8px">
        <div v-for="l in catLogs" :key="l.id" class="card card-pad" style="padding: 10px 12px">
          <div class="flex" style="flex-wrap: wrap; gap: 6px">
            <span class="badge" :class="l.ok ? 'badge-success' : 'badge-danger'">
              <span class="dot"></span>{{ l.ok ? '成功' : '失败' }}
            </span>
            <span v-if="l.status_code" class="badge badge-gray">HTTP {{ l.status_code }}</span>
            <span class="tag">{{ l.kind === 'round' ? '轮次' : l.kind === 'test' ? '测试' : '单任务' }}</span>
            <span class="muted" style="font-size: 12px">{{ fmtTime(l.created_at) }}</span>
          </div>
          <div class="mono ellipsis" style="font-size: 11.5px; margin-top: 6px" :title="l.target">{{ l.target }}</div>
          <pre v-if="l.response" class="log-tree" style="margin-top: 6px; max-height: 120px; padding: 8px 10px">{{ l.response }}</pre>
        </div>
      </div>
      <template #footer>
        <span class="flex-1"></span>
        <button class="btn" @click="showCatLogs = false">关闭</button>
      </template>
    </Modal>

    <!-- 分类回调使用说明 -->
    <Modal v-model="showCatHelp" size="lg" title="任务分类与回调 · 使用说明">
      <div style="font-size: 13.5px; line-height: 1.8; color: var(--text-2)">
        <p style="margin: 0 0 10px">
          <b style="color: var(--text)">这是什么：</b>把任务归入分类，转存完成后自动按分类配置调用你的接口，让其他系统（媒体库刷新、字幕下载、文件整理脚本…）接着处理文件。
        </p>
        <p style="margin: 0 0 10px"><b style="color: var(--text)">触发时机：</b></p>
        <ul style="margin: 0 0 10px; padding-left: 20px">
          <li><b>单任务完成</b>：该分类下每个任务转存完成就回调一次，参数里带这个任务的结果。</li>
          <li><b>分类轮次完成</b>：一批定时触发（或一次手动运行）中，该分类全部已启动任务都结束后回调<b>一次</b>，参数汇总每个任务的结果。定时批量任务间有 1–3 分钟随机延迟，轮次会等齐再回调。</li>
        </ul>
        <p style="margin: 0 0 10px">
          <b style="color: var(--text)">可用变量（URL 和 Body 模板里用 <code class="mono">{变量名}</code> 引用）：</b>
        </p>
        <div class="table-wrap" style="margin-bottom: 10px">
          <table class="table">
            <thead><tr><th style="width: 190px">变量</th><th>说明</th></tr></thead>
            <tbody>
              <tr><td class="mono">{task_name}</td><td>任务名称（轮次模式下无单一任务概念，用 task_results_json）</td></tr>
              <tr><td class="mono">{task_id}</td><td>任务 ID</td></tr>
              <tr><td class="mono">{category}</td><td>分类名称</td></tr>
              <tr><td class="mono">{status}</td><td>success / partial / failed（轮次模式为汇总状态）</td></tr>
              <tr><td class="mono">{transfer_count}</td><td>本次新转存文件数</td></tr>
              <tr><td class="mono">{transferred_files_json}</td><td>新文件名 JSON 数组，如 ["a.mkv","b.mkv"]</td></tr>
              <tr><td class="mono">{transferred_files}</td><td>新文件名，换行分隔</td></tr>
              <tr><td class="mono">{save_path}</td><td>保存路径</td></tr>
              <tr><td class="mono">{trigger}</td><td>manual（手动）/ schedule（定时）</td></tr>
              <tr><td class="mono">{finished_at}</td><td>完成时间</td></tr>
              <tr><td class="mono">{message}</td><td>结果摘要（如「转存 3 个文件」）</td></tr>
              <tr><td class="mono">{task_results_json}</td><td>仅轮次模式：每个任务结果的 JSON 数组</td></tr>
              <tr><td class="mono">{round_id} / {task_count} / {total_transferred}</td><td>仅轮次模式：轮次 ID / 任务数 / 总转存数</td></tr>
            </tbody>
          </table>
        </div>
        <p style="margin: 0 0 10px"><b style="color: var(--text)">示例：</b></p>
        <pre class="log-tree" style="margin-bottom: 10px">GET  http://192.168.1.10:8080/api/refresh?dir={save_path}&count={transfer_count}

POST Body（默认模板，可自行修改）：
{"event":"transfer.completed","category":"{category}","status":"{status}","count":{transfer_count},"files":{transferred_files_json},"path":"{save_path}"}</pre>
        <p style="margin: 0">
          <b style="color: var(--text)">注意事项：</b>勾选「0 转存不回调」后，本次（轮次模式按整轮汇总）没有任何新文件且无失败时不调用接口，有失败仍会回调；HTTP 2xx 视为成功；超时默认 15 秒（可配）；调用失败不自动重试，结果记录在「回调日志」里；请求头（可能含密钥）明文存于本地数据库；服务重启会丢失进行中的轮次；下游接口建议使用内网地址。
        </p>
      </div>
      <template #footer>
        <button class="btn" @click="showCatHelp = false">我知道了</button>
      </template>
    </Modal>
  </div>
</template>