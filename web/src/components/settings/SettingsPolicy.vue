<script setup>
// 转存策略：去重默认值 / 运行记录保留天数 / 账号请求间隔。
//
// 这三个都是「一改就立即生效」的开关型设置，
// 所以每个控件单独 @change 触发保存，没有统一的保存按钮。
defineProps({
  settings: { type: Object, required: true },
  saving: { type: Boolean, default: false },
})
const emit = defineEmits(['save'])
</script>

<template>
  <div class="card card-pad mb-16">
    <div class="card-title">转存策略</div>
    <div class="field" style="max-width: 460px">
      <div class="field-label">跳过已转存历史（默认值）</div>
      <select class="select" :value="settings.skip_history_global"
        @change="emit('save', { skip_history_global: $event.target.value })">
        <option value="on">开启：按转存历史去重（推荐）</option>
        <option value="off">关闭：仅按目标目录现有文件去重</option>
      </select>
      <div class="field-hint">任务里选择「跟随全局」时使用此处取值。</div>
    </div>
    <div class="field" style="max-width: 460px">
      <div class="field-label">运行记录保留天数</div>
      <input class="input" type="number" min="0" :value="settings.run_retention_days"
        @change="emit('save', { run_retention_days: String(parseInt($event.target.value, 10) || 0) })" />
      <div class="field-hint">超期的运行记录每天自动清理（每个任务至少保留最近 50 条）；填 0 = 永久保留。转存历史（去重依据）不受影响。</div>
    </div>
    <div class="field" style="max-width: 460px; margin-bottom: 0">
      <div class="field-label">账号请求间隔（毫秒）</div>
      <input class="input" type="number" min="0" max="60000" step="50"
        :value="settings.request_interval_ms"
        @change="emit('save', { request_interval_ms: Number($event.target.value) || 0 })" />
      <div class="field-hint">
        同一账号的任意两次网盘请求之间至少间隔这么久，是降低账号被风控概率的第一道闸。
        <b>默认 300</b>；填 0 表示不做间隔（不推荐）。调大会显著拉长大批量转存耗时
        （100 个文件 × 1000ms ≈ 多等 100 秒）。
      </div>
    </div>
  </div>
</template>