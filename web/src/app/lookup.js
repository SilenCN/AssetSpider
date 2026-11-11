// 展示层的小工具：纯格式化 / 查表函数，不含请求与状态变更。
export function createLookup(ctx) {
  function siteNameOf(id) {
    return ctx.sources.value.find(s => s.id === id)?.name || `站点 #${id}`
  }

  function categoryName(id) {
    return ctx.categories.value.find(c => c.id === id)?.name || '-'
  }

  function formatTime(value) {
    if (!value) return '-'
    const d = new Date(value)
    return isNaN(d.getTime()) ? String(value) : d.toLocaleString('zh-CN', { hour12: false })
  }
  function modeLabel(m) { return m === 'full' ? '全量' : '增量' }
  function statusLabel(s) {
    return {
      success: '成功', failed: '失败', running: '进行中', queued: '排队中',
      paused: '已暂停', never: '未采集',
    }[s] || s || '-'
  }
  function short(text, n = 46) {
    const s = String(text || '')
    return s.length > n ? s.slice(0, n) + '…' : s
  }

  // 采集记录：任务运行中以内存里的实时视图为准（拉取/新增/更新/未变随分页累加），
  // 结束后回落到落库的终态统计。
  function stat(j, key) {
    if (j.live && typeof j.live[key] === 'number') return j.live[key]
    return j[key] ?? 0
  }
  function jobMessage(j) {
    return (j.live && j.live.message) || j.message || '-'
  }

  // ---------- 进度展示 ----------
  // percent 为 -1 表示总量未知，进度条走不定态动画。
  function barWidth(t) {
    const p = t?.percent
    if (p === undefined || p === null || p < 0) return '100%'
    return Math.max(2, Math.min(100, p)) + '%'
  }
  function isIndeterminate(t) {
    return t?.percent === undefined || t?.percent === null || t.percent < 0
  }
  function fmtEta(sec) {
    if (!sec || sec <= 0) return ''
    if (sec < 60) return `${sec} 秒`
    return `${Math.floor(sec / 60)} 分 ${sec % 60} 秒`
  }
  function taskStatusText(t) {
    if (t.retryInSeconds > 0) return `等待重试 ${t.retryInSeconds}s`
    return statusLabel(t.status)
  }

  // ---------- 资源卡片展示 ----------
  // 图片加载失败时记到该条记录上，退化为占位块，避免整页破图。
  function posterOf(v) {
    if (!v || v._picFailed) return ''
    return v.vodPic || v.vodPicThumb || v.vodPicSlide || ''
  }
  function markPicFailed(v) { if (v) v._picFailed = true }
  function episodeLabel(v) {
    if (!v) return '-'
    if (v.vodRemarks) return v.vodRemarks
    if (v.vodSerial) return `第 ${v.vodSerial} 集`
    if (v.vodTotal) return `全 ${v.vodTotal} 集`
    return '-'
  }
  function scoreOf(v) {
    const n = Number(String(v?.vodScore ?? '').trim())
    return Number.isFinite(n) && n > 0 ? n.toFixed(1) : ''
  }
  function metaLine(v) {
    const cn = v?.categoryId
      ? (ctx.categories.value.find(c => c.id === v.categoryId)?.name || '')
      : ''
    return [v?.vodArea, v?.vodYear, cn].filter(Boolean).join(' · ')
  }
  // 把 vod_tag 拆成标签数组用于展示（与后端 ParseTags 保持同样的分隔符语义）。
  function tagListOf(v) {
    const raw = (v?.vodTag || '').trim()
    if (!raw) return []
    const seen = new Set()
    const out = []
    for (const seg of raw.split(/[,，/、|;；\t]+/)) {
      for (const part of seg.trim().split(/\s+/)) {
        const t = part.trim()
        if (!t) continue
        const k = t.toLowerCase()
        if (seen.has(k)) continue
        seen.add(k)
        out.push(t)
      }
    }
    return out
  }

  function hasValue(v) {
    return v !== null && v !== undefined && String(v).trim() !== ''
  }

  return {
    siteNameOf, categoryName, formatTime, modeLabel, statusLabel, short, stat, jobMessage,
    barWidth, isIndeterminate, fmtEta, taskStatusText,
    posterOf, markPicFailed, episodeLabel, scoreOf, metaLine, tagListOf, hasValue,
  }
}
