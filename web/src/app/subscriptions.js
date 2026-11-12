// 站点配置订阅：远程 JSON 地址 + 定时 / 手动刷新。
//
// 订阅只负责「配置分发」：刷新时把远端列出的站点写进站点表（新增 / 更新 /
// 停用），不触发采集。是否采集仍由站点自身的启用状态与定时 cron 决定，
// 这样一次订阅刷新不会同时起几十个采集任务。
import { computed } from 'vue'
import { emptySubForm } from './state.js'

export function createSubscriptions(ctx) {
  const subEditing = computed(() => !!ctx.subForm.id)

  // 站点 → 订阅名称：站点表只存 subscriptionId，列表上要显示订阅名。
  const subNameOf = computed(() => {
    const map = new Map((ctx.subscriptions.value || []).map(s => [s.id, s.name]))
    return id => (id ? map.get(id) || `#${id}` : '')
  })

  // 订阅刷新状态：never 在订阅语境下是「未刷新」，与采集的「未采集」区分开。
  function subStatusText(status) {
    if (status === 'never' || !status) return '未刷新'
    return { success: '成功', failed: '失败' }[status] || status
  }

  function resetSubForm() { Object.assign(ctx.subForm, emptySubForm()) }

  function openSubForm(s) {
    if (s) {
      Object.assign(ctx.subForm, {
        id: s.id, name: s.name, url: s.url,
        intervalHours: s.intervalHours, enabled: !!s.enabled,
      })
    } else {
      resetSubForm()
    }
    ctx.subOpen.value = true
  }

  function closeSubForm() {
    ctx.subOpen.value = false
    resetSubForm()
  }

  function subPayload() {
    return {
      name: (ctx.subForm.name || '').trim(),
      url: (ctx.subForm.url || '').trim(),
      intervalHours: Number(ctx.subForm.intervalHours) || 0,
      enabled: !!ctx.subForm.enabled,
    }
  }

  async function saveSubscription() {
    if (ctx.subBusy.value) return
    ctx.subBusy.value = true
    try {
      if (ctx.subForm.id) {
        await ctx.request(`/api/subscriptions/v1/${ctx.subForm.id}`, ctx.jsonOptions('PUT', subPayload()))
        ctx.notify('订阅已保存')
      } else {
        await ctx.request('/api/subscriptions/v1', ctx.jsonOptions('POST', subPayload()))
        ctx.notify('订阅已添加，点「刷新」同步站点配置')
      }
      closeSubForm()
      await ctx.refresh()
    } catch (e) {
      ctx.notify(e.message, 'err')
    } finally {
      ctx.subBusy.value = false
    }
  }

  // 一次刷新的结果：新增 / 更新 / 停用 / 跳过，跳过时附上前几条原因。
  function subResultText(r) {
    const parts = [`共 ${r.total} 条`]
    for (const [n, text] of [[r.created, '新增'], [r.updated, '更新'], [r.disabled, '停用'], [r.skipped, '跳过']]) {
      if (n) parts.push(`${text} ${n}`)
    }
    const issues = (r.issues || []).slice(0, 3)
      .map(i => `第 ${i.index + 1} 条「${i.name || '未命名'}」：${i.error}`).join('；')
    return parts.join('，') + (issues ? `（${issues}）` : '')
  }

  async function refreshSubscription(s) {
    if (ctx.subRefreshing.value) return
    ctx.subRefreshing.value = s.id
    try {
      const r = await ctx.request(`/api/subscriptions/v1/${s.id}/refreshes`, { method: 'POST' })
      ctx.notify(`「${s.name}」刷新完成：${subResultText(r)}`, r.skipped ? 'err' : 'ok')
    } catch (e) {
      // 失败原因由后端给出人话版本，直接展示；同时刷新列表把 lastError 带出来。
      ctx.notify(e.message, 'err')
    } finally {
      ctx.subRefreshing.value = null
      await ctx.refresh()
    }
  }

  async function toggleSubscription(s) {
    try {
      await ctx.request(`/api/subscriptions/v1/${s.id}`, ctx.jsonOptions('PUT', {
        name: s.name, url: s.url, intervalHours: s.intervalHours, enabled: !s.enabled,
      }))
      await ctx.refresh()
    } catch (e) { ctx.notify(e.message, 'err') }
  }

  async function removeSubscription(s) {
    if (!confirm(`确认删除订阅「${s.name}」？\n\n已由该订阅同步的站点会保留（影片与采集记录不受影响），只是不再随订阅刷新。`)) return
    try {
      await ctx.request(`/api/subscriptions/v1/${s.id}`, { method: 'DELETE' })
      ctx.notify('订阅已删除')
      await ctx.refresh()
    } catch (e) { ctx.notify(e.message, 'err') }
  }

  // 间隔文本：0 表示只能手动刷新。
  function subIntervalText(s) {
    return s.intervalHours > 0 ? `每 ${s.intervalHours} 小时` : '仅手动'
  }

  return {
    subEditing, subNameOf, subStatusText, subIntervalText,
    openSubForm, closeSubForm, saveSubscription, refreshSubscription,
    toggleSubscription, removeSubscription,
  }
}
