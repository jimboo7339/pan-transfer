<script setup>
// 命名正则规则维护：内置规则覆盖 + 自定义规则 + 试算测试。
import { ref, reactive, inject, onMounted } from 'vue'
import Modal from '../components/Modal.vue'
import { ruleApi } from '../api'

const toast = inject('toast')

const rules = ref([])
const loading = ref(true)
const showEdit = ref(false)
const saving = ref(false)
const form = reactive({ key: '', label: '', pattern: '', replace: '', enabled: true })
const isBuiltin = ref(false)

// 试算
const test = reactive({ name: '第01集.mp4', result: null, testing: false })

async function reload() {
  loading.value = true
  try {
    rules.value = (await ruleApi.list()) || []
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    loading.value = false
  }
}
onMounted(reload)

function openNew() {
  isBuiltin.value = false
  Object.assign(form, { key: '', label: '', pattern: '', replace: '', enabled: true })
  showEdit.value = true
}

function openEdit(r) {
  isBuiltin.value = r.builtin
  Object.assign(form, { key: r.key, label: r.label, pattern: r.pattern, replace: r.replace, enabled: r.enabled })
  showEdit.value = true
}

async function save() {
  if (!form.key.trim()) { toast('请填写规则标识', 'error'); return }
  saving.value = true
  try {
    await ruleApi.save({ ...form })
    toast('规则已保存', 'success')
    showEdit.value = false
    reload()
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    saving.value = false
  }
}

async function toggleEnabled(r) {
  try {
    await ruleApi.save({ key: r.key, label: r.label, pattern: r.pattern, replace: r.replace, enabled: !r.enabled })
    r.enabled = !r.enabled
  } catch (e) {
    toast(e.message, 'error')
  }
}

async function resetRule(r) {
  if (!confirm(`恢复「${r.label || r.key}」为内置默认？自定义修改将被清除。`)) return
  try {
    await ruleApi.remove(r.key)
    toast('已恢复默认', 'success')
    reload()
  } catch (e) {
    toast(e.message, 'error')
  }
}

async function removeRule(r) {
  if (!confirm(`确定删除规则「${r.label || r.key}」？`)) return
  try {
    await ruleApi.remove(r.key)
    toast('规则已删除', 'success')
    reload()
  } catch (e) {
    toast(e.message, 'error')
  }
}

async function runTest() {
  if (!test.name) { test.result = null; return }
  test.testing = true
  try {
    test.result = await ruleApi.test({ name: test.name, pattern: form.pattern, replace: form.replace })
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    test.testing = false
  }
}
</script>

<template>
  <div>
    <div class="page-head">
      <div>
        <h1 class="page-title">命名规则</h1>
        <div class="page-desc">魔法正则规则：任务里用 <code class="mono">$规则名</code> 引用；内置规则可覆盖，也可新增自定义规则。</div>
      </div>
      <div class="page-actions">
        <button class="btn btn-primary" @click="openNew">＋ 新增规则</button>
      </div>
    </div>

    <div v-if="loading" class="loading-block"><span class="spinner"></span> 加载中…</div>
    <template v-else>
      <!-- 桌面端：表格 -->
      <div class="card rules-table-wrap">
        <table class="table">
          <thead>
            <tr>
              <th style="width: 150px">规则</th>
              <th>匹配正则（pattern）</th>
              <th>替换模板（replace）</th>
              <th style="width: 90px">状态</th>
              <th style="width: 170px">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in rules" :key="r.key">
              <td>
                <div class="mono" style="font-weight: 600">{{ r.key }}</div>
                <div class="muted" style="font-size: 12px">{{ r.label || '自定义规则' }}</div>
              </td>
              <td class="mono ellipsis" style="max-width: 280px" :title="r.pattern">{{ r.pattern || '(空 = 全部)' }}</td>
              <td class="mono ellipsis" style="max-width: 220px" :title="r.replace">{{ r.replace || '—' }}</td>
              <td>
                <label class="switch" @click.prevent="toggleEnabled(r)">
                  <input type="checkbox" :checked="r.enabled" />
                  <span class="track"></span>
                </label>
              </td>
              <td>
                <div class="flex">
                  <button class="btn btn-sm" @click="openEdit(r)">编辑</button>
                  <button v-if="r.overridden" class="btn btn-sm" @click="resetRule(r)">恢复默认</button>
                  <button v-if="r.custom" class="btn btn-sm btn-danger btn-ghost" @click="removeRule(r)">删除</button>
                  <span v-if="r.builtin && !r.overridden" class="muted" style="font-size: 12px">内置</span>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- 移动端：卡片列表（表格在窄屏会挤压/截断，操作按钮也点不到） -->
      <div class="rules-mobile">
        <div v-for="r in rules" :key="r.key" class="card rules-card">
          <div class="flex" style="justify-content: space-between; align-items: flex-start; gap: 10px">
            <div style="min-width: 0">
              <div class="mono" style="font-weight: 600; overflow-wrap: anywhere">{{ r.key }}</div>
              <div class="muted" style="font-size: 12px">{{ r.label || '自定义规则' }}</div>
            </div>
            <label class="switch" style="flex-shrink: 0" @click.prevent="toggleEnabled(r)">
              <input type="checkbox" :checked="r.enabled" />
              <span class="track"></span>
            </label>
          </div>
          <div class="rules-field">
            <span class="k">匹配正则</span>
            <code class="v">{{ r.pattern || '(空 = 全部)' }}</code>
          </div>
          <div class="rules-field">
            <span class="k">替换模板</span>
            <code class="v">{{ r.replace || '—' }}</code>
          </div>
          <div class="flex" style="gap: 8px; flex-wrap: wrap; margin-top: 12px">
            <button class="btn btn-sm" @click="openEdit(r)">编辑</button>
            <button v-if="r.overridden" class="btn btn-sm" @click="resetRule(r)">恢复默认</button>
            <button v-if="r.custom" class="btn btn-sm btn-danger btn-ghost" @click="removeRule(r)">删除</button>
            <span v-if="r.builtin && !r.overridden" class="muted" style="font-size: 12px; align-self: center">内置规则</span>
          </div>
        </div>
      </div>
    </template>

    <div class="card card-pad mt-16">
      <div class="card-title">支持的魔法变量</div>
      <div style="display: grid; grid-template-columns: repeat(auto-fill, minmax(min(300px, 100%), 1fr)); gap: 8px; font-size: 13px; color: var(--text-2); line-height: 1.7">
        <div><code class="mono">{TASKNAME}</code> — 任务名称</div>
        <div><code class="mono">{SXX}</code> — 季号（S01）</div>
        <div><code class="mono">{E}</code> — 集数</div>
        <div><code class="mono">{II}</code> — 递增序号（配合 {I+}）</div>
        <div><code class="mono">{EXT}</code> — 扩展名</div>
        <div><code class="mono">{DATE}</code> — 日期</div>
      </div>
      <div class="field-hint mt-8">
        pattern 以 <code class="mono">$</code> 开头时表示引用命名规则；replace 留空 = 按规则默认模板重命名。支持 PCRE 断言语法（自动降级兼容）。
      </div>
    </div>

    <!-- 编辑规则 -->
    <Teleport to="body">
      <div v-if="showEdit" class="modal-overlay" @mousedown.self="showEdit = false">
        <div class="modal">
          <div class="modal-head">
            <div class="modal-title">{{ isBuiltin ? '覆盖内置规则' : '编辑规则' }}</div>
            <button class="modal-close" @click="showEdit = false">✕</button>
          </div>
          <div class="modal-body">
            <div class="form-row">
              <div class="field">
                <div class="field-label">规则标识 <span class="req">*</span></div>
                <input class="input mono" v-model="form.key" :disabled="isBuiltin" placeholder="$MY_RULE" />
                <div class="field-hint">以 $ 开头；与内置同名即为覆盖</div>
              </div>
              <div class="field">
                <div class="field-label">显示名</div>
                <input class="input" v-model="form.label" placeholder="规则用途说明" />
              </div>
            </div>
            <div class="field">
              <div class="field-label">匹配正则（pattern）</div>
              <textarea class="textarea mono" v-model="form.pattern" placeholder="例如：.*?第(\d+)期.*"></textarea>
            </div>
            <div class="field">
              <div class="field-label">替换模板（replace）</div>
              <input class="input mono" v-model="form.replace" placeholder="例如：{TASKNAME}.S01E{E}.{EXT}" />
            </div>

            <div class="card card-pad" style="background: var(--surface-2)">
              <div class="flex mb-8">
                <div style="font-weight: 600; font-size: 13px">试算测试</div>
                <span class="flex-1"></span>
                <button class="btn btn-sm" :disabled="test.testing" @click="runTest">▶ 试算</button>
              </div>
              <input class="input" v-model="test.name" placeholder="输入样例文件名，如：第01集.mp4" @keyup.enter="runTest" />
              <div v-if="test.result" class="test-result mt-8">
                <div class="flex"><span class="muted">命中：</span>
                  <span :class="test.result.matched ? 'text-success' : 'text-danger'">{{ test.result.matched ? '✓ 是' : '✕ 否' }}</span>
                </div>
                <div class="flex"><span class="muted">结果：</span><span class="mono">{{ test.result.result || '（不重命名）' }}</span></div>
              </div>
            </div>

            <label class="checkbox mt-16">
              <input type="checkbox" v-model="form.enabled" />
              启用该规则（禁用后任务中引用它将回退为字面正则）
            </label>
          </div>
          <div class="modal-foot">
            <span class="flex-1"></span>
            <button class="btn" @click="showEdit = false">取消</button>
            <button class="btn btn-primary" :disabled="saving" @click="save">保存</button>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>
