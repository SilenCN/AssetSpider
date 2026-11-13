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

  async function testDatabase() {
    if (ctx.dbTesting.value) return
    ctx.dbTesting.value = true
    ctx.dbTestResult.value = null
    try {
      // 与保存共用同一份数据库载荷；DSN 留空时后端沿用现有连接串，因此也可实测。
      const payload = { database: buildDatabasePayload(ctx.dbForm.value) }
      ctx.dbTestResult.value = await ctx.request('/api/settings/v1/database/test', ctx.jsonOptions('POST', payload))
    } catch (e) {
      ctx.dbTestResult.value = { ok: false, error: e.message }
    } finally {
      ctx.dbTesting.value = false
    }
  }

  // 重启期间服务会断开：直接轮询健康检查（不走 request，避免用 401 逻辑清登录态）。
  async function waitForHealthy(tries = 40, interval = 500) {
    for (let i = 0; i < tries; i += 1) {
      await new Promise(resolve => setTimeout(resolve, interval))
      try {
        const res = await fetch('/api/system/v1/health', { cache: 'no-store' })
        if (res.ok) return true
      } catch (e) {
        // 进程还没起来，连接被拒绝属正常，继续等下一轮。
      }
    }
    return false
  }

  async function restartService() {
    if (ctx.restarting.value) return
    if (!confirm('确认重启服务？\n\n服务会先停止接收新请求、等待运行中的采集任务收尾，随后自动重启；期间页面会短暂无法访问。')) return
    ctx.restarting.value = true
    try {
      const res = await ctx.request('/api/settings/v1/restart', ctx.jsonOptions('POST', {}))
      if (!res || !res.ok) throw new Error(res?.error || '重启请求被拒绝')
      ctx.notify(res.message || '服务正在重启…')
      // 重启生效后重新拉一次配置：数据库连接目标、启动项等只读值都会更新。
      if (await waitForHealthy()) {
        await loadSettings()
        ctx.notify('服务已重启完成')
      } else {
        ctx.notify('等待服务恢复超时，请稍后手动刷新页面', 'err')
      }
    } catch (e) {
      ctx.error.value = e.message
      ctx.notify(e.message, 'err')
    } finally {
      ctx.restarting.value = false
    }
  }

  return { loadSettings, saveSettings, testDatabase, restartService }
}
