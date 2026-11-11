// 站点配置：新增 / 编辑弹窗、启停、清空数据，以及配置 JSON 的导入导出。
import { computed } from 'vue'
import { evaluateCron, formatCronTime } from '../cron.js'
import { emptyForm } from './state.js'

export function createSources(ctx) {
  const editing = computed(() => !!ctx.form.id)

  // cron 输入时实时校验：给出合法/非法原因、这条规则的中文含义与接下来几次触发时刻。
  // 纯前端计算（web/src/cron.js），与后端 utils/cron.go 语义一致，输入即反馈、无需请求。
  const cronFeedback = computed(() => {
    if (!ctx.form.scheduleEnabled) return { state: 'off', error: '', meaning: '', next: [] }
    return evaluateCron(ctx.form.scheduleCron, { count: 3 })
  })
  const cronNextText = computed(() => cronFeedback.value.next.map(d => formatCronTime(d)).join('、'))

  function resetForm() { Object.assign(ctx.form, emptyForm()) }

  function sourcePayload(s) {
    return {
      name: s.name, apiBase: s.apiBase, format: s.format || 'seacms_json',
      extraParams: s.extraParams || '', enabled: s.enabled,
      incrementalHours: Number(s.incrementalHours) || 0,
      maxPages: Number(s.maxPages) || 0,
      scheduleEnabled: !!s.scheduleEnabled,
      scheduleCron: (s.scheduleCron || '').trim(),
      parseApi: s.parseApi || '', playDomain: s.playDomain || '',
      picApi: s.picApi || '', picDomain: s.picDomain || '',
    }
  }

  // 站点配置改成弹窗：列表页只留一个「新增站点」入口，编辑时点行内「编辑」打开同一个弹窗。
  function openSourceForm(s) {
    if (s) {
      Object.assign(ctx.form, {
        id: s.id, name: s.name, apiBase: s.apiBase, format: s.format,
        extraParams: s.extraParams, incrementalHours: s.incrementalHours,
        maxPages: s.maxPages, enabled: s.enabled,
        scheduleEnabled: !!s.scheduleEnabled, scheduleCron: s.scheduleCron || '',
        parseApi: s.parseApi || '', playDomain: s.playDomain || '',
        picApi: s.picApi || '', picDomain: s.picDomain || '',
      })
    } else {
      resetForm()
    }
    ctx.error.value = ''
    ctx.sourceOpen.value = true
  }

  function closeSourceForm() {
    ctx.sourceOpen.value = false
    resetForm()
  }

  function editSource(s) { openSourceForm(s) }

  async function saveSource() {
    ctx.error.value = ''
    // 定时开启时前端先拦一道，避免带着明显非法的表达式白跑一次请求。
    if (ctx.form.scheduleEnabled && cronFeedback.value.state === 'invalid') {
      ctx.error.value = `定时配置有误：${cronFeedback.value.error}`
      ctx.notify(ctx.error.value, 'err')
      return
    }
    try {
      const payload = sourcePayload(ctx.form)
      if (ctx.form.id) {
        await ctx.request(`/api/sources/v1/${ctx.form.id}`, ctx.jsonOptions('PUT', payload))
        ctx.notify('站点配置已保存')
      } else {
        await ctx.request('/api/sources/v1?autostart=1', ctx.jsonOptions('POST', payload))
        ctx.notify('站点已添加，已自动发起一次全量采集')
      }
      closeSourceForm()
      await ctx.refresh()
    } catch (e) {
      ctx.error.value = e.message
      ctx.notify(e.message, 'err')
    }
  }

  async function removeSource(s) {
    if (!confirm(`确认删除站点「${s.name}」？该站点的影片数据与采集记录会一并删除。`)) return
    try {
      await ctx.request(`/api/sources/v1/${s.id}`, { method: 'DELETE' })
      ctx.notify('站点已删除')
      await ctx.refresh()
    } catch (e) { ctx.notify(e.message, 'err') }
  }

  // 清空站点数据但保留站点配置：影片、分类、标签、采集记录一并删除，
  // 采集状态与影片数复位，站点接口地址等配置不动。
  async function clearSourceData(s) {
    if (!confirm(`确认清空站点「${s.name}」的数据？\n\n将删除该站点的全部影片、分类、标签与采集记录，并复位采集状态；站点配置（接口地址、解析、启用状态等）保留。\n\n此操作不可撤销。`)) return
    try {
      const r = await ctx.request(`/api/sources/v1/${s.id}/data`, { method: 'DELETE' })
      ctx.notify(`已清空「${s.name}」：影片 ${r.videos}、分类 ${r.categories}、标签 ${r.tags}、采集记录 ${r.jobs}`)
      await ctx.refresh()
    } catch (e) { ctx.notify(e.message, 'err') }
  }

  async function toggleSource(s) {
    try {
      await ctx.request(`/api/sources/v1/${s.id}`,
        ctx.jsonOptions('PUT', { ...sourcePayload(s), enabled: !s.enabled }))
      await ctx.refresh()
    } catch (e) { ctx.notify(e.message, 'err') }
  }

  // ---------- 站点配置 JSON 导入 / 导出 ----------
  const importPreviewShown = computed(() => ctx.importPreview.value.slice(0, 30))

  async function exportSources() {
    try {
      const data = await ctx.request('/api/sources/v1/export')
      const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json;charset=utf-8' })
      const url = URL.createObjectURL(blob)
      const d = new Date()
      const pad = n => String(n).padStart(2, '0')
      const stamp = `${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}-${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}`
      const a = document.createElement('a')
      a.href = url
      a.download = `assetspider-sources-${stamp}.json`
      document.body.appendChild(a)
      a.click()
      a.remove()
      setTimeout(() => URL.revokeObjectURL(url), 1000)
      ctx.notify(`已导出 ${data.sources?.length || 0} 个站点配置`)
    } catch (e) { ctx.notify(e.message, 'err') }
  }

  function pickImportFile() {
    if (!ctx.importFileEl.value) return
    ctx.importFileEl.value.value = '' // 允许连续选同一个文件
    ctx.importFileEl.value.click()
  }

  async function onImportFile(e) {
    const file = e.target.files?.[0]
    if (!file) return
    try {
      const text = await file.text()
      const data = JSON.parse(text)
      let items
      if (Array.isArray(data)) items = data
      else if (data && Array.isArray(data.sources)) items = data.sources
      else if (data && typeof data === 'object') items = [data]
      else throw new Error('内容不是站点配置对象或数组')
      if (!items.length) throw new Error('文件里没有站点配置')
      ctx.importRaw.value = text
      ctx.importFileName.value = file.name
      ctx.importPreview.value = items.map(s => ({
        name: typeof s?.name === 'string' ? s.name : '',
        apiBase: typeof s?.apiBase === 'string' ? s.apiBase : '',
      }))
      ctx.importOpen.value = true
    } catch (err) {
      ctx.notify(`无法读取 JSON：${err.message}`, 'err')
    }
  }

  function closeImport() {
    ctx.importOpen.value = false
    ctx.importRaw.value = ''
    ctx.importFileName.value = ''
    ctx.importPreview.value = []
  }

  async function confirmImport() {
    if (ctx.importBusy.value) return
    ctx.importBusy.value = true
    try {
      const res = await ctx.request('/api/sources/v1/import', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: ctx.importRaw.value,
      })
      const issues = (res.issues || []).slice(0, 3)
        .map(i => `第 ${i.index + 1} 条「${i.name || '未命名'}」：${i.error}`).join('；')
      const tail = res.failed ? `、失败 ${res.failed} 个${issues ? `（${issues}）` : ''}` : ''
      ctx.notify(`导入完成：新增 ${res.created} 个、更新 ${res.updated} 个${tail}`, res.failed ? 'err' : 'ok')
      closeImport()
      await ctx.refresh()
    } catch (e) {
      ctx.notify(e.message, 'err')
    } finally {
      ctx.importBusy.value = false
    }
  }

  return {
    editing, cronFeedback, cronNextText, resetForm, sourcePayload,
    openSourceForm, closeSourceForm, editSource, saveSource, removeSource,
    clearSourceData, toggleSource, importPreviewShown, exportSources, pickImportFile,
    onImportFile, closeImport, confirmImport,
  }
}
