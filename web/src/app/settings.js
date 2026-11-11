// 系统设置：采集运行时配置的读取与保存（保存后即时生效并写回配置文件）。
import { buildPayload } from '../settings.js'

export function createSettings(ctx) {
  async function loadSettings() {
    try {
      ctx.settings.value = (await ctx.request('/api/settings/v1')) || null
      // 表单只放可编辑的 collector 字段；只读启动项与字段元信息留在 settings 里展示
      ctx.settingsForm.value = { ...(ctx.settings.value?.collector || {}) }
      ctx.settingsDirty.value = false
    } catch (e) { ctx.error.value = e.message }
  }

  async function saveSettings() {
    if (ctx.settingsSaving.value || !ctx.settingsDirty.value) return
    ctx.settingsSaving.value = true
    try {
      // 只提交可编辑且未被环境变量锁定的字段；其余保持后端原值
      const payload = buildPayload(ctx.settings.value.fields, ctx.settingsForm.value)
      ctx.settings.value = await ctx.request('/api/settings/v1', ctx.jsonOptions('PUT', payload))
      ctx.settingsForm.value = { ...(ctx.settings.value.collector || {}) }
      ctx.settingsDirty.value = false
      // 并发上限可能变了，刷新容量让任务页/概览卡的只读展示同步
      const c = await ctx.request('/api/tasks/v1/capacity')
      if (c) ctx.capacity.value = c
      ctx.notify('系统设置已保存并即时生效')
    } catch (e) {
      ctx.error.value = e.message
      ctx.notify(e.message, 'err')
    } finally {
      ctx.settingsSaving.value = false
    }
  }

  return { loadSettings, saveSettings }
}
