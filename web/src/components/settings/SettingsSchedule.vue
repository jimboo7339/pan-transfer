<script setup>
// 调度配置：全局开关 + cron 表达式 + 旧版（运行时间 + 更新日）。
//
// 与别的区块不同，这里有一个「保存调度配置」按钮：
// 早先开关一改就立即落库，而下面的 cron 要按按钮才落库。
// 用户关掉开关再打开，会发现 cron 还是旧值（从未保存过），以为配置丢了。
// 现在整张卡片统一走那一个按钮，一次操作一个心智模型。
import { ref, watch } from 'vue'
import WeekPicker from '../WeekPicker.vue'
import { cronApi } from '../../api'
import { CRON_PRESETS, cronToText } from '../../cron'

const props = defineProps({
  settings: { type: Object, required: true },
  saving: { type: Boolean, default: false },
})
const emit = defineEmits(['save'])

const cronNext = ref('')
const cronError = ref('')

// 全局 cron 校验：防抖 300ms + 请求序号守卫。
//
// 与 TaskEdit 的 cron 校验同一套理由：
//   - 无防抖时每敲一个字符发一次请求，`0 */2 * * *` 会发 11 个，
//     在 SQLite 单连接下排队拖慢其它 API；
//   - 无序号守卫时先发的请求后返回会覆盖新结果，
//     用户看到的"下次执行时间"是上一次输入算出来的。
let cronTimer = null
let cronSeq = 0

watch(() => props.settings.schedule_cron, (v) => {
  cronNext.value = ''; cronError.value = ''
  if (cronTimer) { clearTimeout(cronTimer); cronTimer = null }
  if (!v || !v.trim()) return
  const expr = v.trim()
  if (expr.split(/\s+/).length !== 5) {
    cronError.value = '需要 5 个字段：分 时 日 月 周'
    return
  }

  const seq = ++cronSeq
  cronTimer = setTimeout(() => {
    cronApi.next(expr)
      .then(r => { if (seq === cronSeq) cronNext.value = r.next_run })
      .catch(e => { if (seq === cronSeq) cronError.value = e.message })
  }, 300)
})

// toggleEnabled 切换全局调度开关。
//
// 用 :checked + @change 而不是 v-model：父组件传进来的是 reactive 对象，
// 子组件直接改它的字段虽然能工作，但会让"谁改了状态"变得不可追踪。
// 这里统一 emit('save', patch)，由容器落库后再回填。
function toggleEnabled(e) {
  emit('save', { schedule_enabled: e.target.checked ? 'on' : 'off' })
}

function saveSchedule() {
  // 用可选链/默认值：后端若返回 null（字段缺失）会直接抛 TypeError，
  // 而这里没有 try-catch，点"保存调度配置"就崩。
  const cron = (props.settings.schedule_cron || '').trim()
  const time = props.settings.schedule_time || ''
  if (props.settings.schedule_enabled === 'on' && !cron && !time) {
    // 提示交给容器统一做（toast 是全局注入的），这里只阻断保存。
    emit('invalid', '全局调度开启时需要填写 cron 表达式或运行时间')
    return
  }
  emit('save', {
    schedule_enabled: props.settings.schedule_enabled,
    schedule_cron: cron,
    schedule_time: time,
    schedule_week: props.settings.schedule_week || '',
  })
}

defineExpose({ cronNext, cronError })
</script>

<template>
  <div class="card card-pad mb-16">
    <div class="card-title">调度配置</div>
    <div class="page-desc" style="margin-top: -6px; margin-bottom: 14px">
      任务里调度模式选「跟随全局」时使用此处配置；任务自身选「自定义定时」的按任务配置执行。
    </div>
    <div class="field" style="max-width: 420px">
      <div class="field-label">全局定时调度</div>
      <div class="flex">
        <label class="switch">
          <input type="checkbox" :checked="settings.schedule_enabled !== 'off'"
            @change="toggleEnabled" />
          <span class="track"></span>
        </label>
        <span class="page-desc" style="margin: 0">{{ settings.schedule_enabled === 'off' ? '已关闭：所有任务仅手动运行' : '开启中：到点自动运行「跟随全局」的任务' }}</span>
      </div>
      <!-- 开关不再立即落库：早先开关一改就保存，而下面的 cron 要按
            「保存调度配置」才落库。用户关掉开关再打开，会发现 cron
            还是旧值（从未保存过），以为配置丢了。
            现在整张卡片统一走底部那一个保存按钮。 -->
    </div>
    <div class="field" style="max-width: 560px">
      <div class="field-label">Cron 表达式（分 时 日 月 周）</div>
      <input class="input mono" v-model="settings.schedule_cron"
        placeholder="例如 0 */2 * * *  = 每 2 小时"
        :disabled="settings.schedule_enabled === 'off'" />
      <div class="flex mt-8" style="flex-wrap: wrap; gap: 6px">
        <button v-for="p in CRON_PRESETS" :key="p.value" class="btn btn-sm"
          :disabled="settings.schedule_enabled === 'off'"
          @click="settings.schedule_cron = p.value">{{ p.label }}</button>
      </div>
      <div v-if="cronError" class="field-hint text-danger" style="margin-top: 6px">{{ cronError }}</div>
      <div v-else-if="cronNext" class="field-hint" style="margin-top: 6px">
        下次执行：{{ cronNext }} · {{ cronToText(settings.schedule_cron) }}
      </div>
      <div v-else-if="settings.schedule_cron" class="field-hint text-warn" style="margin-top: 6px">
        表达式格式有误，暂无法计算下次执行时间。
      </div>
      <div class="field-hint" style="margin-top: 6px">
        示例：每 2 小时 <code class="mono">0 */2 * * *</code>；每天 08:00 <code class="mono">0 8 * * *</code>；周一到五 20:30 <code class="mono">30 20 * * 1-5</code>
      </div>
    </div>
    <details class="mt-8" style="max-width: 560px">
      <summary style="cursor: pointer; font-size: 12.5px; color: var(--text-3); user-select: none">
        旧版配置（运行时间 + 更新日，兼容保留）
      </summary>
      <div class="form-row mt-8">
        <div class="field">
          <div class="field-label">运行时间</div>
          <input class="input" type="time" v-model="settings.schedule_time"
            :disabled="settings.schedule_enabled === 'off'" />
        </div>
        <div class="field">
          <div class="field-label">更新日（不选 = 每天）</div>
          <WeekPicker v-model="settings.schedule_week" />
        </div>
      </div>
      <div class="field-hint">填了「Cron 表达式」时以它为准；否则回退到这里的运行时间 + 更新日。Cron 优先。</div>
    </details>
    <button class="btn btn-primary" :disabled="saving" @click="saveSchedule">保存调度配置</button>
    <div class="field-hint mt-8">到点后有 1–3 分钟随机延迟；截止日期在任务里单独配置。</div>
  </div>
</template>