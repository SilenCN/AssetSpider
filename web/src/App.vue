<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'

// ---------- 登录态 ----------
const TOKEN_KEY = 'assetspider_token'
const token = ref(localStorage.getItem(TOKEN_KEY) || '')
const me = ref(null)
const authReady = ref(false)
const initialized = ref(false)
const isAdmin = computed(() => !!me.value?.isAdmin)
const currentUsername = computed(() => me.value?.user?.username || '')

function clearAuth() {
  localStorage.removeItem(TOKEN_KEY)
  token.value = ''
  me.value = null
}
function setAuth(tk) {
  token.value = tk
  localStorage.setItem(TOKEN_KEY, tk)
}

// hls.js 体积较大，只在真正播放 m3u8 时按需加载，避免拖慢首屏。
let HlsLib = null
async function loadHls() {
  if (!HlsLib) HlsLib = (await import('hls.js')).default
  return HlsLib
}

const tab = ref('sources')
const health = ref({})
const sources = ref([])
const videos = ref([])
const categories = ref([])
const categoryTree = ref([])
const tags = ref([])
const jobs = ref([])
const tasks = ref([])
const users = ref([])
const capacity = ref({ maxConcurrent: 3, running: 0, queued: 0 })
const maxConcurrent = ref(3)
const capacityDirty = ref(false)
const error = ref('')
const notice = ref(null)

const videoQuery = ref('')
const videoSource = ref('')
const videoCategory = ref('')
const videoTag = ref('')
const videoArea = ref('')
const videoSort = ref('time_desc')
const areas = ref([])
const videoPage = ref(1)
const videoSize = ref(24)
const videoTotal = ref(0)
const videoPages = computed(() => Math.max(1, Math.ceil(videoTotal.value / videoSize.value)))

let noticeTimer = null
function notify(text, type = 'ok') {
  notice.value = { text, type }
  clearTimeout(noticeTimer)
  noticeTimer = setTimeout(() => { notice.value = null }, 7000)
}

async function request(path, options = {}) {
  const headers = { ...(options.headers || {}) }
  if (token.value) headers['Authorization'] = 'Bearer ' + token.value
  const res = await fetch(path, { ...options, headers })
  const body = await res.json().catch(() => ({}))
  // 令牌失效时立即清除本地登录态，回到登录页。
  if (res.status === 401 && token.value) {
    clearAuth()
    throw new Error('登录已失效，请重新登录')
  }
  if (!res.ok) throw new Error(body.error || `请求失败 (${res.status})`)
  return body
}

function jsonOptions(method, data) {
  return {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data),
  }
}

async function loadHealth() {
  health.value = await request('/api/system/v1/health')
}
async function loadSources() {
  sources.value = (await request('/api/sources/v1')) || []
}
async function loadVideos() {
  const p = new URLSearchParams({ page: String(videoPage.value), size: String(videoSize.value) })
  if (videoQuery.value.trim()) p.set('q', videoQuery.value.trim())
  if (videoSource.value) p.set('source_id', String(videoSource.value))
  if (videoCategory.value) p.set('category_id', String(videoCategory.value))
  if (videoTag.value) p.set('tag_id', String(videoTag.value))
  if (videoArea.value) p.set('area', videoArea.value)
  if (videoSort.value) p.set('sort', videoSort.value)
  const d = await request('/api/videos/v1?' + p.toString())
  videos.value = d.items || []
  videoTotal.value = d.total || 0
  videoPage.value = d.page || 1
  videoSize.value = d.size || 24
}
async function loadAreas() {
  try { areas.value = (await request('/api/videos/v1/areas')) || [] } catch (e) { areas.value = [] }
}
async function loadJobs() {
  jobs.value = (await request('/api/jobs/v1?limit=50')) || []
}
// 采集任务（排队/运行/暂停）与并发容量
async function loadTasks() {
  const d = (await request('/api/tasks/v1')) || {}
  tasks.value = d.items || []
  if (d.capacity) capacity.value = d.capacity
  // 用户正在输入时不覆盖输入框
  if (!capacityDirty.value) maxConcurrent.value = capacity.value.maxConcurrent
}
async function loadCategories() {
  categories.value = (await request('/api/categories/v1')) || []
}
async function loadCategoryTree() {
  categoryTree.value = (await request('/api/categories/v1/tree')) || []
}
async function loadTags() {
  tags.value = (await request('/api/tags/v1')) || []
}

async function loadUsers() {
  if (!isAdmin.value) { users.value = []; return }
  users.value = (await request('/api/users/v1')) || []
}

async function refresh() {
  error.value = ''
  try {
    const pending = [
      loadHealth(), loadSources(), loadVideos(), loadJobs(),
      loadCategories(), loadCategoryTree(), loadTags(), loadAreas(), loadTasks(),
    ]
    if (isAdmin.value) pending.push(loadUsers())
    await Promise.all(pending)
  } catch (e) {
    error.value = e.message
  }
}

function categoryName(id) {
  return categories.value.find(c => c.id === id)?.name || '-'
}
// 站点正在进行的采集任务（排队/运行），用于把「全量/增量」换成「暂停」
function activeTaskOf(sourceId) {
  return tasks.value.find(t =>
    t.sourceId === sourceId && (t.status === 'running' || t.status === 'queued')) || null
}
function showCategoryVideos(c) {
  videoCategory.value = String(c.id)
  videoPage.value = 1
  tab.value = 'videos'
  loadVideos().catch(e => { error.value = e.message })
}
// 点击卡片上的标签：按该标签筛选资源库（标签名大小写不敏感匹配）。
function filterByTag(name) {
  const t = tags.value.find(x => (x.name || '').toLowerCase() === String(name).toLowerCase())
  if (!t) return
  videoTag.value = String(t.id)
  videoPage.value = 1
  tab.value = 'videos'
  loadVideos().catch(e => { error.value = e.message })
}

// ---------- 分类树（站点 → 一级分类 → 二级分类）----------
// 分类按站点隔离（source_id + cat_key 唯一），因此顶层用资源站点名分组，
// 站点下再挂一级分类，一级分类下挂二级分类，构成最多三层的树。
const catCollapsed = reactive(new Set())

function isCatCollapsed(key) { return catCollapsed.has(key) }
function toggleCat(key) {
  if (catCollapsed.has(key)) catCollapsed.delete(key)
  else catCollapsed.add(key)
}
// 虚拟分组节点没有分类 ID，用自身的 key 作为折叠标识
function catKey(node) { return node.virtual ? node.key : `cat-${node.id}` }

function siteNameOf(id) {
  return sources.value.find(s => s.id === id)?.name || `站点 #${id}`
}

const categoryForest = computed(() => {
  const sites = new Map()
  for (const node of categoryTree.value) {
    const sid = node.sourceId || 0
    if (!sites.has(sid)) {
      sites.set(sid, {
        key: `site-${sid}`, sourceId: sid, name: siteNameOf(sid),
        children: [], catCount: 0, videoCount: 0,
      })
    }
    sites.get(sid).children.push(node)
  }
  const order = new Map(sources.value.map((s, i) => [s.id, i]))
  return [...sites.values()]
    .map(site => {
      // 后端把「找不到父分类」的二级分类当成根节点一并返回（父分类可能没被采到，
      // 例如站点只返回了二级分类的影片）。这类节点不直接铺在站点下，否则看起来
      // 像是站点的一级分类，统一收进「未归类」分组。
      const roots = []
      const orphans = []
      for (const node of site.children) {
        if (node.level === 2) orphans.push(node)
        else roots.push(node)
      }
      let catCount = 0
      let videoCount = 0
      const walk = n => {
        catCount++
        videoCount += n.videoCount || 0
        for (const ch of n.children || []) walk(ch)
      }
      roots.forEach(walk)
      orphans.forEach(walk)
      if (orphans.length) {
        roots.push({
          key: `orphan-${site.sourceId}`, virtual: true, name: '未归类',
          hint: '父分类未采集到，暂挂在此', videoCount: orphans.reduce((n, c) => n + (c.videoCount || 0), 0),
          children: orphans,
        })
      }
      site.children = roots
      site.catCount = catCount
      site.videoCount = videoCount
      return site
    })
    .sort((a, b) => (order.get(a.sourceId) ?? 1e6) - (order.get(b.sourceId) ?? 1e6))
})

const catTotal = computed(() => categoryForest.value.reduce(
  (n, s) => n + s.catCount, 0))

// 分类下拉里带上层级路径（一级 / 二级），避免不同站点的同名分类难以区分
const catPath = computed(() => {
  const m = new Map()
  const walk = (n, prefix) => {
    const path = prefix ? `${prefix} / ${n.name}` : n.name
    m.set(n.id, path)
    for (const ch of n.children || []) walk(ch, path)
  }
  categoryTree.value.forEach(n => walk(n, ''))
  return m
})

function catLabel(c) {
  return `${siteNameOf(c.sourceId)} · ${catPath.value.get(c.id) || c.name}`
}

function toggleAllCats() {
  const allOpen = categoryForest.value.every(s => !isCatCollapsed(s.key))
  for (const site of categoryForest.value) {
    const keys = [site.key]
    const walk = n => { if (n.children?.length) keys.push(catKey(n)); (n.children || []).forEach(walk) }
    site.children.forEach(walk)
    for (const k of keys) {
      if (allOpen) catCollapsed.add(k)
      else catCollapsed.delete(k)
    }
  }
}

// ---------- 全字段详情 ----------
const fieldMeta = ref({ groups: [], fields: [], total: 0 })
const detail = ref(null)
const detailTab = ref('fields')
const onlyNonEmpty = ref(true)

async function loadFieldMeta() {
  try { fieldMeta.value = await request('/api/meta/v1/fields') } catch (e) { /* 元数据不影响主流程 */ }
}

function hasValue(v) {
  return v !== null && v !== undefined && String(v).trim() !== ''
}

const nonEmptyCount = computed(() => {
  if (!detail.value) return 0
  return (fieldMeta.value.fields || []).filter(f => hasValue(detail.value[f.json])).length
})

const groupedFields = computed(() => {
  const groups = fieldMeta.value.groups || []
  const fields = fieldMeta.value.fields || []
  return groups.map(g => ({
    key: g.key,
    label: g.label,
    items: fields.filter(f => f.group === g.key && (!onlyNonEmpty.value || hasValue(detail.value?.[f.json]))),
  })).filter(g => g.items.length)
})

const extraPairs = computed(() => {
  if (!detail.value?.extra) return []
  try {
    const obj = JSON.parse(detail.value.extra)
    return Object.entries(obj).map(([k, v]) => [k, typeof v === 'string' ? v : JSON.stringify(v)])
  } catch (e) { return [] }
})

// 按苹果CMS 字段解析出的结构化播放组（来自 detail.vodPlayList 的 JSON）。
const playGroups = computed(() => {
  const raw = detail.value?.vodPlayList
  if (!raw) return []
  try {
    const arr = JSON.parse(raw)
    return Array.isArray(arr) ? arr : []
  } catch (e) { return [] }
})

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
  const cn = v?.categoryId ? (categories.value.find(c => c.id === v.categoryId)?.name || '') : ''
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

// ---------- 详情与播放 ----------
const playerEl = ref(null)
const activeGroup = ref(0)
const activeEp = ref(0)
let hlsInstance = null

const currentGroup = computed(() => playGroups.value[activeGroup.value] || null)
const currentEpisodes = computed(() => currentGroup.value?.episodes || [])
const currentEpisode = computed(() => currentEpisodes.value[activeEp.value] || null)
const currentUrl = computed(() => currentEpisode.value?.url || '')

// 站点配置了「视频解析地址」时，后端把播放地址拼成解析服务网页地址并打上
// frame 标记；此时必须用 iframe 承载，<video> 无法直链播放（地址里常带
// ?url=...m3u8，容易被后缀判断误认成媒体流）。parseEnabled 作为旧数据兜底。
const detailSource = computed(() => sources.value.find(s => s.id === detail.value?.sourceId) || null)
const parseEnabled = computed(() => !!detailSource.value?.parseApi)
const useFrame = computed(() => !!currentUrl.value && (currentEpisode.value?.frame === true || parseEnabled.value))

// 含 m3u8 即按 HLS 处理（很多站点地址是 ...?url=xxx.m3u8，不能只匹配结尾）。
const isHls = computed(() => !!currentUrl.value && /m3u8/i.test(currentUrl.value))
const isDirectVideo = computed(() => !!currentUrl.value && /\.(mp4|webm|ogg|mov|m4v)(\?|#|$)/i.test(currentUrl.value))
// 苹果CMS 不少站点的播放地址不带后缀（如 /play/abc123），不能按后缀判死。
// 这类地址先交给 hls.js 试播（多数实际是 m3u8），失败再回退 <video> 直链。
const isUnknownStream = computed(() => !!currentUrl.value && !useFrame.value && !isHls.value && !isDirectVideo.value)
const canPlay = computed(() => !!currentUrl.value && (isHls.value || isDirectVideo.value || isUnknownStream.value))

const playError = ref('')
let playErrorTimer = null

function onVideoLoaded() {
  playError.value = ''
  if (playErrorTimer) { clearTimeout(playErrorTimer); playErrorTimer = null }
}

function hlsErrorText(data) {
  const map = {
    manifestLoadError: '播放列表加载失败（地址不可达、跨域或被拦截）',
    manifestLoadTimeOut: '播放列表加载超时',
    manifestParsingError: '播放列表格式错误（不是合法 m3u8）',
    levelLoadError: '线路加载失败',
    fragLoadError: '视频分片加载失败',
    fragLoadTimeOut: '视频分片加载超时',
    manifestIncompatibleCodecsError: '浏览器不支持该视频编码',
    bufferAppendError: '视频缓冲写入失败',
  }
  return map[data?.details] || data?.details || '未知错误'
}

function videoErrorText(code) {
  return ({
    1: '加载被中止',
    2: '网络错误（地址不可达或被拦截）',
    3: '视频解码失败（浏览器不支持该编码）',
    4: '媒体地址不支持或格式不匹配',
  })[code] || '未知错误'
}

function onVideoError(e) {
  // destroyPlayer 里的 removeAttribute('src') + load() 也会触发 error，忽略空 src 的情况。
  if (!e.target?.getAttribute('src')) return
  playError.value = '播放失败：' + videoErrorText(e.target?.error?.code) + '。可在新窗口打开原地址确认，或为站点配置视频解析地址。'
}

function destroyPlayer() {
  if (playErrorTimer) { clearTimeout(playErrorTimer); playErrorTimer = null }
  if (hlsInstance) {
    try { hlsInstance.destroy() } catch (e) { /* 忽略销毁异常 */ }
    hlsInstance = null
  }
  const el = playerEl.value
  if (el) {
    try { el.pause() } catch (e) { /* 元素可能已卸载 */ }
    try { el.removeAttribute('src'); el.load() } catch (e) { /* 忽略 */ }
  }
}

// 按地址类型挂载播放器。
//
// 关键：Linux/Chrome 的 canPlayType('application/vnd.apple.mpegurl') 会返回 'maybe'，
// 但原生 <video> 实际解不了 m3u8（只有 Safari 能），直接赋 src 必然
// MEDIA_ERR_SRC_NOT_SUPPORTED。因此只要 MediaSource 可用就一律优先用 hls.js，
// 原生播放仅作为 hls.js 不可用且浏览器明确返回 'probably' 时的兜底。
async function mountPlayer() {
  destroyPlayer()
  playError.value = ''
  await nextTick()
  if (useFrame.value) return // 解析地址走 iframe，无需挂载 <video>
  const el = playerEl.value
  const url = currentUrl.value
  if (!el || !url || !canPlay.value) return
  if (isHls.value || isUnknownStream.value) {
    try {
      const Hls = await loadHls()
      if (!Hls.isSupported()) throw new Error('浏览器不支持 MediaSource，无法播放 HLS')
      const inst = new Hls({ enableWorker: true })
      hlsInstance = inst
      let recovered = false
      inst.on(Hls.Events.MANIFEST_PARSED, () => { el.play().catch(() => {}) })
      inst.on(Hls.Events.ERROR, (_evt, data) => {
        if (!data?.fatal) return
        // 无后缀地址可能其实是直链视频：hls.js 试播失败后回退 <video> 再试一次。
        if (isUnknownStream.value) {
          try { inst.destroy() } catch (e) { /* 忽略 */ }
          hlsInstance = null
          el.src = url
          el.play().catch(() => {})
          return
        }
        if (data.type === Hls.ErrorTypes.NETWORK_ERROR && !recovered) {
          recovered = true
          playError.value = 'HLS ' + hlsErrorText(data) + '，正在重试…'
          try { inst.startLoad() } catch (e) { /* 忽略 */ }
          // 重试迟迟不成功时给出明确结论，避免一直停在「正在重试」。
          playErrorTimer = setTimeout(() => {
            if (playError.value.includes('正在重试')) {
              playError.value = 'HLS 播放失败：' + hlsErrorText(data) + '。请检查站点的播放域名 / 解析地址配置，或在新窗口打开原地址确认。'
            }
          }, 8000)
          return
        }
        playError.value = 'HLS 播放失败：' + hlsErrorText(data) + '。可在新窗口打开原地址确认，或为站点配置视频解析地址。'
      })
      inst.loadSource(url)
      inst.attachMedia(el)
      return
    } catch (e) {
      // 只有明确支持原生 HLS（Safari 返回 'probably'）时才回退，'maybe' 不可信。
      if (el.canPlayType('application/vnd.apple.mpegurl') === 'probably') {
        el.src = url
        el.play().catch(() => {})
        return
      }
      playError.value = '无法播放该地址：' + (e?.message || String(e))
      return
    }
  }
  el.src = url
  el.play().catch(() => { /* 自动播放被浏览器拦截时由用户点播放键 */ })
}

function chooseGroup(i) {
  if (i === activeGroup.value) return
  activeGroup.value = i
  activeEp.value = 0
  mountPlayer()
}
function chooseEp(i) {
  activeEp.value = i
  mountPlayer()
}
function switchDetailTab(t) {
  if (t === detailTab.value) return
  if (t === 'play') {
    detailTab.value = t
    mountPlayer()
  } else {
    destroyPlayer()
    detailTab.value = t
  }
}
function closeDetail() {
  destroyPlayer()
  detail.value = null
}

async function openDetail(v) {
  detail.value = v
  detailTab.value = 'play'
  onlyNonEmpty.value = true
  // 默认选中第一条有剧集的线路
  const gi = playGroups.value.findIndex(g => (g.episodes?.length || 0) > 0)
  activeGroup.value = gi >= 0 ? gi : 0
  activeEp.value = 0
  if (!fieldMeta.value.fields?.length) await loadFieldMeta()
  mountPlayer()
}

onBeforeUnmount(destroyPlayer)

// Esc 关闭当前弹窗（导入确认 → 站点配置 → 资源详情）
function onEsc(e) {
  if (e.key !== 'Escape') return
  if (importOpen.value) { closeImport(); return }
  if (sourceOpen.value) { closeSourceForm(); return }
  if (detail.value) closeDetail()
}
onMounted(() => window.addEventListener('keydown', onEsc))
onBeforeUnmount(() => window.removeEventListener('keydown', onEsc))

// ---------- 站点配置 ----------
const emptyForm = () => ({
  id: null, name: '', apiBase: '', format: 'seacms_json', extraParams: '',
  incrementalHours: 24, maxPages: 0, enabled: true,
  parseApi: '', playDomain: '', picApi: '', picDomain: '',
})
const form = reactive(emptyForm())
const editing = computed(() => !!form.id)
const sourceOpen = ref(false)

function resetForm() { Object.assign(form, emptyForm()) }

function sourcePayload(s) {
  return {
    name: s.name, apiBase: s.apiBase, format: s.format || 'seacms_json',
    extraParams: s.extraParams || '', enabled: s.enabled,
    incrementalHours: Number(s.incrementalHours) || 0,
    maxPages: Number(s.maxPages) || 0,
    parseApi: s.parseApi || '', playDomain: s.playDomain || '',
    picApi: s.picApi || '', picDomain: s.picDomain || '',
  }
}

// 站点配置改成弹窗：列表页只留一个「新增站点」入口，编辑时点行内「编辑」打开同一个弹窗。
function openSourceForm(s) {
  if (s) {
    Object.assign(form, {
      id: s.id, name: s.name, apiBase: s.apiBase, format: s.format,
      extraParams: s.extraParams, incrementalHours: s.incrementalHours,
      maxPages: s.maxPages, enabled: s.enabled,
      parseApi: s.parseApi || '', playDomain: s.playDomain || '',
      picApi: s.picApi || '', picDomain: s.picDomain || '',
    })
  } else {
    resetForm()
  }
  error.value = ''
  sourceOpen.value = true
}

function closeSourceForm() {
  sourceOpen.value = false
  resetForm()
}

function editSource(s) { openSourceForm(s) }

// ---------- 站点配置 JSON 导入 / 导出 ----------
const importOpen = ref(false)
const importBusy = ref(false)
const importFileName = ref('')
const importRaw = ref('')
const importPreview = ref([])
const importFileEl = ref(null)
const importPreviewShown = computed(() => importPreview.value.slice(0, 30))

async function exportSources() {
  try {
    const data = await request('/api/sources/v1/export')
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
    notify(`已导出 ${data.sources?.length || 0} 个站点配置`)
  } catch (e) { notify(e.message, 'err') }
}

function pickImportFile() {
  if (!importFileEl.value) return
  importFileEl.value.value = '' // 允许连续选同一个文件
  importFileEl.value.click()
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
    importRaw.value = text
    importFileName.value = file.name
    importPreview.value = items.map(s => ({
      name: typeof s?.name === 'string' ? s.name : '',
      apiBase: typeof s?.apiBase === 'string' ? s.apiBase : '',
    }))
    importOpen.value = true
  } catch (err) {
    notify(`无法读取 JSON：${err.message}`, 'err')
  }
}

function closeImport() {
  importOpen.value = false
  importRaw.value = ''
  importFileName.value = ''
  importPreview.value = []
}

async function confirmImport() {
  if (importBusy.value) return
  importBusy.value = true
  try {
    const res = await request('/api/sources/v1/import', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: importRaw.value,
    })
    const issues = (res.issues || []).slice(0, 3)
      .map(i => `第 ${i.index + 1} 条「${i.name || '未命名'}」：${i.error}`).join('；')
    const tail = res.failed ? `、失败 ${res.failed} 个${issues ? `（${issues}）` : ''}` : ''
    notify(`导入完成：新增 ${res.created} 个、更新 ${res.updated} 个${tail}`, res.failed ? 'err' : 'ok')
    closeImport()
    await refresh()
  } catch (e) {
    notify(e.message, 'err')
  } finally {
    importBusy.value = false
  }
}

async function saveSource() {
  error.value = ''
  try {
    const payload = sourcePayload(form)
    if (form.id) await request(`/api/sources/v1/${form.id}`, jsonOptions('PUT', payload))
    else await request('/api/sources/v1', jsonOptions('POST', payload))
    closeSourceForm()
    notify('站点配置已保存')
    await refresh()
  } catch (e) {
    error.value = e.message
    notify(e.message, 'err')
  }
}

async function removeSource(s) {
  if (!confirm(`确认删除站点「${s.name}」？该站点的影片数据与采集记录会一并删除。`)) return
  try {
    await request(`/api/sources/v1/${s.id}`, { method: 'DELETE' })
    notify('站点已删除')
    await refresh()
  } catch (e) { notify(e.message, 'err') }
}

async function toggleSource(s) {
  try {
    await request(`/api/sources/v1/${s.id}`,
      jsonOptions('PUT', { ...sourcePayload(s), enabled: !s.enabled }))
    await refresh()
  } catch (e) { notify(e.message, 'err') }
}

// ---------- 采集任务 ----------
// 采集改为异步任务：发起后立即返回，进度在「采集任务」里实时轮询；
// 运行中的任务可随时手动暂停（已完成页的数据已入库，续采页码会保存）。
async function startTask(s, mode) {
  const label = modeLabel(mode)
  try {
    const t = await request('/api/tasks/v1', jsonOptions('POST', { sourceId: s.id, mode }))
    notify(`「${s.name}」${label}采集已入队（任务 #${t.jobId}）`, 'info')
    await loadTasks()
  } catch (e) { notify(e.message, 'err') }
}

async function crawlAll(mode) {
  const list = sources.value.filter(s => s.enabled)
  if (!list.length) { notify('没有启用的站点', 'err'); return }
  const label = modeLabel(mode)
  let ok = 0
  const failures = []
  for (const s of list) {
    try {
      await request('/api/tasks/v1', jsonOptions('POST', { sourceId: s.id, mode }))
      ok++
    } catch (e) { failures.push(`${s.name}: ${e.message}`) }
  }
  if (ok) {
    notify(`已提交 ${ok}/${list.length} 个${label}任务，超出并发上限的会在队列中等待`, failures.length ? 'err' : 'ok')
  } else {
    notify(`批量采集失败：${failures[0] || '未知错误'}`, 'err')
  }
  await loadTasks()
}

async function pauseTask(jobId) {
  try {
    await request(`/api/tasks/v1/${jobId}`, jsonOptions('PUT', { status: 'paused' }))
    notify(`任务 #${jobId} 已暂停，进度已保存`, 'ok')
    await Promise.all([loadTasks(), loadJobs()])
  } catch (e) { notify(e.message, 'err') }
}

async function resumeTask(jobId) {
  try {
    const t = await request(`/api/tasks/v1/${jobId}`, jsonOptions('PUT', { status: 'running' }))
    notify(`任务 #${t.jobId} 已继续，从暂停页接着采（同一记录，统计累加）`, 'ok')
    await Promise.all([loadTasks(), loadJobs()])
  } catch (e) { notify(e.message, 'err') }
}

// 失败任务：复用原记录，从失败页续采（统计与尝试次数继续累加）
async function retryTask(jobId) {
  try {
    const t = await request(`/api/tasks/v1/${jobId}/retry`, { method: 'POST' })
    notify(`任务 #${t.jobId} 已重新入队，从失败页继续（同一记录，统计累加）`, 'ok')
    await Promise.all([loadTasks(), loadJobs()])
  } catch (e) { notify(e.message, 'err') }
}

// 已结束任务：复用原记录，忽略断点从第 1 页重采，统计清零
async function rerunTask(jobId) {
  try {
    const t = await request(`/api/tasks/v1/${jobId}/rerun`, { method: 'POST' })
    notify(`任务 #${t.jobId} 已重新运行，从第 1 页开始（同一记录，统计已清零）`, 'ok')
    await Promise.all([loadTasks(), loadJobs()])
  } catch (e) { notify(e.message, 'err') }
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

async function saveCapacity() {
  try {
    const c = await request('/api/tasks/v1/capacity',
      jsonOptions('PUT', { maxConcurrent: Number(maxConcurrent.value) }))
    capacity.value = c
    capacityDirty.value = false
    notify(`并发上限已调整为 ${c.maxConcurrent} 个任务`)
  } catch (e) { notify(e.message, 'err') }
}

function searchVideos() { videoPage.value = 1; loadVideos().catch(e => { error.value = e.message }) }
function gotoPage(p) {
  if (p < 1 || p > videoPages.value) return
  videoPage.value = p
  loadVideos().catch(e => { error.value = e.message })
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

// ---------- 登录 / 会话 ----------
const loginForm = reactive({ username: '', password: '' })
const loginBusy = ref(false)
const loginError = ref('')

async function doLogin() {
  loginError.value = ''
  loginBusy.value = true
  try {
    const d = await request('/api/auth/v1/sessions', jsonOptions('POST', {
      username: loginForm.username.trim(), password: loginForm.password,
    }))
    setAuth(d.token)
    me.value = { user: d.user, isAdmin: d.isAdmin, visibleSourceIds: [] }
    initialized.value = true
    loginForm.password = ''
    notify(d.firstUser ? '系统已初始化，你的账号自动注册为管理员' : `欢迎回来，${d.user.username}`)
    loadFieldMeta().catch(() => {})
    await refresh()
  } catch (e) {
    loginError.value = e.message
  } finally {
    loginBusy.value = false
  }
}

async function doLogout() {
  try { await request('/api/auth/v1/sessions', { method: 'DELETE' }) } catch (e) { /* 忽略 */ }
  clearAuth()
  tab.value = 'sources'
  notify('已退出登录', 'info')
}

const pwdModal = ref(false)
const pwdForm = reactive({ oldPassword: '', newPassword: '' })
async function doChangePassword() {
  try {
    const d = await request('/api/auth/v1/password', jsonOptions('PUT', { ...pwdForm }))
    setAuth(d.token)
    pwdForm.oldPassword = ''
    pwdForm.newPassword = ''
    pwdModal.value = false
    notify(d.message || '密码已修改')
  } catch (e) { notify(e.message, 'err') }
}

async function bootstrap() {
  authReady.value = false
  try {
    const st = await request('/api/auth/v1/status')
    initialized.value = st.initialized
    if (token.value) {
      try { me.value = await request('/api/auth/v1/me') } catch (e) { clearAuth() }
    }
  } catch (e) {
    error.value = e.message
  } finally {
    authReady.value = true
  }
  if (me.value) {
    loadFieldMeta().catch(() => {})
    await refresh()
  }
}

// ---------- 用户管理（管理员） ----------
const emptyUserForm = () => ({
  id: null, username: '', password: '', role: 'user', enabled: true, sourceIds: [],
})
const userForm = reactive(emptyUserForm())
const userEditing = computed(() => !!userForm.id)

function resetUserForm() { Object.assign(userForm, emptyUserForm()) }

function editUser(u) {
  Object.assign(userForm, {
    id: u.id, username: u.username, password: '', role: u.role,
    enabled: u.enabled, sourceIds: [...(u.sourceIds || [])],
  })
  window.scrollTo({ top: 0, behavior: 'smooth' })
}

async function saveUser() {
  error.value = ''
  try {
    const payload = {
      role: userForm.role, enabled: userForm.enabled,
      sourceIds: [...userForm.sourceIds],
    }
    if (userForm.id) {
      if (userForm.password) payload.password = userForm.password
      await request(`/api/users/v1/${userForm.id}`, jsonOptions('PUT', payload))
    } else {
      payload.username = userForm.username.trim()
      payload.password = userForm.password
      await request('/api/users/v1', jsonOptions('POST', payload))
    }
    resetUserForm()
    notify('用户信息已保存')
    await refresh()
  } catch (e) {
    error.value = e.message
    notify(e.message, 'err')
  }
}

async function toggleUser(u) {
  try {
    await request(`/api/users/v1/${u.id}`, jsonOptions('PUT', { enabled: !u.enabled }))
    notify(u.enabled ? '账号已停用' : '账号已启用')
    await refresh()
  } catch (e) { notify(e.message, 'err') }
}

async function removeUser(u) {
  if (!confirm(`确认删除用户「${u.username}」？该用户的登录会话与站点授权会一并清除。`)) return
  try {
    await request(`/api/users/v1/${u.id}`, { method: 'DELETE' })
    notify('用户已删除')
    if (userForm.id === u.id) resetUserForm()
    await refresh()
  } catch (e) { notify(e.message, 'err') }
}

function visibleLabel(u) {
  if (u.role === 'admin') return '全部站点（管理员）'
  const ids = u.sourceIds || []
  if (!ids.length) return '未授权任何站点'
  return ids.map(id => sources.value.find(s => s.id === id)?.name || `#${id}`).join('、')
}

// 采集数据（站点状态 / 实时任务 / 采集记录）只在这两个页签自动刷新。
// 停留在资源库、视频分类、用户管理时不发轮询请求，避免无谓流量和其它页面被反复重绘。
const collectTabs = ['sources', 'jobs']
function refreshCollectData() {
  // 站点配置页看任务卡片与站点「上次采集」状态；采集记录页看记录（含运行中的 live 进度）。
  if (tab.value === 'sources') return Promise.all([loadTasks(), loadSources()])
  if (tab.value === 'jobs') return loadJobs()
  return Promise.resolve()
}

let taskTimer = null
onMounted(async () => {
  await bootstrap()
  taskTimer = setInterval(() => {
    if (!me.value || document.hidden || !collectTabs.includes(tab.value)) return
    refreshCollectData().catch(() => {})
  }, 1500)
})
onBeforeUnmount(() => { clearInterval(taskTimer) })

// 从别的页签切回来时立刻补一次，不用等下一个轮询周期；页面重新可见时也补一次。
watch(tab, (v) => {
  if (!me.value || !collectTabs.includes(v)) return
  refreshCollectData().catch(() => {})
})
function onVisible() {
  if (!document.hidden && me.value && collectTabs.includes(tab.value)) refreshCollectData().catch(() => {})
}
onMounted(() => document.addEventListener('visibilitychange', onVisible))
onBeforeUnmount(() => document.removeEventListener('visibilitychange', onVisible))
</script>

<template>
  <div v-if="!authReady" class="boot">正在检查登录状态…</div>

  <div v-else-if="!me" class="login-wrap">
    <div class="login-card">
      <h1>AssetSpider</h1>
      <p class="muted">视频站点采集 · JSON 接口 · 全量 / 增量 · 全字段入库</p>
      <form @submit.prevent="doLogin">
        <input v-model="loginForm.username" placeholder="用户名" autocomplete="username" required />
        <input v-model="loginForm.password" type="password" placeholder="密码" autocomplete="current-password" required />
        <button type="submit" :disabled="loginBusy">
          {{ loginBusy ? '登录中…' : (initialized ? '登录' : '注册并登录') }}
        </button>
      </form>
      <p v-if="!initialized" class="hint">
        第一个登录的账号将<strong>自动注册为管理员</strong>，拥有全部权限；之后可创建用户并分配可见站点。
      </p>
      <p v-if="loginError" class="error">{{ loginError }}</p>
    </div>
  </div>

  <template v-else>
  <main class="page">
    <header>
      <h1>AssetSpider</h1>
      <p>视频站点采集 · JSON 接口 · 全量 / 增量 · 全字段入库 · Vue 3 + Gin + GORM/SQLite</p>
      <div class="stats">
        <span class="db">DB: {{ health.database || '加载中…' }}</span>
        <span class="stat">可见站点 <b>{{ sources.length }}</b></span>
        <span class="stat">可见影片 <b>{{ videoTotal }}</b></span>
        <span class="stat">字段 <b>{{ fieldMeta.total || '-' }}</b></span>
      </div>
      <div class="userbar">
        <span class="host">{{ currentUsername }}</span>
        <span class="badge" :class="isAdmin ? 'on' : 'off'">{{ isAdmin ? '管理员' : '普通用户' }}</span>
        <button class="mini ghost" @click="pwdModal = true">修改密码</button>
        <button class="mini ghost" @click="doLogout">退出登录</button>
      </div>
    </header>

    <div v-if="notice" class="notice" :class="notice.type">{{ notice.text }}</div>
    <p v-if="error" class="error">{{ error }}</p>

    <nav class="tabs">
      <button :class="{ active: tab === 'sources' }" @click="tab = 'sources'">站点配置</button>
      <button :class="{ active: tab === 'videos' }" @click="tab = 'videos'; loadVideos()">资源库</button>
      <button :class="{ active: tab === 'categories' }" @click="tab = 'categories'; loadCategories()">视频分类</button>
      <button :class="{ active: tab === 'jobs' }" @click="tab = 'jobs'">采集记录</button>
      <button v-if="isAdmin" :class="{ active: tab === 'users' }" @click="tab = 'users'; loadUsers()">用户管理</button>
      <button class="ghost refresh" @click="refresh">刷新</button>
    </nav>

    <!-- 站点配置 -->
    <template v-if="tab === 'sources'">
      <section class="card">
        <div class="list-head">
          <h2>采集任务</h2>
          <span class="muted">
            运行 {{ capacity.running }} / {{ capacity.maxConcurrent }} · 排队 {{ capacity.queued }}
          </span>
        </div>
        <div class="actions">
          <button @click="crawlAll('full')">全量采集可见站点</button>
          <button class="ghost" @click="crawlAll('incremental')">增量采集可见站点</button>
          <template v-if="isAdmin">
            <label class="field inline">最多同时运行
              <input v-model.number="maxConcurrent" type="number" min="1" max="64"
                     @input="capacityDirty = true" />
            </label>
            <button class="ghost" :disabled="!capacityDirty" @click="saveCapacity">保存上限</button>
          </template>
        </div>
        <p class="hint">
          采集为异步任务：发起后立即入队，同时运行的任务数不超过上面的上限，其余排队等待。
          运行中的任务可随时<b>暂停</b>——已完成分页的数据已经入库，全量采集的续采页码与增量采集的游标都会保存，
          点「继续」即可从断点接着采。抓取失败会<b>自动退避重试</b>（次数由 <code>collector.retry_times</code> 配置），
          仍然失败的任务可在「采集记录」里<b>手动重试</b>（从失败页续采）或<b>重新运行</b>（从第 1 页重来）——
          <b>继续 / 重试 / 重新运行都复用原任务记录</b>，不会多出一条历史；重新运行时统计与尝试次数清零。
        </p>
        <p v-if="!tasks.length" class="empty">当前没有排队或运行中的采集任务。</p>
        <div v-else class="table-wrap">
          <table>
            <thead>
              <tr><th class="hide-sm">任务</th><th>站点</th><th>模式</th><th>状态</th><th class="prog-col">实时进度</th><th title="拉取 / 新增 / 更新 / 未变">拉取/新增/更新/未变</th><th class="hide-sm">速率</th><th class="hide-sm">已运行</th><th>操作</th></tr>
            </thead>
            <tbody>
              <tr v-for="t in tasks" :key="t.jobId">
                <td class="mono hide-sm">#{{ t.jobId }}</td>
                <td class="host">{{ t.sourceName }}</td>
                <td>
                  {{ modeLabel(t.mode) }}
                  <div v-if="t.maxAttempts > 1" class="tiny">第 {{ t.attempt }}/{{ t.maxAttempts }} 次尝试</div>
                </td>
                <td>
                  <span class="badge" :class="t.retryInSeconds > 0 ? 'paused' : t.status">{{ taskStatusText(t) }}</span>
                </td>
                <td class="prog-col">
                  <div class="prog-track" :class="{ indet: isIndeterminate(t) }">
                    <i :style="{ width: barWidth(t) }"></i>
                  </div>
                  <div class="prog-meta">
                    <b>{{ isIndeterminate(t) ? '—' : Math.floor(t.percent) + '%' }}</b>
                    <span v-if="t.pageCount > 0">{{ t.page }} / {{ t.pageCount }} 页</span>
                    <span v-else>{{ t.pages }} 页</span>
                    <span>{{ t.fetched }} 条</span>
                    <span v-if="t.etaSeconds > 0">剩余 {{ fmtEta(t.etaSeconds) }}</span>
                  </div>
                  <div v-if="t.message" class="tiny msg" :title="t.message">{{ short(t.message, 60) }}</div>
                </td>
                <td class="mono stats" :title="`拉取 ${t.fetched} 条，新增 ${t.new}，更新 ${t.updated}，未变 ${t.unchanged}`">
                  {{ t.fetched }} / <span class="up">{{ t.new }}</span> / {{ t.updated }} / <span class="muted">{{ t.unchanged }}</span>
                </td>
                <td class="mono muted hide-sm">{{ t.itemsPerSecond > 0 ? t.itemsPerSecond.toFixed(1) + ' 条/秒' : '-' }}</td>
                <td class="muted hide-sm">{{ t.elapsedSeconds }}s</td>
                <td class="ops">
                  <button v-if="t.status === 'running' || t.status === 'queued'"
                          class="mini danger" @click="pauseTask(t.jobId)">暂停</button>
                  <button v-else-if="t.status === 'paused'"
                          class="mini ghost" @click="resumeTask(t.jobId)">继续</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <section class="card">
        <div class="list-head">
          <div class="list-head-l">
            <h2>站点列表</h2><span class="muted">共 {{ sources.length }} 个</span>
          </div>
          <div class="ops import-ops">
            <button class="ghost" @click="exportSources">导出 JSON</button>
            <button v-if="isAdmin" class="ghost" @click="pickImportFile">导入 JSON</button>
            <button v-if="isAdmin" @click="openSourceForm()">+ 新增站点</button>
            <input ref="importFileEl" class="hidden-file" type="file" accept=".json,application/json" @change="onImportFile" />
          </div>
        </div>
        <p v-if="!sources.length" class="empty">{{ isAdmin ? '还没有站点，点右上角「新增站点」添加一个 JSON 采集接口。' : '管理员尚未给你分配任何可见站点。' }}</p>
        <div v-else class="table-wrap">
          <table>
            <thead>
              <tr>
                <th class="hide-sm">ID</th><th>名称</th><th class="hide-sm">接口地址</th><th>状态</th><th>影片数</th>
                <th class="hide-sm">上次采集</th><th class="hide-sm">游标 / 结果</th><th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="s in sources" :key="s.id">
                <td class="mono hide-sm">{{ s.id }}</td>
                <td class="host">
                  {{ s.name }}
                  <div v-if="s.parseApi || s.playDomain || s.picApi || s.picDomain" class="tiny muted" :title="[s.parseApi && ('解析：' + s.parseApi), s.playDomain && ('播放域名：' + s.playDomain), s.picApi && ('图片代理：' + s.picApi), s.picDomain && ('图片域名：' + s.picDomain)].filter(Boolean).join(' / ')">
                    {{ [s.parseApi && '解析播放', s.playDomain && '播放域名替换', (s.picApi || s.picDomain) && '图片改写'].filter(Boolean).join(' · ') }}
                  </div>
                </td>
                <td class="url hide-sm" :title="s.apiBase">{{ short(s.apiBase, 38) }}</td>
                <td>
                  <span class="badge" :class="s.enabled ? 'on' : 'off'">{{ s.enabled ? '启用' : '停用' }}</span>
                </td>
                <td class="mono" title="已入库影片总数：采集运行中按进度落库间隔（默认每分钟）刷新，任务结束时再刷新一次">{{ s.videoCount }}</td>
                <td class="muted hide-sm">{{ formatTime(s.lastSyncAt) }}</td>
                <td class="muted hide-sm">
                  <span class="badge" :class="s.lastStatus">{{ statusLabel(s.lastStatus) }}</span>
                  <div class="mono tiny">{{ s.lastCursor || '-' }}</div>
                </td>
                <td class="ops">
                  <template v-if="activeTaskOf(s.id)">
                    <span class="muted tiny">{{ statusLabel(activeTaskOf(s.id).status) }}…</span>
                    <button class="mini danger" @click="pauseTask(activeTaskOf(s.id).jobId)">暂停</button>
                  </template>
                  <template v-else>
                    <button class="mini" @click="startTask(s, 'full')">全量</button>
                    <button class="mini ghost" @click="startTask(s, 'incremental')">增量</button>
                  </template>
                  <template v-if="isAdmin">
                    <button class="mini ghost" @click="editSource(s)">编辑</button>
                    <button class="mini ghost" @click="toggleSource(s)">{{ s.enabled ? '停用' : '启用' }}</button>
                    <button class="mini danger" @click="removeSource(s)">删除</button>
                  </template>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </template>

    <!-- 视频分类 -->
    <template v-if="tab === 'categories'">
      <section class="card">
        <div class="list-head">
          <h2>视频分类</h2>
          <span class="muted">
            {{ categoryForest.length }} 个站点 · {{ catTotal }} 个分类（支持二级）
            <button class="mini ghost" @click="toggleAllCats">展开 / 收起</button>
          </span>
        </div>
        <p class="hint">采集时按接口返回的 type_id / type_name 自动创建分类并关联影片，每个资源站各自一套；依据苹果CMS 的 type_pid 模型支持二级分类：视频的 <code>type_id_1</code> 即所属一级分类 ID，二级分类会挂到对应一级分类之下。点「查看影片」可按分类（含其子分类）筛选影片。</p>
        <p v-if="!categoryForest.length" class="empty">还没有分类，执行一次采集后会自动生成。</p>
        <div v-else class="tree">
          <div v-for="site in categoryForest" :key="site.key" class="tree-site">
            <div class="tree-row site" @click="toggleCat(site.key)">
              <span class="twisty" :class="{ open: !isCatCollapsed(site.key) }">▶</span>
              <b class="tree-title">{{ site.name }}</b>
              <span class="muted">{{ site.catCount }} 个分类 · {{ site.videoCount }} 部影片</span>
            </div>
            <div v-show="!isCatCollapsed(site.key)" class="tree-kids">
              <div v-for="node in site.children" :key="node.key || node.id" class="tree-branch">
                <div class="tree-row" :class="{ group: node.virtual }">
                  <span
                    class="twisty"
                    :class="{ open: !isCatCollapsed(catKey(node)), empty: !(node.children && node.children.length) }"
                    @click.stop="node.children && node.children.length && toggleCat(catKey(node))"
                  >▶</span>
                  <span class="badge" :class="node.virtual ? 'queued' : (node.level === 1 ? 'on' : 'off')">{{ node.virtual ? '分组' : (node.level === 1 ? '一级' : '二级') }}</span>
                  <b class="tree-title">{{ node.name }}</b>
                  <span v-if="!node.virtual" class="muted hide-sm">站点ID {{ node.typeId || '-' }}</span>
                  <span v-if="node.virtual" class="muted">{{ node.hint }}</span>
                  <span class="muted">{{ node.videoCount }} 部</span>
                  <span class="tree-gap"></span>
                  <button v-if="!node.virtual" class="mini ghost" @click="showCategoryVideos(node)">查看影片</button>
                </div>
                <div v-if="node.children && node.children.length" v-show="!isCatCollapsed(catKey(node))" class="tree-kids">
                  <div v-for="ch in node.children" :key="ch.id" class="tree-row leaf">
                    <span class="twisty empty"></span>
                    <span class="badge off">二级</span>
                    <b class="tree-title">{{ ch.name }}</b>
                    <span class="muted hide-sm">站点ID {{ ch.typeId || '-' }}</span>
                    <span class="muted">{{ ch.videoCount }} 部</span>
                    <span class="tree-gap"></span>
                    <button class="mini ghost" @click="showCategoryVideos(ch)">查看影片</button>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </section>
    </template>

    <!-- 资源库 -->
    <template v-if="tab === 'videos'">
      <section class="card">
        <div class="toolbar">
          <input v-model="videoQuery" placeholder="按视频名称 / 别名 / 演员 / 导演搜索" @keyup.enter="searchVideos" />
          <select v-model="videoCategory" @change="searchVideos" title="按分类筛选">
            <option value="">全部分类</option>
            <option v-for="c in categories" :key="c.id" :value="c.id">{{ catLabel(c) }}</option>
          </select>
          <select v-model="videoTag" @change="searchVideos" title="按标签筛选">
            <option value="">全部标签</option>
            <option v-for="t in tags" :key="t.id" :value="t.id">{{ siteNameOf(t.sourceId) }} · {{ t.name }}</option>
          </select>
          <select v-model="videoArea" @change="searchVideos" title="按地域筛选">
            <option value="">全部地域</option>
            <option v-for="a in areas" :key="a" :value="a">{{ a }}</option>
          </select>
          <select v-model="videoSource" @change="searchVideos" title="按站点筛选">
            <option value="">全部站点</option>
            <option v-for="s in sources" :key="s.id" :value="s.id">{{ s.name }}</option>
          </select>
          <select v-model="videoSort" @change="searchVideos" title="排序">
            <option value="time_desc">更新时间 ↓</option>
            <option value="time_asc">更新时间 ↑</option>
            <option value="score_desc">评分 ↓</option>
            <option value="score_asc">评分 ↑</option>
          </select>
          <button @click="searchVideos">搜索</button>
          <span class="muted">共 {{ videoTotal }} 条</span>
        </div>
        <p v-if="!videos.length" class="empty">{{ isAdmin ? '暂无采集数据，可到「站点配置」执行一次全量采集。' : '暂无可见的采集数据。' }}</p>
        <div v-else class="res-grid">
          <article v-for="v in videos" :key="v.id" class="res-card" @click="openDetail(v)">
            <div class="res-poster">
              <img v-if="posterOf(v)" :src="posterOf(v)" :alt="v.vodName" loading="lazy" @error="markPicFailed(v)" />
              <div v-else class="res-poster-ph">{{ (v.vodName || '#').slice(0, 1) }}</div>
              <span v-if="scoreOf(v)" class="res-score">{{ scoreOf(v) }}</span>
              <span v-if="episodeLabel(v) !== '-'" class="res-eps">{{ episodeLabel(v) }}</span>
            </div>
            <div class="res-info">
              <h4 :title="v.vodName">{{ v.vodName || v.vodId }}</h4>
              <p v-if="metaLine(v)" class="res-meta">{{ metaLine(v) }}</p>
              <div v-if="tagListOf(v).length" class="res-tags">
                <span v-for="t in tagListOf(v).slice(0, 4)" :key="t" class="res-tag" :title="`按标签「${t}」筛选`" @click.stop="filterByTag(t)">{{ t }}</span>
              </div>
              <p class="res-time">{{ v.vodTime || formatTime(v.firstSeenAt) }}</p>
            </div>
          </article>
        </div>
        <div v-if="videoPages > 1" class="pager">
          <button class="ghost" :disabled="videoPage <= 1" @click="gotoPage(videoPage - 1)">上一页</button>
          <span>{{ videoPage }} / {{ videoPages }}</span>
          <button class="ghost" :disabled="videoPage >= videoPages" @click="gotoPage(videoPage + 1)">下一页</button>
        </div>
      </section>
    </template>

    <!-- 采集记录 -->
    <template v-if="tab === 'jobs'">
      <section class="card">
        <div class="list-head"><h2>采集记录</h2><span class="muted">最近 50 条</span></div>
        <p v-if="!jobs.length" class="empty">暂无采集记录。</p>
        <div v-else class="table-wrap">
          <table>
            <thead>
              <tr><th>ID</th><th>站点</th><th class="hide-sm">模式</th><th>状态</th><th>页数</th><th>拉取</th><th>新增</th><th>更新</th><th>未变</th><th class="hide-sm">开始时间</th><th class="hide-sm">说明</th><th>操作</th></tr>
            </thead>
            <tbody>
              <tr v-for="j in jobs" :key="j.id">
                <td class="mono">
                  {{ j.id }}
                  <div v-if="j.retryOf" class="tiny">源自 #{{ j.retryOf }}</div>
                </td>
                <td class="host">{{ j.sourceName }}</td>
                <td class="hide-sm">{{ modeLabel(j.mode) }}</td>
                <td>
                  <span class="badge" :class="j.status">{{ j.live ? taskStatusText(j.live) : statusLabel(j.status) }}</span>
                  <div v-if="stat(j, 'attempt') > 1" class="tiny">尝试 {{ stat(j, 'attempt') }} 次</div>
                </td>
                <td class="mono">
                  <template v-if="j.live">
                    <div class="prog-track sm" :class="{ indet: isIndeterminate(j.live) }">
                      <i :style="{ width: barWidth(j.live) }"></i>
                    </div>
                    <div class="tiny">
                      {{ j.live.pageCount > 0 ? j.live.page + '/' + j.live.pageCount : j.live.pages }} 页
                      · {{ j.live.pageCount > 0 ? Math.floor(j.live.percent) + '%' : '进行中' }}
                    </div>
                  </template>
                  <template v-else>{{ j.pages }}</template>
                </td>
                <td class="mono">{{ stat(j, 'fetched') }}</td>
                <td class="mono up">{{ stat(j, 'new') }}</td>
                <td class="mono">{{ stat(j, 'updated') }}</td>
                <td class="mono muted">{{ stat(j, 'unchanged') }}</td>
                <td class="muted hide-sm">{{ formatTime(j.startedAt) }}</td>
                <td class="muted msg hide-sm" :title="jobMessage(j)">{{ short(jobMessage(j), 30) }}</td>
                <td class="ops">
                  <template v-if="j.status === 'running' || j.status === 'queued'">
                    <button class="mini danger" @click="pauseTask(j.id)">暂停</button>
                  </template>
                  <template v-else>
                    <button v-if="j.status === 'paused'" class="mini" @click="resumeTask(j.id)">继续</button>
                    <button v-if="j.status === 'failed'" class="mini" @click="retryTask(j.id)">重试</button>
                    <button class="mini ghost" @click="rerunTask(j.id)">重新运行</button>
                  </template>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </template>

    <!-- 用户管理（管理员） -->
    <template v-if="tab === 'users' && isAdmin">
      <section class="card">
        <h2>{{ userEditing ? `编辑用户「${userForm.username}」` : '新增用户' }}</h2>
        <form class="user-form" @submit.prevent="saveUser">
          <input v-model="userForm.username" placeholder="用户名 *" :disabled="userEditing" :required="!userEditing" />
          <input v-model="userForm.password" type="password"
                 :placeholder="userEditing ? '新密码（留空不改）' : '密码 *（至少 6 位）'" :required="!userEditing" />
          <label class="field">角色
            <select v-model="userForm.role">
              <option value="user">普通用户</option>
              <option value="admin">管理员</option>
            </select>
          </label>
          <label class="check"><input v-model="userForm.enabled" type="checkbox" /> 启用</label>
          <button type="submit">{{ userEditing ? '保存修改' : '创建用户' }}</button>
          <button v-if="userEditing" type="button" class="ghost" @click="resetUserForm">取消</button>
          <div class="source-grants">
            <span class="muted">可见站点授权</span>
            <span class="hint">普通用户只能看见被勾选的站点；管理员默认可见全部，无需授权。</span>
            <div class="grant-list">
              <label v-for="s in sources" :key="s.id" class="check">
                <input type="checkbox" :value="s.id" v-model="userForm.sourceIds" /> {{ s.name }}
              </label>
              <span v-if="!sources.length" class="muted">还没有站点可分配</span>
            </div>
          </div>
        </form>
        <p class="hint">创建/修改后立即生效，无需用户重新登录。停用、改密或变更角色会使该用户的现有会话失效。</p>
      </section>

      <section class="card">
        <div class="list-head"><h2>用户列表</h2><span class="muted">共 {{ users.length }} 个账号</span></div>
        <p v-if="!users.length" class="empty">还没有其他账号。</p>
        <div v-else class="table-wrap">
          <table>
            <thead>
              <tr><th>ID</th><th>用户名</th><th>角色</th><th>状态</th><th>可见站点</th><th class="hide-sm">最后登录</th><th>操作</th></tr>
            </thead>
            <tbody>
              <tr v-for="u in users" :key="u.id">
                <td class="mono">{{ u.id }}</td>
                <td class="host">{{ u.username }}<span v-if="u.id === me.user?.id" class="tiny muted">（我）</span></td>
                <td><span class="badge" :class="u.role === 'admin' ? 'on' : 'off'">{{ u.role === 'admin' ? '管理员' : '普通用户' }}</span></td>
                <td><span class="badge" :class="u.enabled ? 'on' : 'off'">{{ u.enabled ? '启用' : '停用' }}</span></td>
                <td class="muted">{{ visibleLabel(u) }}</td>
                <td class="muted hide-sm">{{ formatTime(u.lastLoginAt) }}</td>
                <td class="ops">
                  <button class="mini ghost" @click="editUser(u)">编辑</button>
                  <button class="mini ghost" @click="toggleUser(u)">{{ u.enabled ? '停用' : '启用' }}</button>
                  <button class="mini danger" @click="removeUser(u)">删除</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </template>

    <!-- 站点配置导入确认（仅管理员） -->
    <div v-if="importOpen" class="modal-mask" @click.self="closeImport">
      <div class="modal narrow">
        <div class="modal-head">
          <div>
            <h3>导入站点配置</h3>
            <p class="muted">{{ importFileName }} · 共 {{ importPreview.length }} 条配置</p>
          </div>
          <button class="ghost" @click="closeImport">关闭</button>
        </div>
        <div class="modal-body">
          <p class="hint">按<strong>站点名称</strong>匹配：同名配置会被更新（保留站点 ID、采集状态与增量游标），不同名的新建，因此重复导入不会产生重复站点。单条配置校验失败不影响其它条目。支持导出文件、站点数组、单条站点对象三种 JSON。</p>
          <div class="import-list">
            <div v-for="(s, i) in importPreviewShown" :key="i" class="import-row">
              <span class="mono muted">#{{ i + 1 }}</span>
              <b>{{ s.name || '（未命名）' }}</b>
              <span class="muted">{{ s.apiBase || '（缺少接口地址）' }}</span>
            </div>
            <div v-if="importPreview.length > importPreviewShown.length" class="import-row muted">…… 还有 {{ importPreview.length - importPreviewShown.length }} 条未列出</div>
          </div>
        </div>
        <div class="modal-actions">
          <button :disabled="importBusy" @click="confirmImport">{{ importBusy ? '导入中…' : '确认导入' }}</button>
          <button class="ghost" @click="closeImport">取消</button>
        </div>
      </div>
    </div>

    <!-- 站点配置弹窗（新增 / 编辑共用，仅管理员） -->
    <div v-if="sourceOpen && isAdmin" class="modal-mask" @click.self="closeSourceForm">
      <div class="modal narrow">
        <div class="modal-head">
          <div>
            <h3>{{ editing ? '编辑站点' : '新增采集站点' }}</h3>
            <p class="muted">苹果CMS V10 Provide 接口：<code>api.php/provide/vod/?ac=videolist</code></p>
          </div>
          <button class="ghost" @click="closeSourceForm">关闭</button>
        </div>
        <form class="modal-body sf-form" @submit.prevent="saveSource">
          <label class="sf-item">
            <span>站点名称 *</span>
            <input v-model="form.name" placeholder="例：某某资源站" required />
          </label>
          <label class="sf-item">
            <span>附加参数</span>
            <input v-model="form.extraParams" placeholder="如 t=1&amp;wd=电影（可留空）" />
          </label>
          <label class="sf-item full">
            <span>JSON 接口地址 *</span>
            <input v-model="form.apiBase" placeholder="https://site.com/api.php/provide/vod/" required />
          </label>

          <p class="sf-group full">播放增强<span class="muted">只影响播放时的地址，不改动入库的原始地址</span></p>
          <label class="sf-item full">
            <span>视频解析地址</span>
            <input v-model="form.parseApi" placeholder="https://jx.example.com/?url=（可留空，支持 {url} 占位符）" />
          </label>
          <label class="sf-item full">
            <span>视频播放域名</span>
            <input v-model="form.playDomain" placeholder="https://cdn.example.com（可留空，播放时替换原地址域名）" />
          </label>

          <p class="sf-group full">图片增强<span class="muted">封面等图片地址，同样只影响展示、不改动入库数据</span></p>
          <label class="sf-item full">
            <span>图片解析 / 代理地址</span>
            <input v-model="form.picApi" placeholder="https://img.example.com/proxy?url=（可留空，拼在图片地址前，支持 {url}）" />
          </label>
          <label class="sf-item full">
            <span>图片域名</span>
            <input v-model="form.picDomain" placeholder="https://img.example.com（可留空，展示时替换原图片域名）" />
          </label>

          <label class="sf-item">
            <span>增量小时数</span>
            <input v-model.number="form.incrementalHours" type="number" min="0" />
          </label>
          <label class="sf-item">
            <span>最大页数（0 = 全局配置）</span>
            <input v-model.number="form.maxPages" type="number" min="0" />
          </label>
          <label class="check full"><input v-model="form.enabled" type="checkbox" /> 启用该站点</label>

          <p class="hint full">
            增量采集附加 <code>h=&lt;小时数&gt;</code> 并按 <code>vod_time</code> 游标去重；接口返回的<strong>全部标准字段</strong>结构化入库，站点自定义字段存入 <code>extra</code>，不丢字段。<br />
            地址改写统一<strong>先换域名、再拼解析/代理地址</strong>：解析地址含 <code>{url}</code> 时按占位符插入，未含时直接前缀拼接；图片改写还作用于 <code>#</code> 分隔的截图集合。
          </p>
          <div class="sf-actions full">
            <button type="submit">{{ editing ? '保存修改' : '添加站点' }}</button>
            <button type="button" class="ghost" @click="closeSourceForm">取消</button>
          </div>
          <p v-if="error" class="error full">{{ error }}</p>
        </form>
      </div>
    </div>

    <!-- 资源详情 -->
    <div v-if="detail" class="modal-mask" @click.self="closeDetail">
      <div class="modal">
        <div class="modal-head">
          <div>
            <h3>{{ detail.vodName || detail.vodId }}</h3>
            <p class="muted">
              <span v-if="metaLine(detail)">{{ metaLine(detail) }} · </span>
              更新：{{ episodeLabel(detail) }} · {{ detail.vodTime || '-' }}
              <span v-if="scoreOf(detail)"> · 评分 {{ scoreOf(detail) }}</span>
              <span v-if="detail.vodRemarks && detail.vodRemarks !== episodeLabel(detail)"> · {{ detail.vodRemarks }}</span>
            </p>
            <div v-if="tagListOf(detail).length" class="res-tags detail-tags">
              <span v-for="t in tagListOf(detail)" :key="t" class="res-tag" :title="`按标签「${t}」筛选`" @click="closeDetail(); filterByTag(t)">{{ t }}</span>
            </div>
          </div>
          <button class="ghost" @click="closeDetail">关闭</button>
        </div>
        <div class="modal-tabs">
          <button :class="{ active: detailTab === 'play' }" @click="switchDetailTab('play')">播放组 ({{ playGroups.length }})</button>
          <button :class="{ active: detailTab === 'fields' }" @click="switchDetailTab('fields')">标准字段</button>
          <button :class="{ active: detailTab === 'extra' }" @click="switchDetailTab('extra')">扩展字段 ({{ extraPairs.length }})</button>
          <label v-if="detailTab === 'fields'" class="check"><input v-model="onlyNonEmpty" type="checkbox" /> 只看非空</label>
        </div>
        <div class="modal-body">
          <template v-if="detailTab === 'play'">
            <div class="player-wrap">
              <video v-show="!useFrame" ref="playerEl" class="player" controls playsinline preload="metadata"
                     @error="onVideoError" @loadeddata="onVideoLoaded"></video>
              <iframe v-if="useFrame" :key="currentUrl" class="player-frame" :src="currentUrl"
                      allow="autoplay; fullscreen; encrypted-media" allowfullscreen
                      referrerpolicy="no-referrer"></iframe>
              <p v-if="!playGroups.length" class="empty">该影片没有播放地址（vod_play_url 为空）。</p>
              <p v-else-if="useFrame" class="hint">
                已按站点配置拼接<strong>视频解析地址</strong>，下面用解析页面播放；若解析站要求手动播放，
                可在<strong>解析页面内</strong>点击播放。
                <a v-if="currentUrl" :href="currentUrl" target="_blank" rel="noopener" class="ep-url">在新窗口打开</a>
              </p>
              <p v-else-if="!currentUrl" class="hint">
                当前线路这一集没有播放地址。
              </p>
              <p v-else class="hint">m3u8 优先用 hls.js 播放，mp4 / webm 走直链；无后缀地址会先按 HLS 试播再回退直链。切换线路或集数即时切换。</p>
              <p v-if="playError" class="play-error">
                {{ playError }}
                <a v-if="currentUrl" :href="currentUrl" target="_blank" rel="noopener" class="ep-url">在新窗口打开</a>
              </p>
            </div>
            <template v-if="playGroups.length">
              <div class="group-tabs">
                <button
                  v-for="(g, gi) in playGroups" :key="gi"
                  class="mini" :class="gi === activeGroup ? '' : 'ghost'"
                  :title="g.note || ''"
                  @click="chooseGroup(gi)"
                >{{ g.from || ('线路' + (gi + 1)) }} ({{ g.episodes?.length || 0 }})</button>
              </div>
              <div class="ep-chips">
                <button
                  v-for="(ep, ei) in currentEpisodes" :key="ei"
                  class="mini" :class="ei === activeEp ? '' : 'ghost'"
                  @click="chooseEp(ei)"
                >{{ ep.name || ('第' + (ei + 1) + '集') }}</button>
                <span v-if="!currentEpisodes.length" class="muted">该线路暂无剧集。</span>
              </div>
              <p v-if="currentEpisode?.url" class="play-url">
                <span v-if="detailSource?.playDomain" class="badge on">播放域名已替换</span>
                <span v-if="parseEnabled" class="badge on">已拼接解析地址</span>
                {{ currentEpisode.url }}
              </p>
            </template>
          </template>
          <template v-else-if="detailTab === 'fields'">
            <section v-for="g in groupedFields" :key="g.key" class="field-group">
              <h4>{{ g.label }} <span class="muted">{{ g.items.length }} 项</span></h4>
              <div class="field-grid">
                <div v-for="f in g.items" :key="f.key" class="field-item" :class="{ long: f.long }">
                  <div class="field-k"><code>{{ f.key }}</code> {{ f.desc }}</div>
                  <div class="field-v" :title="String(detail[f.json] ?? '')">{{ detail[f.json] ?? '-' }}</div>
                </div>
              </div>
            </section>
            <p v-if="!groupedFields.length" class="empty">没有可显示的字段。</p>
          </template>
          <template v-else-if="detailTab === 'extra'">
            <p class="hint">接口返回但不在标准字段清单内的字段（站点自定义字段、嵌套对象、数组等），按原始 JSON 值保存，确保不丢失。</p>
            <p v-if="!extraPairs.length" class="empty">该站点未返回扩展字段。</p>
            <table v-else class="kv-table">
              <tbody><tr v-for="[k, val] in extraPairs" :key="k"><th><code>{{ k }}</code></th><td>{{ val }}</td></tr></tbody>
            </table>
          </template>
        </div>
      </div>
    </div>
    <!-- 修改密码 -->
    <div v-if="pwdModal" class="modal-mask" @click.self="pwdModal = false">
      <div class="modal sm">
        <div class="modal-head">
          <h3>修改密码</h3>
          <button class="ghost" @click="pwdModal = false">关闭</button>
        </div>
        <div class="modal-body">
          <form class="user-form" @submit.prevent="doChangePassword">
            <input v-model="pwdForm.oldPassword" type="password" placeholder="原密码" required />
            <input v-model="pwdForm.newPassword" type="password" placeholder="新密码（至少 6 位）" required />
            <button type="submit">确认修改</button>
          </form>
          <p class="hint">修改后该账号的其他登录会话会立即失效，当前会话会自动换发新令牌。</p>
        </div>
      </div>
    </div>
  </main>
  </template>
</template>

<style>
:root { color-scheme: light; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif; }
* { box-sizing: border-box; }
body { margin: 0; background: #f5f6f8; color: #1f2329; }
.page { max-width: 1180px; margin: 0 auto; padding: 28px 20px 64px; }
header h1 { margin: 0; font-size: 28px; }
header p { margin: 6px 0 12px; color: #646a73; }
.stats { display: flex; gap: 10px; flex-wrap: wrap; align-items: center; }
.db { display: inline-block; background: #eef1f5; padding: 4px 10px; border-radius: 6px; font-size: 12px; color: #4e5969; }
.stat { background: #eef1f5; padding: 4px 10px; border-radius: 6px; font-size: 12px; color: #4e5969; }
.stat b { color: #165dff; }
.tabs { display: flex; gap: 8px; margin: 20px 0 0; border-bottom: 1px solid #e5e6eb; padding-bottom: 10px; }
.tabs button { background: #f2f3f5; color: #4e5969; }
.tabs button.active { background: #165dff; color: #fff; }
.tabs .refresh { margin-left: auto; }
.card { background: #fff; border: 1px solid #e5e6eb; border-radius: 10px; padding: 20px; margin-top: 18px; box-shadow: 0 1px 2px rgba(0,0,0,.03); }
.card h2 { margin: 0 0 14px; font-size: 16px; }
.list-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 12px; }
.list-head h2 { margin: 0; }
.actions, .toolbar { display: flex; gap: 10px; flex-wrap: wrap; align-items: center; }
form, .source-form { display: flex; gap: 10px; flex-wrap: wrap; align-items: center; }
input, select { padding: 8px 10px; border: 1px solid #c9cdd4; border-radius: 6px; font-size: 14px; background: #fff; }
input { flex: 1 1 190px; min-width: 0; }
input.wide { flex: 2 1 380px; }
.field { display: flex; align-items: center; gap: 6px; font-size: 13px; color: #4e5969; }
.field input { width: 90px; flex: none; }
.check { display: flex; align-items: center; gap: 6px; font-size: 13px; color: #4e5969; }
.check input { flex: none; }
button { padding: 8px 16px; border: 0; border-radius: 6px; background: #165dff; color: #fff; cursor: pointer; font-size: 14px; white-space: nowrap; }
button:disabled { opacity: .55; cursor: default; }
button.ghost { background: #f2f3f5; color: #1f2329; }
button.mini { padding: 5px 9px; font-size: 12px; }
button.danger { background: #fff1f0; color: #f53f3f; }
.hint { margin: 12px 0 0; font-size: 12px; color: #86909c; line-height: 1.8; }
code { background: #f2f3f5; padding: 1px 5px; border-radius: 4px; }
.table-wrap { overflow-x: auto; }
table { width: 100%; border-collapse: collapse; font-size: 13px; }
th, td { text-align: left; padding: 10px 8px; border-bottom: 1px solid #f0f1f3; white-space: nowrap; }
th { color: #86909c; font-weight: 500; }
.host { font-weight: 600; }
.url, .msg { max-width: 260px; overflow: hidden; text-overflow: ellipsis; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 12px; }
.tiny { font-size: 11px; margin-top: 3px; color: #86909c; }
.muted { color: #86909c; }
.mono.up { color: #00b42a; font-weight: 600; }
.badge { display: inline-block; padding: 2px 8px; border-radius: 4px; font-size: 12px; background: #f2f3f5; color: #4e5969; }
.badge.on, .badge.success { background: #e8ffea; color: #00b42a; }
.badge.off { background: #f2f3f5; color: #86909c; }
.badge.failed { background: #fff1f0; color: #f53f3f; }
.badge.running { background: #e8f3ff; color: #165dff; }
.badge.queued { background: #f5f0ff; color: #722ed1; }
.badge.paused { background: #fff7e8; color: #d25f00; }
.ops { display: flex; gap: 5px; }
/* 实时进度条（任务列表 / 采集记录） */
.prog-col { min-width: 220px; white-space: normal; }
.prog-track { position: relative; height: 6px; border-radius: 4px; background: #eef1f5; overflow: hidden; }
.prog-track.sm { height: 4px; margin-bottom: 3px; }
.prog-track i { display: block; height: 100%; width: 0; background: #165dff; border-radius: 4px; transition: width .4s ease; }
.prog-track.indet i {
  background: linear-gradient(90deg, #165dff 25%, #a3c4ff 50%, #165dff 75%);
  background-size: 200% 100%;
  animation: prog-indet 1.2s linear infinite;
}
@keyframes prog-indet { from { background-position: 120% 0; } to { background-position: -80% 0; } }
.prog-meta { display: flex; flex-wrap: wrap; gap: 8px; align-items: baseline; margin-top: 5px; font-size: 11px; color: #86909c; }
.prog-meta b { color: #1f2329; font-size: 12px; }
.tiny.msg { max-width: 340px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pager { display: flex; align-items: center; justify-content: center; gap: 14px; margin-top: 16px; font-size: 13px; color: #4e5969; }
.empty { color: #86909c; margin: 8px 0; }
.error { background: #fff1f0; color: #f53f3f; padding: 10px 14px; border-radius: 6px; margin-top: 16px; }
.notice { padding: 10px 14px; border-radius: 6px; margin-top: 16px; background: #e8ffea; color: #009a29; font-size: 13px; }
.notice.err { background: #fff1f0; color: #f53f3f; }
.notice.info { background: #e8f3ff; color: #165dff; }
.modal-mask { position: fixed; inset: 0; background: rgba(0,0,0,.45); display: flex; align-items: center; justify-content: center; padding: 24px; z-index: 50; }
.modal { background: #fff; border-radius: 12px; width: min(960px, 100%); max-height: 88vh; display: flex; flex-direction: column; box-shadow: 0 12px 40px rgba(0,0,0,.2); }
.modal-head { display: flex; justify-content: space-between; align-items: flex-start; gap: 16px; padding: 18px 22px; border-bottom: 1px solid #e5e6eb; }
.modal-head h3 { margin: 0 0 6px; font-size: 17px; }
.modal-head p { margin: 0; font-size: 12px; }
.modal-tabs { display: flex; gap: 8px; align-items: center; padding: 12px 22px; border-bottom: 1px solid #e5e6eb; flex-wrap: wrap; }
.modal-tabs button { padding: 5px 12px; background: #f2f3f5; color: #4e5969; font-size: 13px; }
.modal-tabs button.active { background: #165dff; color: #fff; }
.modal-tabs .check { margin-left: auto; }
.modal-body { overflow-y: auto; padding: 6px 22px 22px; }
/* 站点配置导入 / 导出 */
.hidden-file { display: none; }
.import-ops { flex-wrap: wrap; justify-content: flex-end; align-items: center; }
.import-list { max-height: 300px; overflow-y: auto; border: 1px solid #e5e6eb; border-radius: 6px; }
.import-row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; padding: 8px 12px; font-size: 13px; border-bottom: 1px solid #f0f1f3; }
.import-row:last-child { border-bottom: 0; }
.import-row b { font-size: 13px; }
.modal-actions { display: flex; justify-content: flex-end; gap: 10px; padding: 14px 22px 20px; border-top: 1px solid #e5e6eb; }
.list-head-l { display: flex; align-items: baseline; gap: 10px; }
.list-head-l h2 { margin: 0; }
/* 站点配置弹窗表单 */
.modal.narrow { width: min(720px, 100%); }
.sf-form { display: grid; grid-template-columns: 1fr 1fr; align-items: start; gap: 12px 18px; padding: 16px 22px 22px; }
.sf-item { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
.sf-item > span { font-size: 12px; color: #4e5969; }
.sf-item input { width: 100%; flex: none; }
.sf-form .full { grid-column: 1 / -1; }
.sf-group {
  display: flex; align-items: baseline; gap: 8px; margin: 6px 0 0; padding-top: 10px;
  border-top: 1px dashed #e5e6eb; font-size: 13px; font-weight: 600; color: #165dff;
}
.sf-group .muted { font-weight: 400; font-size: 12px; }
.sf-actions { display: flex; gap: 10px; justify-content: flex-end; }
.sf-form .hint { margin: 2px 0 0; }
.field-group { margin-top: 16px; }
.field-group h4 { margin: 0 0 10px; font-size: 14px; color: #165dff; }
.field-group h4 span { font-weight: 400; font-size: 12px; }
.field-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(270px, 1fr)); gap: 8px 16px; }
.field-item { border-bottom: 1px dashed #f0f1f3; padding-bottom: 6px; min-width: 0; }
.field-item.long { grid-column: 1 / -1; }
.ep-list { margin: 6px 0 0; padding-left: 20px; }
.ep-list li { padding: 4px 0; border-bottom: 1px dashed #f0f1f3; }
.ep-name { display: inline-block; min-width: 110px; font-weight: 600; color: #1f2329; }
.ep-url { color: #165dff; text-decoration: none; word-break: break-all; }
.ep-url:hover { text-decoration: underline; }
.badge.info { background: #e8f3ff; color: #165dff; }
.field-k { font-size: 11px; color: #86909c; margin-bottom: 2px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.field-v { font-size: 13px; word-break: break-all; white-space: pre-wrap; max-height: 66px; overflow: hidden; }
.field-item.long .field-v { max-height: 130px; }
.kv-table { width: 100%; }
.kv-table th { width: 240px; text-align: left; }
.kv-table td { white-space: pre-wrap; word-break: break-all; }
/* 资源卡片（名称 / 图片 / 更新集数） */
.res-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(160px, 1fr)); gap: 16px; margin-top: 16px; }
.res-card { border: 1px solid #e5e6eb; border-radius: 10px; overflow: hidden; background: #fff; cursor: pointer; transition: box-shadow .15s ease, transform .15s ease; }
.res-card:hover { box-shadow: 0 6px 18px rgba(0,0,0,.1); transform: translateY(-2px); }
.res-poster { position: relative; aspect-ratio: 2 / 3; background: #f2f3f5; }
.res-poster img { width: 100%; height: 100%; object-fit: cover; display: block; }
.res-poster-ph { width: 100%; height: 100%; display: flex; align-items: center; justify-content: center; font-size: 40px; color: #c9cdd4; background: linear-gradient(135deg, #f2f3f5, #e5e6eb); }
.res-score { position: absolute; left: 6px; top: 6px; background: rgba(0,0,0,.66); color: #ffb400; font-size: 12px; font-weight: 600; padding: 2px 6px; border-radius: 4px; }
.res-eps { position: absolute; right: 6px; bottom: 6px; left: 6px; background: rgba(0,0,0,.66); color: #fff; font-size: 11px; padding: 2px 6px; border-radius: 4px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; text-align: center; }
.res-info { padding: 8px 10px 10px; }
.res-info h4 { margin: 0 0 4px; font-size: 13px; font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.res-meta { margin: 0 0 3px; font-size: 11px; color: #86909c; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.res-tags { display: flex; flex-wrap: wrap; gap: 4px; margin: 0 0 3px; }
.res-tag { font-size: 10px; line-height: 1; padding: 3px 6px; border-radius: 999px; background: #eef4ff; color: #2f6df6; cursor: pointer; white-space: nowrap; }
.res-tag:hover { background: #dde9ff; }
.res-time { margin: 0; font-size: 11px; color: #a9aeb8; }
.detail-tags { margin-top: 6px; }
/* 详情播放器与线路 / 剧集选择 */
.player-wrap { margin-top: 4px; }
.player { width: 100%; max-height: 420px; background: #000; border-radius: 8px; display: block; }
.player-frame { width: 100%; aspect-ratio: 16 / 9; max-height: 420px; border: 0; border-radius: 8px; background: #000; display: block; }
.group-tabs, .ep-chips { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 12px; }
.ep-chips { max-height: 168px; overflow-y: auto; padding: 2px; }
.play-url { margin: 10px 0 0; font-size: 11px; color: #86909c; word-break: break-all; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
.play-error { margin: 10px 0 0; padding: 9px 12px; border: 1px solid #ffb3b3; background: #fff2f0; border-radius: 6px; font-size: 12px; color: #cb2634; line-height: 1.7; word-break: break-all; }
/* 分类树：资源站点 → 一级分类 → 二级分类 */
.tree { display: flex; flex-direction: column; gap: 12px; }
.tree-site { border: 1px solid #e5e6eb; border-radius: 8px; overflow: hidden; }
.tree-row { display: flex; align-items: center; gap: 10px; padding: 10px 12px; flex-wrap: wrap; }
.tree-row.site { background: #f7f8fa; cursor: pointer; user-select: none; }
.tree-row.site:hover { background: #eef1f6; }
.tree-row.group { background: #fbfbfe; }
.tree-row.group b.tree-title { color: #4e5969; font-weight: 600; }
.tree-row b.tree-title { font-size: 14px; }
.tree-gap { flex: 1 1 0; min-width: 0; }
.twisty {
  width: 12px; flex: none; font-size: 10px; line-height: 1; color: #86909c;
  display: inline-block; transition: transform .15s ease; transform: rotate(0deg);
}
.twisty.open { transform: rotate(90deg); }
.twisty.empty { visibility: hidden; }
.tree-kids { border-top: 1px solid #f0f1f3; }
.tree-kids .tree-row { padding-left: 28px; }
.tree-kids .tree-kids { border-top: 1px dashed #eef1f5; }
.tree-kids .tree-kids .tree-row { padding-left: 46px; background: #fafbfc; }
.tree-kids .tree-kids .tree-row:hover { background: #f5f7fa; }

/* ---------- 手机端适配（≤760px） ---------- */
/* 策略：表格保留横滚、隐藏次要列；表单与工具栏单列；弹窗与播放器铺满宽度。 */
@media (max-width: 760px) {
  body { overflow-x: hidden; } /* 兜底：任何元素都不应把整页撑出横向滚动 */
  .page { padding: 16px 12px 44px; }
  header h1 { font-size: 22px; }
  header p { margin: 4px 0 10px; font-size: 13px; line-height: 1.6; }
  .db { word-break: break-all; }
  .stats { gap: 6px; }
  .stat, .db { font-size: 11px; }

  /* 顶部页签改为可横向滑动的单行，避免换行把内容挤下去 */
  .tabs {
    overflow-x: auto;
    flex-wrap: nowrap;
    -webkit-overflow-scrolling: touch;
    scrollbar-width: none;
    gap: 6px;
    margin-top: 14px;
  }
  .tabs::-webkit-scrollbar { display: none; }
  .tabs button { flex: 0 0 auto; padding: 8px 13px; font-size: 13px; }
  .tabs .refresh { margin-left: 4px; }

  .card { padding: 14px; margin-top: 14px; border-radius: 8px; }
  .card h2 { font-size: 15px; }
  /* 标题行的右侧统计在窄屏改竖排，避免 nowrap 撑宽卡片、把整页挤出屏幕 */
  .list-head { flex-direction: column; align-items: flex-start; gap: 2px; }
  .list-head span { white-space: normal; }
  .hint { font-size: 11px; line-height: 1.7; }
  .hint code { word-break: break-all; }

  /* 输入控件占满整行；16px 字号避免 iOS 聚焦时自动放大 */
  form, .source-form, .actions, .toolbar { gap: 8px; }
  input, select { flex: 1 1 100%; font-size: 16px; padding: 9px 10px; }
  input.wide { flex: 1 1 100%; }
  .field { flex: 1 1 100%; justify-content: space-between; }
  .field input { width: 120px; flex: none; }
  .check { flex: 1 1 100%; }
  form button, .source-form button { flex: 1 1 auto; padding: 9px 14px; }
  .toolbar button { flex: 1 1 auto; }
  .toolbar .muted { flex: 1 1 100%; }

  /* 表格横滚 + 隐藏次要列；再压一档内边距与字号，尽量让常用列在窄屏一次看全 */
  .table-wrap { -webkit-overflow-scrolling: touch; }
  th, td { padding: 7px 5px; font-size: 12px; }
  .mono { font-size: 11px; }
  .hide-sm { display: none; }
  .prog-col { min-width: 140px; }
  .url, .msg { max-width: 160px; }
  .ops { gap: 4px; }
  .ops button.mini { padding: 5px 7px; }

  /* 详情弹窗全屏；播放器按比例收敛高度 */
  .modal-mask { padding: 0; align-items: stretch; }
  .modal { width: 100%; height: 100%; max-height: none; border-radius: 0; }
  .modal-head { padding: 14px 16px; flex-wrap: wrap; gap: 8px; }
  .modal-head h3 { font-size: 16px; }
  .modal-tabs { padding: 10px 16px; gap: 6px; }
  .modal-tabs button { padding: 5px 10px; }
  .modal-body { padding: 4px 16px 20px; }
  .sf-form { grid-template-columns: 1fr; padding: 12px 16px 18px; gap: 10px; }
  .sf-actions { flex-wrap: wrap; justify-content: stretch; }
  .sf-actions button { flex: 1 1 auto; }
  .import-ops { justify-content: flex-start; }
  .modal-actions { padding: 12px 16px 16px; }
  .modal-actions button { flex: 1 1 auto; }
  .import-row { gap: 6px; font-size: 12px; }
  .player { max-height: 56vh; }
  .player-frame { max-height: 56vh; }
  .field-grid { grid-template-columns: 1fr; }
  .field-item.long .field-v { max-height: 180px; }
  .kv-table th { width: auto; }

  /* 资源卡片：窄屏两列 */
  .res-grid { grid-template-columns: repeat(auto-fill, minmax(138px, 1fr)); gap: 10px; }
  .res-poster-ph { font-size: 30px; }
  .res-info { padding: 7px 8px 9px; }
  .res-info h4 { font-size: 12px; }
  .res-meta, .res-time { font-size: 10px; }

  /* 分类树：收窄缩进，标题行允许换行不挤压按钮 */
  .tree-row { padding: 9px 10px; gap: 6px; }
  .tree-row b.tree-title { font-size: 13px; }
  .tree-kids .tree-row { padding-left: 20px; }
  .tree-kids .tree-kids .tree-row { padding-left: 34px; }
  .ep-name { min-width: 0; }
  .ep-list { padding-left: 16px; }
  .pager { gap: 10px; font-size: 12px; }
  .notice, .error { font-size: 12px; padding: 8px 10px; }
}

/* ---------- 登录与账号 ---------- */
.boot { min-height: 100vh; display: flex; align-items: center; justify-content: center; color: #86909c; font-size: 14px; }
.login-wrap { min-height: 100vh; display: flex; align-items: center; justify-content: center; padding: 24px; background: linear-gradient(160deg, #eef2ff 0%, #f5f6f8 60%); }
.login-card { width: min(420px, 100%); background: #fff; border: 1px solid #e5e6eb; border-radius: 14px; padding: 32px; box-shadow: 0 12px 40px rgba(31,35,41,.08); }
.login-card h1 { margin: 0; font-size: 26px; }
.login-card > p.muted { margin: 6px 0 20px; font-size: 13px; }
.login-card form { display: flex; flex-direction: column; gap: 12px; }
.login-card form input { width: 100%; flex: none; padding: 10px 12px; }
.login-card form button { width: 100%; padding: 10px; margin-top: 4px; }
.login-card .hint { margin: 16px 0 0; }
.userbar { display: flex; align-items: center; gap: 8px; margin-top: 10px; flex-wrap: wrap; }
.user-form { display: flex; gap: 10px; flex-wrap: wrap; align-items: center; }
.source-grants { flex-basis: 100%; display: flex; flex-direction: column; gap: 4px; }
.grant-list { display: flex; flex-wrap: wrap; gap: 8px 16px; margin-top: 4px; }
.modal.sm { width: min(460px, 100%); }
</style>
