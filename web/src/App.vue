<script setup>
import { ref, computed, inject, watch } from 'vue'
import { useRoute } from 'vue-router'
import ThemeToggle from './components/ThemeToggle.vue'
import { authApi, setToken } from './api'

const session = inject('session')
const refreshSession = inject('refreshSession')
const toast = inject('toast')
const route = useRoute()

const password = ref('')
const busy = ref(false)
const sidebarOpen = ref(false)

const needLogin = computed(() => session.value.loaded && session.value.auth_required && !session.value.authed)

// 切页自动收起抽屉
watch(() => route.path, () => { sidebarOpen.value = false })

async function doLogin() {
  if (!password.value) return
  busy.value = true
  try {
    const res = await authApi.login(password.value)
    if (res && res.token) setToken(res.token)
    await refreshSession()
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div v-if="needLogin" class="login-wrap">
    <div class="login-card">
      <div class="flex" style="gap: 12px; margin-bottom: 6px">
        <div class="brand-logo" style="width: 42px; height: 42px">
          <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 17l4.5-7 3.5 5 3.5-8 4.5 10"/></svg>
        </div>
        <div>
          <div style="font-size: 18px; font-weight: 700">PanTransfer</div>
          <div style="font-size: 12px; color: var(--text-3)">轻量网盘转存 · 夸克 / 光鸭</div>
        </div>
      </div>
      <p style="color: var(--text-2); font-size: 13px; margin: 10px 0 18px">请输入访问密码登录管理台。</p>
      <form @submit.prevent="doLogin">
        <input class="input" type="password" v-model="password" placeholder="访问密码" autofocus style="height: 40px" />
        <button class="btn btn-primary btn-lg" type="submit" :disabled="busy || !password" style="width: 100%; margin-top: 12px">
          <span v-if="busy" class="spinner" style="border-top-color: #fff"></span>
          <span v-else>登 录</span>
        </button>
      </form>
    </div>
  </div>

  <div v-else-if="session.loaded" class="layout">
    <!-- 移动端顶栏 -->
    <div class="topbar">
      <button class="btn btn-icon btn-ghost" aria-label="打开菜单" @click="sidebarOpen = true">
        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M4 6h16M4 12h16M4 18h16"/></svg>
      </button>
      <div class="brand-mini">
        <div class="brand-logo">
          <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 17l4.5-7 3.5 5 3.5-8 4.5 10"/></svg>
        </div>
        PanTransfer
      </div>
      <span class="flex-1"></span>
      <ThemeToggle compact />
    </div>

    <!-- 抽屉遮罩 -->
    <div v-if="sidebarOpen" class="drawer-backdrop" @click="sidebarOpen = false"></div>

    <aside class="sidebar" :class="{ open: sidebarOpen }">
      <div class="brand">
        <div class="brand-logo">
          <svg width="19" height="19" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 17l4.5-7 3.5 5 3.5-8 4.5 10"/></svg>
        </div>
        <div>
          <div class="brand-name">PanTransfer</div>
          <div class="brand-sub">网盘转存工具</div>
        </div>
      </div>
      <nav class="nav">
        <router-link v-slot="{ href, navigate }" custom to="/">
          <button class="nav-item" :class="{ active: $route.path === '/' }" :href="href" @click="navigate">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 17l4.5-7 3.5 5 3.5-8 4.5 10"/></svg>
            转存任务
          </button>
        </router-link>
        <router-link v-slot="{ href, navigate }" custom to="/logs">
          <button class="nav-item" :class="{ active: $route.path === '/logs' }" :href="href" @click="navigate">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M6 4h9l4 4v12a1 1 0 01-1 1H6a1 1 0 01-1-1V5a1 1 0 011-1z"/><path d="M9 11h6M9 15h6"/></svg>
            运行日志
          </button>
        </router-link>
        <router-link v-slot="{ href, navigate }" custom to="/accounts">
          <button class="nav-item" :class="{ active: $route.path === '/accounts' }" :href="href" @click="navigate">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="8" r="4"/><path d="M4 21c0-4 3.6-6 8-6s8 2 8 6"/></svg>
            网盘账号
          </button>
        </router-link>
        <router-link v-slot="{ href, navigate }" custom to="/rules">
          <button class="nav-item" :class="{ active: $route.path === '/rules' }" :href="href" @click="navigate">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16M4 12h10M4 17h7"/><circle cx="18" cy="16" r="3.2"/><path d="M20.4 18.4L22.5 20.5"/></svg>
            命名规则
          </button>
        </router-link>
        <router-link v-slot="{ href, navigate }" custom to="/settings">
          <button class="nav-item" :class="{ active: $route.path === '/settings' }" :href="href" @click="navigate">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3.2"/><path d="M19 12a7 7 0 00-.14-1.4l2-1.55-2-3.46-2.35.95A7 7 0 0014 5.1L13.65 2.6h-3.3L10 5.1a7 7 0 00-2.5 1.44l-2.36-.95-2 3.46 2 1.55A7 7 0 005 12c0 .47.05.94.14 1.4l-2 1.55 2 3.46 2.35-.95A7 7 0 0010 18.9l.35 2.5h3.3l.35-2.5a7 7 0 002.5-1.44l2.36.95 2-3.46-2-1.55c.09-.46.14-.93.14-1.4z"/></svg>
            设置
          </button>
        </router-link>
      </nav>
      <div class="sidebar-footer">
        <div class="mb-8"><ThemeToggle /></div>
        <div>PanTransfer v{{ session.version }}</div>
        <div style="margin-top: 3px">夸克 / 光鸭 · 本机轻量部署</div>
      </div>
    </aside>

    <main class="main">
      <router-view />
    </main>
  </div>

  <div v-else class="loading-block" style="height: 100vh">
    <span class="spinner spinner-lg"></span>
  </div>

  <!-- 全局 Toast -->
  <div class="toasts">
    <div v-for="t in toasts" :key="t.id" class="toast" :class="'toast-' + t.type">
      <span v-if="t.type === 'success'">✓</span>
      <span v-else-if="t.type === 'error'">✕</span>
      <span v-else>ℹ</span>
      <span class="flex-1">{{ t.msg }}</span>
    </div>
  </div>
</template>
