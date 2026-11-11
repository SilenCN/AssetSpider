// 登录 / 会话：初始化探测、登录、退出、修改密码。
import { DEFAULT_TAB } from '../layout.js'

export function createAuth(ctx) {
  async function doLogin() {
    ctx.loginError.value = ''
    ctx.loginBusy.value = true
    try {
      const d = await ctx.request('/api/auth/v1/sessions', ctx.jsonOptions('POST', {
        username: ctx.loginForm.username.trim(), password: ctx.loginForm.password,
      }))
      ctx.setAuth(d.token)
      ctx.me.value = { user: d.user, isAdmin: d.isAdmin, visibleSourceIds: [] }
      ctx.initialized.value = true
      ctx.loginForm.password = ''
      ctx.notify(d.firstUser ? '系统已初始化，你的账号自动注册为管理员' : `欢迎回来，${d.user.username}`)
      ctx.loadFieldMeta().catch(() => {})
      await ctx.refresh()
    } catch (e) {
      ctx.loginError.value = e.message
    } finally {
      ctx.loginBusy.value = false
    }
  }

  async function doLogout() {
    try { await ctx.request('/api/auth/v1/sessions', { method: 'DELETE' }) } catch (e) { /* 忽略 */ }
    ctx.clearAuth()
    ctx.tab.value = DEFAULT_TAB
    ctx.notify('已退出登录', 'info')
  }

  async function doChangePassword() {
    try {
      const d = await ctx.request('/api/auth/v1/password', ctx.jsonOptions('PUT', { ...ctx.pwdForm }))
      ctx.setAuth(d.token)
      ctx.pwdForm.oldPassword = ''
      ctx.pwdForm.newPassword = ''
      ctx.pwdModal.value = false
      ctx.notify(d.message || '密码已修改')
    } catch (e) { ctx.notify(e.message, 'err') }
  }

  async function bootstrap() {
    ctx.authReady.value = false
    try {
      const st = await ctx.request('/api/auth/v1/status')
      ctx.initialized.value = st.initialized
      if (ctx.token.value) {
        try { ctx.me.value = await ctx.request('/api/auth/v1/me') } catch (e) { ctx.clearAuth() }
      }
    } catch (e) {
      ctx.error.value = e.message
    } finally {
      ctx.authReady.value = true
    }
    if (ctx.me.value) {
      ctx.loadFieldMeta().catch(() => {})
      await ctx.refresh()
    }
  }

  return { doLogin, doLogout, doChangePassword, bootstrap }
}
