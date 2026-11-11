// 用户管理（管理员）：列表、新增 / 编辑弹窗、启停与删除。
import { computed } from 'vue'
import { emptyUserForm } from './state.js'

export function createUsers(ctx) {
  const userEditing = computed(() => !!ctx.userForm.id)

  async function loadUsers() {
    if (!ctx.isAdmin.value) { ctx.users.value = []; return }
    ctx.users.value = (await ctx.request('/api/users/v1')) || []
  }

  function resetUserForm() { Object.assign(ctx.userForm, emptyUserForm()) }

  function openUserForm(u = null) {
    resetUserForm()
    if (u) {
      Object.assign(ctx.userForm, {
        id: u.id, username: u.username, password: '', role: u.role,
        enabled: u.enabled, sourceIds: [...(u.sourceIds || [])],
      })
    }
    ctx.userFormOpen.value = true
  }

  function closeUserForm() {
    ctx.userFormOpen.value = false
    resetUserForm()
  }

  function editUser(u) { openUserForm(u) }

  async function saveUser() {
    ctx.error.value = ''
    try {
      const payload = {
        role: ctx.userForm.role, enabled: ctx.userForm.enabled,
        sourceIds: [...ctx.userForm.sourceIds],
      }
      if (ctx.userForm.id) {
        if (ctx.userForm.password) payload.password = ctx.userForm.password
        await ctx.request(`/api/users/v1/${ctx.userForm.id}`, ctx.jsonOptions('PUT', payload))
      } else {
        payload.username = ctx.userForm.username.trim()
        payload.password = ctx.userForm.password
        await ctx.request('/api/users/v1', ctx.jsonOptions('POST', payload))
      }
      closeUserForm() // 保存成功才关闭；失败时保留弹窗内容便于修改
      ctx.notify('用户信息已保存')
      await ctx.refresh()
    } catch (e) {
      ctx.error.value = e.message
      ctx.notify(e.message, 'err')
    }
  }

  async function toggleUser(u) {
    try {
      await ctx.request(`/api/users/v1/${u.id}`, ctx.jsonOptions('PUT', { enabled: !u.enabled }))
      ctx.notify(u.enabled ? '账号已停用' : '账号已启用')
      await ctx.refresh()
    } catch (e) { ctx.notify(e.message, 'err') }
  }

  async function removeUser(u) {
    if (!confirm(`确认删除用户「${u.username}」？该用户的登录会话与站点授权会一并清除。`)) return
    try {
      await ctx.request(`/api/users/v1/${u.id}`, { method: 'DELETE' })
      ctx.notify('用户已删除')
      if (ctx.userForm.id === u.id) closeUserForm()
      await ctx.refresh()
    } catch (e) { ctx.notify(e.message, 'err') }
  }

  function visibleLabel(u) {
    if (u.role === 'admin') return '全部站点（管理员）'
    const ids = u.sourceIds || []
    if (!ids.length) return '未授权任何站点'
    return ids.map(id => ctx.sources.value.find(s => s.id === id)?.name || `#${id}`).join('、')
  }

  return {
    userEditing, loadUsers, resetUserForm, openUserForm, closeUserForm, editUser,
    saveUser, toggleUser, removeUser, visibleLabel,
  }
}
