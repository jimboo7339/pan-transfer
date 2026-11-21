<script setup>
// 任务操作按钮组。
//
// 为什么要抽出来：卡片视图和表格视图此前各写一套操作按钮，
// 卡片 5 个按钮同权重排一行、表格用 ⋯ 菜单收纳次操作。
// 结果同一个任务在两个视图里能做的操作不一样（表格有「任务详情」菜单项，
// 卡片没有；卡片的删除在表格里藏在菜单深处），
// 而且加一个新操作要改两处、容易漏。
//
// 现在统一为：主操作（运行 / 编辑）直接显示，次操作收进 ⋯ 菜单。
// 两个视图都用这个组件，行为由 construction 保证一致。
import DropdownMenu from './DropdownMenu.vue'

defineProps({
  task: { type: Object, required: true },
  running: { type: Boolean, default: false },
})

const emit = defineEmits(['run', 'edit', 'runs', 'toggle', 'remove', 'detail'])

// 菜单项按「常用 → 危险」排序，危险项放最后。
// 删除用 danger 样式，避免误点。
function menuItems(t) {
  return [
    { key: 'detail', label: '任务详情', icon: '🔗' },
    { key: 'runs', label: '运行记录', icon: '📄' },
    {
      key: 'toggle',
      label: t.status === 'paused' ? '启用' : '暂停',
      icon: t.status === 'paused' ? '⏵' : '⏸',
    },
    { key: 'remove', label: '删除任务', icon: '🗑', danger: true },
  ]
}

function onSelect(key) {
  // DropdownMenu 的 @select 只给 key，任务对象由父组件闭包持有。
  // 这里把 key 原样上抛，父组件用 `v-on="{...}"` 或具名监听处理。
  emit(key)
}
</script>

<template>
  <div class="task-actions">
    <button class="btn btn-sm btn-primary" :disabled="running" @click="emit('run')">
      <span v-if="running" class="spinner" style="border-top-color: #fff"></span>
      <span v-else>▶ 运行</span>
    </button>
    <button class="btn btn-sm" @click="emit('edit')">编辑</button>
    <DropdownMenu :items="menuItems(task)" :label="`「${task.task_name}」的更多操作`"
      @select="onSelect" />
  </div>
</template>

<style scoped>
.task-actions {
  display: flex; align-items: center; gap: 6px; flex-wrap: wrap;
}
</style>