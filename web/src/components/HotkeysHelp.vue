<script setup>
// 快捷键帮助面板（按 ? 打开）。
//
// 为什么需要：快捷键如果不被发现就等于不存在。用户不会去读 README 里的
// 快捷键列表，但会在界面上看到一个"?"按钮或提示。
//
// 这里同时承担"教学"职责：每一行除了键位，还说明它做什么、
// 以及 g 前缀序列的用法（这是最容易被误解的——用户按了 g 之后
// 不知道还要再按一个键）。
import { computed } from 'vue'
import { HOTKEYS as HOTKEY_LIST } from '../hotkeys'
import Modal from './Modal.vue'

// 必须显式声明为 setup 内的常量，模板才能访问到。
// （直接 import 的绑定在某些构建组合下不会暴露给模板编译器。）
const HOTKEYS = HOTKEY_LIST

const props = defineProps({ modelValue: Boolean })
const emit = defineEmits(['update:modelValue'])

// 不能直接 v-model="modelValue"：那是在给 prop 赋值。
// 双向绑定的正确做法是计算属性代理到 emit。
const open = computed({
  get: () => !!props.modelValue,
  set: (v) => emit('update:modelValue', v),
})

function close() {
  emit('update:modelValue', false)
}
</script>

<template>
  <Modal v-model="open" title="键盘快捷键">
    <div class="hotkeys-list">
      <div v-for="(h, i) in HOTKEYS" :key="i" class="hotkeys-row">
        <div class="hotkeys-keys">
          <kbd v-for="(k, j) in h.keys" :key="j" class="hotkeys-kbd">{{ k }}</kbd>
        </div>
        <div class="hotkeys-desc">{{ h.desc }}</div>
      </div>
    </div>
    <div class="hotkeys-note">
      <b>提示：</b>带 <code class="mono">g</code> 前缀的是两步序列——
      先按 <kbd class="hotkeys-kbd-inline">g</kbd>，再按对应字母（1.2 秒内）。
      输入框内不会触发任何快捷键，避免打字时误触。
    </div>
    <div class="hotkeys-note hotkeys-note-warn">
      <b>已在当前页时按 <kbd class="hotkeys-kbd-inline">g</kbd> + 对应字母</b>
      不会报错也不会跳转（目标就是当前页），只在左下角提示一句。
      仪表盘是默认页，所以 <kbd class="hotkeys-kbd-inline">g</kbd> <kbd class="hotkeys-kbd-inline">d</kbd>
      在刚打开面板时看起来"没反应"——那是正常的。
    </div>
    <template #footer>
      <button class="btn btn-primary" @click="close">知道了</button>
    </template>
  </Modal>
</template>

<style scoped>
.hotkeys-list { display: flex; flex-direction: column; gap: 2px; }
.hotkeys-row {
  display: flex; align-items: center; gap: 12px;
  padding: 7px 0; border-bottom: 1px solid var(--border);
}
.hotkeys-row:last-child { border-bottom: none; }
.hotkeys-keys { display: flex; gap: 4px; flex-shrink: 0; width: 110px; }
.hotkeys-kbd {
  font-family: var(--mono); font-size: 11.5px; color: var(--text-2);
  background: var(--surface-2); border: 1px solid var(--border);
  border-bottom-width: 2px; border-radius: 5px; padding: 2px 7px;
  min-width: 24px; text-align: center;
}
.hotkeys-kbd-inline {
  font-family: var(--mono); font-size: 11px; color: var(--text-2);
  background: var(--surface-2); border: 1px solid var(--border);
  border-bottom-width: 2px; border-radius: 4px; padding: 1px 5px;
}
.hotkeys-desc { font-size: 13px; color: var(--text-2); }
.hotkeys-note {
  margin-top: 14px; padding: 10px 12px; border-radius: 8px;
  background: var(--surface-2); font-size: 12.5px; color: var(--text-2);
  line-height: 1.7;
}
/* 补充说明：比主提示更醒目一点，但不用警告色（这不是错误，是预期行为） */
.hotkeys-note-warn {
  margin-top: 8px;
  background: var(--primary-soft);
  color: var(--text-2);
}
</style>