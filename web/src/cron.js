// 站点定时增量的 5 段 cron（分 时 日 月 周）解析与解释。
// 纯函数、不依赖 Vue / DOM，便于单测；语义与后端 internal/utils/cron.go 保持一致。

const FIELDS = [
  { name: '分钟', min: 0, max: 59 },
  { name: '小时', min: 0, max: 23 },
  { name: '日期', min: 1, max: 31 },
  { name: '月份', min: 1, max: 12 },
  { name: '星期', min: 0, max: 6 },
]
const WEEK_NAMES = ['日', '一', '二', '三', '四', '五', '六']
const pad = n => String(n).padStart(2, '0')

// parseField 把一段表达式展开成允许值集合。
// any 只在「纯 *」时为 true，用于复刻 cron 的 日/周 同时限定取并集的语义。
function parseField(raw, index) {
  const def = FIELDS[index]
  const values = new Set()
  let any = false
  let step = null
  if (!raw) return { error: `第 ${index + 1} 段（${def.name}）为空` }

  const parseNum = (text) => {
    if (!/^\d+$/.test(text)) return NaN
    const n = Number(text)
    if (index === 4 && n === 7) return 7 // 周字段 7 = 周日
    return n >= def.min && n <= def.max ? n : NaN
  }
  const push = (v) => values.add(index === 4 && v === 7 ? 0 : v)

  for (const item of raw.split(',')) {
    if (!item) return { error: `${def.name}段包含空项（逗号多余）` }
    let body = item
    let st = 1
    if (item.includes('/')) {
      const kv = item.split('/')
      if (kv.length !== 2 || !/^\d+$/.test(kv[1]) || Number(kv[1]) < 1) {
        return { error: `${def.name}段「${item}」的步长写法非法，应形如 */15 或 1-10/2` }
      }
      body = kv[0]
      st = Number(kv[1])
      step = st
    }
    let lo, hi
    if (body === '*') {
      lo = def.min
      hi = def.max
      if (st === 1) any = true
    } else if (body.includes('-')) {
      const kv = body.split('-')
      if (kv.length !== 2) return { error: `${def.name}段「${item}」的区间写法非法` }
      lo = parseNum(kv[0])
      hi = parseNum(kv[1])
    } else {
      lo = parseNum(body)
      hi = st > 1 ? def.max : lo
    }
    if (Number.isNaN(lo) || Number.isNaN(hi)) {
      return { error: `${def.name}段「${item}」超出范围 ${def.min}-${def.max}${index === 4 ? '（7 也可表示周日）' : ''}` }
    }
    if (lo > hi) return { error: `${def.name}段「${item}」的区间起点大于终点` }
    for (let v = lo; v <= hi; v += st) push(v)
  }
  if (!values.size) return { error: `${def.name}段没有匹配任何值` }
  return { values, any, step }
}

// parseCron 解析整条表达式，失败时返回带 error 的结果。
export function parseCron(spec) {
  const text = String(spec ?? '').trim()
  const parts = text.split(/\s+/).filter(Boolean)
  if (parts.length !== 5) {
    return { valid: false, error: `需要 5 段（分 时 日 月 周），当前是 ${text ? parts.length : 0} 段` }
  }
  const fields = []
  for (let i = 0; i < 5; i++) {
    const f = parseField(parts[i], i)
    if (f.error) return { valid: false, error: f.error }
    fields.push(f)
  }
  return { valid: true, parts, fields }
}

function listText(values, suffix = '') {
  return [...values].sort((a, b) => a - b).join('、') + suffix
}

function weekText(raw) {
  if (raw === '*') return '每天'
  const map = { '1-5': '周一至周五', '0,6': '周六、周日', '6,0': '周六、周日' }
  if (map[raw]) return '每' + map[raw]
  const parsed = parseCron(`0 0 * * ${raw}`)
  if (!parsed.valid) return `每周 ${raw}`
  const set = parsed.fields[4].values
  const names = [...set].sort((a, b) => a - b).map(v => '周' + WEEK_NAMES[v])
  return '每' + names.join('、')
}

function dayPhrase(dom, dow) {
  if (dom === '*' && dow === '*') return '每天'
  if (dom !== '*' && dow === '*') return `每月 ${dom.replaceAll(',', '、')} 日`
  if (dom === '*' && dow !== '*') return weekText(dow)
  return `每月 ${dom.replaceAll(',', '、')} 日或${weekText(dow)}（满足其一即触发）`
}

// describeCron 给出人类可读的中文含义；复杂表达式回退成分段说明。
export function describeCron(spec) {
  const parsed = parseCron(spec)
  if (!parsed.valid) return ''
  const [mi, hr, dom, mon, dow] = parsed.parts
  const all = s => s === '*'
  const minuteStep = /^\*\/(\d+)$/.exec(mi)
  const hourStep = /^\*\/(\d+)$/.exec(hr)
  const mm = /^\d+$/.test(mi) ? Number(mi) : null
  const hh = /^\d+$/.test(hr) ? Number(hr) : null
  const hhmm = mm !== null && hh !== null && mm < 60 && hh < 24 ? `${pad(hh)}:${pad(mm)}` : null
  const plainDay = all(dom) && all(mon) && all(dow)

  if (minuteStep && all(hr) && plainDay) return `每 ${minuteStep[1]} 分钟自动执行一次增量采集`
  if (hourStep && mm !== null && plainDay) return `每 ${hourStep[1]} 小时的第 ${mm} 分钟自动执行一次增量采集`
  if (all(hr) && mm !== null && plainDay) return `每小时的第 ${mm} 分钟自动执行一次增量采集`
  if (hhmm && plainDay) return `每天 ${hhmm} 自动执行一次增量采集`
  if (hhmm && all(mon)) return `${dayPhrase(dom, dow)} ${hhmm} 自动执行一次增量采集`
  if (hhmm && !all(mon) && !all(dom) && all(dow)) {
    return `每年 ${mon.replaceAll(',', '、')} 月 ${dom.replaceAll(',', '、')} 日 ${hhmm} 自动执行一次增量采集`
  }
  if (hhmm && !all(mon)) {
    return `${mon.replaceAll(',', '、')} 月内${dayPhrase(dom, dow)} ${hhmm} 自动执行一次增量采集`
  }
  const [fm, fh, fd, fmo, fw] = parsed.fields
  const monthPart = fmo.any ? '每月' : `${listText(fmo.values, ' 月')}`
  const weekPart = fw.any ? '' : `；星期 ${[...fw.values].sort((a, b) => a - b).map(v => WEEK_NAMES[v]).join('、')}`
  return `复杂规则：分钟 ${fm.any ? '不限' : listText(fm.values)}；小时 ${fh.any ? '不限' : listText(fh.values)}；日期 ${fd.any ? '不限' : listText(fd.values, ' 日')}；${monthPart}${weekPart}。具体时刻见下方「接下来」。`
}

function matchesDay(fields, date) {
  const dom = fields[2], dow = fields[4]
  const domOK = dom.values.has(date.getDate())
  const dowOK = dow.values.has(date.getDay())
  if (!dom.any && !dow.any) return domOK || dowOK
  if (!dom.any) return domOK
  if (!dow.any) return dowOK
  return true
}

function nextAfter(fields, after) {
  const start = new Date(Math.floor(after.getTime() / 60000) * 60000 + 60000)
  const day = new Date(start.getFullYear(), start.getMonth(), start.getDate())
  for (let d = 0; d < 732; d++) {
    const monthOK = fields[3].values.has(day.getMonth() + 1)
    if (monthOK && matchesDay(fields, day)) {
      for (let h = 0; h < 24; h++) {
        if (!fields[1].values.has(h)) continue
        for (let m = 0; m < 60; m++) {
          if (!fields[0].values.has(m)) continue
          const cand = new Date(day.getFullYear(), day.getMonth(), day.getDate(), h, m)
          if (cand.getTime() > after.getTime()) return cand
        }
      }
    }
    day.setDate(day.getDate() + 1)
  }
  return null
}

// nextRuns 返回接下来 count 次触发时刻（本地时区），最多向前找两年。
export function nextRuns(spec, now = new Date(), count = 3) {
  const parsed = parseCron(spec)
  if (!parsed.valid) return []
  const out = []
  let after = now instanceof Date ? now : new Date(now)
  for (let i = 0; i < count; i++) {
    const next = nextAfter(parsed.fields, after)
    if (!next) break
    out.push(next)
    after = next
  }
  return out
}

// formatCronTime 把触发时刻格式化成 MM-DD HH:mm（跨年时带上年份）。
export function formatCronTime(d, now = new Date()) {
  const base = `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
  return d.getFullYear() === now.getFullYear() ? base : `${d.getFullYear()}-${base}`
}

// evaluateCron 是给表单用的总入口：一次拿到校验状态、含义与接下来的时刻。
export function evaluateCron(spec, options = {}) {
  const now = options.now instanceof Date ? options.now : new Date()
  const count = options.count ?? 3
  const text = String(spec ?? '').trim()
  if (!text) return { state: 'empty', error: '', meaning: '', next: [] }
  const parsed = parseCron(text)
  if (!parsed.valid) return { state: 'invalid', error: parsed.error, meaning: '', next: [] }
  return {
    state: 'ok',
    error: '',
    meaning: describeCron(text),
    next: nextRuns(text, now, count),
  }
}
