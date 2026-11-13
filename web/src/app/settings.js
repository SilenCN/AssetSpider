// 系统设置：采集运行时配置的读取与保存（保存后即时生效并写回配置文件）。
import { buildPayload, buildDatabasePayload } from '../settings.js'

function dbFormFromView(view) {
  const db = view?.database
  if (!db) return { driver: 'sqlite', path: '', dsn: '', maxOpenConns: 16, maxIdleConns: 8, connMaxLifetimeMin: 30 }
  // DSN 不回显明文：表单里始终留空，表示「保留现有连接串」；用户需改时自行填写。
  return {
    driver: db.driver || 'sqlite',
    path: db.path || '',
    dsn: '',
    maxOpenConns: db.maxOpenConns ?? 16,
    maxIdleConns: db.maxIdleConns ?? 8,
    connMaxLifetimeMin: db.connMaxLifetimeMin ?? 30,
  }
}

export function createSettings(ctx) {
  async function loadSettings() {
    try {
      ctx.settings.value = (await ctx.request('/api/settings/v1')) || null
      // 表单只放可编辑的 collector 字段；只读启动项与字段元信息留在 settings 里展示
      ctx.settingsForm.value = { ...(ctx.settings.value?.collector || {}) }
      ctx.dbForm.value = dbFormFromView(ctx.settings.value)
      ctx.settingsDirty.value = false
    } catch (e) { ctx.error.value = e.message }
  }

  async function saveSettings() {
    if (ctx.settingsSaving.value || !ctx.settingsDirty.value) return
    ctx.settingsSaving.value = true
    try {
      // 采集配置只提交可编辑且未被环境变量锁定的字段；数据库配置一并提交。
      const payload = {
        collector: buildPayload(ctx.settings.value.fields, ctx.settingsForm.value),
        database: buildDatabasePayload(ctx.dbForm.value),
      }
      ctx.settings.value = await ctx.request('/api/settings/v1', ctx.jsonOptions('PUT', payload))
      ctx.settingsForm.value = { ...(ctx.settings.value.collector || {}) }
      ctx.dbForm.value = dbFormFromView(ctx.settings.value)
      ctx.settingsDirty.value = false
      // 并发上限可能变了，刷新容量让任务页/概览卡的只读展示同步
      const c = await ctx.request('/api/tasks/v1/capacity')
      if (c) ctx.capacity.value = c
      ctx.notify('系统设置已保存并写入配置文件，数据库配置需重启服务后生效')
    } catch (e) {
      ctx.error.value = e.message
      ctx.notify(e.message, 'err')
    } finally {
      ctx.settingsSaving.value = false
    }
  }

  return { loadSettings, saveSettings }
}
