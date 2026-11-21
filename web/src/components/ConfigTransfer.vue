<script setup>
// 配置导入 / 导出弹窗。
//
// 为什么放在设置页而不是任务页：这是「整个实例的配置」而不是某个任务的配置，
// 和「备份/还原」是一对——备份保的是「本机完整状态（含凭据）」，
// 配置导出保的是「可迁移的编排逻辑（不含凭据）」。两者用途不同，放在一起容易被混淆，
// 所以这里用清晰的文案说明区别，并且只把「不含凭据」这一点作为核心卖点。
//
// 导入是破坏性操作（新建任务、覆盖同名规则），因此：
//   1. 先预览，让用户看到「会发生什么 / 哪些会被跳过」；
//   2. 校验不通过则禁用「确认导入」；
//   3. 导入完成后展示逐条结果（成功/跳过/失败），而不是一句笼统的"导入成功"。
import { ref, computed, inject } from 'vue'
import Modal from './Modal.vue'
import { configApi } from '../api'

const toast = inject('toast')
const confirmDialog = inject('confirmDialog')

const visible = ref(false)
const tab = ref('export') // 'export' | 'import'

// ---------- 导出 ----------
const exporting = ref(false)

async function doExport() {
  exporting.value = true
  try {
    // 直接触发浏览器下载：后端返回 Content-Disposition。
    const a = document.createElement('a')
    a.href = configApi.exportUrl()
    a.download = ''
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    toast('已开始下载配置文件', 'success')
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    exporting.value = false
  }
}

// ---------- 导入 ----------
const rawJson = ref('')
const fileName = ref('')
const parsing = ref(false)
const previewing = ref(false)
const importing = ref(false)
const preview = ref(null)
const applyResult = ref(null)
const parseError = ref('')

const canImport = computed(() => {
  return !!preview.value && preview.value.valid && !previewing.value && !importing.value
})

function open() {
  visible.value = true
  tab.value = 'export'
  rawJson.value = ''
  fileName.value = ''
  preview.value = null
  applyResult.value = null
  parseError.value = ''
}

// 解析用户粘贴或上传的 JSON。只做语法与版本校验，
// 真正的业务校验交给后端 preview（它才知道账号/分类在不在）。
async function parseAndPreview() {
  parseError.value = ''
  preview.value = null
  applyResult.value = null

  const text = rawJson.value.trim()
  if (!text) {
    parseError.value = '请粘贴配置内容或选择文件'
    return
  }

  let bundle
  try {
    bundle = JSON.parse(text)
  } catch (e) {
    parseError.value = 'JSON 解析失败：' + e.message
    return
  }

  previewing.value = true
  try {
    preview.value = await configApi.importPreview(bundle)
    if (!preview.value.valid) {
      parseError.value = (preview.value.errors || []).join('；')
    }
  } catch (e) {
    parseError.value = e.message
  } finally {
    previewing.value = false
  }
}

// 文件上传：读成文本后走同一套预览流程。
function onFilePick(ev) {
  const file = ev.target.files && ev.target.files[0]
  if (!file) return
  fileName.value = file.name
  const reader = new FileReader()
  reader.onload = () => {
    rawJson.value = String(reader.result || '')
    parseAndPreview()
  }
  reader.onerror = () => {
    parseError.value = '读取文件失败'
  }
  reader.readAsText(file)
}

async function doImport() {
  if (!canImport.value) return

  // 导入会新建/覆盖数据，属于破坏性操作：再用确认弹层兜一层。
  const ok = await confirmDialog({
    title: '导入配置',
    body: preview.value
      ? `将创建 ${preview.value.summary.task_count} 个任务、` +
        `${preview.value.summary.category_count} 个分类、` +
        `${preview.value.summary.rule_count} 条规则（同名的任务与规则会被覆盖）。`
      : '确认导入这份配置？',
    warn: '导入不会触碰账号凭据；若目标实例缺少同名账号，相关任务会被跳过。此操作不可撤销。',
    confirmText: '确认导入',
  })
  if (!ok) return

  importing.value = true
  try {
    applyResult.value = await configApi.import(JSON.parse(rawJson.value))
    const r = applyResult.value
    toast(
      `导入完成：新建 ${r.tasks_created} 个任务、更新 ${r.tasks_updated} 个` +
        `、跳过 ${r.tasks_skipped} 个`,
      r.tasks_skipped > 0 ? 'info' : 'success',
      6000,
    )
  } catch (e) {
    toast(e.message, 'error', 6000)
  } finally {
    importing.value = false
  }
}

defineExpose({ open })
</script>

<template>
  <Modal v-model="visible" title="配置导入 / 导出" size="lg">
    <div class="tabs">
      <button class="tab" :class="{ active: tab === 'export' }" @click="tab = 'export'">导出</button>
      <button class="tab" :class="{ active: tab === 'import' }" @click="tab = 'import'">导入</button>
    </div>

    <!-- 导出 -->
    <template v-if="tab === 'export'">
      <div class="confirm-body">
        <p style="margin-top: 0">
          导出会把<b>任务、命名规则、分类与回调配置</b>打包成一个 JSON 文件，
          用于换机、重装或多机同步。
        </p>
        <div class="confirm-warn">
          <b>不包含任何凭据。</b>
          导出文件里只有「该账号是否配过凭据」这样的布尔标记，
          不会有 cookie 或 token 明文——所以你可以在论坛、群里传这份配置，
          而不会把网盘账号一起送出去。
        </div>
        <ul style="margin-bottom: 0">
          <li><b>备份</b>（设置页另一个功能）保的是本机完整状态，<b>含凭据</b>，用于灾备</li>
          <li><b>配置导出</b>保的是可迁移的编排逻辑，用于换机 / 分享 / 版本化</li>
        </ul>
      </div>
      <div class="flex" style="margin-top: 14px">
        <span class="field-hint" style="margin-top: 0">
          导出文件含 schema 版本号，旧版本程序无法直接读取高版本配置。
        </span>
        <span class="flex-1"></span>
        <button class="btn btn-primary" :disabled="exporting" @click="doExport">
          <span v-if="exporting" class="spinner" style="border-top-color:#fff"></span>
          <span v-else>⬇ 下载配置文件</span>
        </button>
      </div>
    </template>

    <!-- 导入 -->
    <template v-else>
      <div class="confirm-body">
        <p style="margin-top: 0">
          粘贴或上传之前导出的 JSON，会先<b>预检</b>告诉你将发生什么，再决定是否写入。
          账号凭据不受影响，也不会被导入。
        </p>
      </div>

      <div class="flex" style="margin: 10px 0; gap: 8px">
        <label class="btn btn-sm" style="cursor: pointer">
          📁 选择 JSON 文件
          <input type="file" accept=".json,application/json" style="display:none" @change="onFilePick" />
        </label>
        <span v-if="fileName" class="tag">{{ fileName }}</span>
      </div>

      <div class="field">
        <div class="field-label">或直接粘贴配置内容</div>
        <textarea class="textarea mono" rows="6" v-model="rawJson"
          placeholder='{"schema_version":1,"tasks":[...]}'></textarea>
      </div>

      <div class="flex" style="margin-top: 10px">
        <button class="btn btn-primary" :disabled="previewing || !rawJson.trim()"
          @click="parseAndPreview">
          <span v-if="previewing" class="spinner" style="border-top-color:#fff"></span>
          <span v-else>🔍 预检</span>
        </button>
      </div>

      <div v-if="parseError" class="confirm-warn" style="background: var(--danger-soft); color: var(--danger)">
        {{ parseError }}
      </div>

      <!-- 预检结果 -->
      <div v-if="preview" class="card card-pad" style="margin-top: 12px">
        <div class="flex mb-8">
          <span class="badge" :class="preview.valid ? 'badge-success' : 'badge-danger'">
            <span class="dot"></span>{{ preview.valid ? '校验通过' : '校验未通过' }}
          </span>
          <span class="muted" style="font-size: 12.5px">
            将创建 {{ preview.summary.task_count }} 个任务 ·
            {{ preview.summary.category_count }} 个分类 ·
            {{ preview.summary.rule_count }} 条规则
          </span>
        </div>

        <div v-if="preview.task_names && preview.task_names.length" style="font-size: 12.5px; color: var(--text-2)">
          <div style="font-weight: 600; margin-bottom: 4px">任务</div>
          <div class="mono" style="max-height: 120px; overflow: auto">
            <div v-for="n in preview.task_names" :key="n">· {{ n }}</div>
          </div>
        </div>

        <div v-if="preview.skipped && preview.skipped.length"
          class="confirm-warn" style="margin-top: 10px">
          <div style="font-weight: 600; margin-bottom: 4px">
            {{ preview.skipped.length }} 个任务会被跳过
          </div>
          <div v-for="(s, i) in preview.skipped" :key="i">· {{ s }}</div>
        </div>

        <div v-if="preview.warnings && preview.warnings.length"
          style="margin-top: 10px; font-size: 12.5px; color: var(--warn); line-height: 1.7">
          <div style="font-weight: 600">需要注意</div>
          <div v-for="(w, i) in preview.warnings" :key="i">· {{ w }}</div>
        </div>
      </div>

      <!-- 导入结果 -->
      <div v-if="applyResult" class="card card-pad" style="margin-top: 12px">
        <div class="flex mb-8">
          <span class="badge badge-success"><span class="dot"></span>导入完成</span>
        </div>
        <div class="table-wrap">
          <table class="table">
            <tbody>
              <tr><td>新建任务</td><td class="num">{{ applyResult.tasks_created }}</td></tr>
              <tr><td>更新任务</td><td class="num">{{ applyResult.tasks_updated }}</td></tr>
              <tr><td>跳过任务</td><td class="num">{{ applyResult.tasks_skipped }}</td></tr>
              <tr><td>新建分类</td><td class="num">{{ applyResult.categories_created }}</td></tr>
              <tr><td>写入规则</td><td class="num">{{ applyResult.rules_written }}</td></tr>
            </tbody>
          </table>
        </div>
        <div v-if="applyResult.failed && applyResult.failed.length"
          class="confirm-warn" style="margin-top: 10px">
          <div style="font-weight: 600; margin-bottom: 4px">失败项</div>
          <div v-for="(f, i) in applyResult.failed" :key="i">· {{ f }}</div>
        </div>
      </div>
    </template>

    <template #footer>
      <span class="flex-1"></span>
      <button class="btn" @click="visible = false">关闭</button>
      <button v-if="tab === 'import'" class="btn btn-primary"
        :disabled="!canImport" @click="doImport">
        <span v-if="importing" class="spinner" style="border-top-color:#fff"></span>
        <span v-else>确认导入</span>
      </button>
    </template>
  </Modal>
</template>