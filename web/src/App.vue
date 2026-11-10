<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, provide, reactive, ref, watch } from 'vue'
import { linkedFilters, scopeOptions } from './filterLinkage.js'
import { evaluateCron, formatCronTime } from './cron.js'
import { buildPayload } from './settings.js'
import {
  NAV_ITEMS, DEFAULT_TAB, normalizeTab, tabToHash, navItemsFor,
  detailHash, detailIdFromHash,
} from './layout.js'
import SourcesView from './views/SourcesView.vue'
import CategoriesView from './views/CategoriesView.vue'
import VideosView from './views/VideosView.vue'
import SettingsView from './views/SettingsView.vue'
import VideoDetailView from './views/VideoDetailView.vue'
import JobsView from './views/JobsView.vue'
import UsersView from './views/UsersView.vue'

// 侧边栏布局：记住当前页面（URL hash 优先，localStorage 兜底）与折叠状态。
const TAB_KEY = 'assetspider_tab'
const SIDEBAR_KEY = 'assetspider_sidebar_collapsed'
function initialTab() {
  // 影片详情页地址（#/video/<id>）以资源库为底层页面，登录后按 id 恢复详情。
  if (detailIdFromHash(location.hash)) return DEFAULT_TAB
  const fromHash = normalizeTab(location.hash)
  if (fromHash) return fromHash
  // URL 显式带了但非法的 hash：忽略 localStorage，直接回退到主页面，
  // 避免一个无效链接把用户送到上一次访问的无关页面。
  if (location.hash) return DEFAULT_TAB
  return normalizeTab(localStorage.getItem(TAB_KEY)) || DEFAULT_TAB
}

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

const tab = ref(initialTab())  // 资源库为主：默认 / 回退页都是资源库
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
// 系统设置：采集运行时配置（并发上限已并入此处，任务页只做只读展示）
const settings = ref(null)
const settingsForm = ref({})
const settingsDirty = ref(false)
const settingsSaving = ref(false)
const error = ref('')
const notice = ref(null)

const videoQuery = ref('')
const videoSource = ref('')
const videoCategory = ref('')
const videoTag = ref('')
const videoAreaId = ref('')
const videoYear = ref('')
const videoSort = ref('time_desc')
const areas = ref([])
const years = ref([])
// ---------- 筛选条件双向联动 ----------
// 分类 / 标签 / 地域都归属于某个站点（sourceId），站点是筛选的主轴：
// 选了站点就只列出该站点的选项；反过来选中某个分类 / 标签 / 地域，
// 站点会自动定位过去，并清掉属于其它站点的已选条件，避免必然为空的组合。
const visibleCategories = computed(() => scopeOptions(categories.value, videoSource.value))
const visibleTags = computed(() => scopeOptions(tags.value, videoSource.value))
const visibleAreas = computed(() => scopeOptions(areas.value, videoSource.value))
const hasActiveFilters = computed(() => !!(
  videoQuery.value.trim() || videoSource.value || videoCategory.value ||
  videoTag.value || videoAreaId.value || videoYear.value
))
// 地域与标签同构：每个资源站一套，因此站点未定时下拉按站点分组（optgroup），
// 避免「美国」在多站点各出现一次时看起来像重复项；站点已定时只剩一组，直接平铺。
const areaGroups = computed(() => {
  const groups = new Map()
  for (const a of visibleAreas.value) {
    if (!groups.has(a.sourceId)) {
      groups.set(a.sourceId, { sourceId: a.sourceId, name: siteNameOf(a.sourceId), items: [] })
    }
    groups.get(a.sourceId).items.push(a)
  }
  return [...groups.values()]
})
const videoPage = ref(1)
const videoSize = ref(24)
// 列表接口只返回 items/page/size：不返回总数、也不返回 hasMore。
// 前端按「本页条数 >= 分页条数」认为还有下一页；正好整除时会多发一次空页请求为止。
const videoHasMore = ref(true)
// 资源库改成「底部无限滚动」：列表是累计的，这里记已经加载到第几页、是否还有下一页。
const videoLoading = ref(false)     // 首屏 / 搜索 / 切筛选：整表重载
const videoLoadingMore = ref(false) // 滚到底加载下一页
const videoEnd = computed(() => !videoHasMore.value)
// 请求序号：搜索词或筛选条件连续变化时，只认最后一次请求的结果，
// 避免先发后到的旧响应把新结果覆盖掉。
let videoSeq = 0

// 「可见影片」与资源库页签的计数徽标都取站点表上的影片数快照（videoCount）：
// 列表接口不返回总数，也就不需要对 videos 表做 COUNT。
// 它与「可见站点」同源，因此资源库里的搜索/分类/标签筛选不会改变这个数值。
const visibleVideoTotal = computed(() =>
  sources.value.reduce((n, s) => n + (Number(s.videoCount) || 0), 0)
)

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
// 拉取一页影片。append=true 时追加到现有列表（无限滚动），否则整表替换（搜索/筛选/刷新）。
// 接口只返回 items / page / size，是否还有下一页由本页条数与分页条数推导。
async function fetchVideos(page, append) {
  const seq = ++videoSeq
  const requestedSize = videoSize.value
  if (append) {
    videoLoadingMore.value = true
  } else {
    videoLoading.value = true
    // 新的搜索/筛选从第一页开始，顺带回到顶部，否则还停在上一次的滚动位置
    if (tab.value === 'videos') window.scrollTo(0, 0)
  }
  try {
    const p = new URLSearchParams({ page: String(page), size: String(videoSize.value) })
    if (videoQuery.value.trim()) p.set('q', videoQuery.value.trim())
    if (videoSource.value) p.set('source_id', String(videoSource.value))
    if (videoCategory.value) p.set('category_id', String(videoCategory.value))
    if (videoTag.value) p.set('tag_id', String(videoTag.value))
    if (videoAreaId.value) p.set('area_id', String(videoAreaId.value))
    if (videoYear.value) p.set('year', videoYear.value)
    if (videoSort.value) p.set('sort', videoSort.value)
    const d = await request('/api/videos/v1?' + p.toString())
    if (seq !== videoSeq) return // 已经有更新的请求在跑，丢弃这次结果
    const items = d.items || []
    videos.value = append ? videos.value.concat(items) : items
    // 本页取满就认为还有下一页；不足一页（含空页）即到底。
    videoHasMore.value = items.length >= requestedSize
    videoPage.value = d.page || page
    videoSize.value = d.size || 24
  } finally {
    if (seq === videoSeq) {
      videoLoading.value = false
      videoLoadingMore.value = false
      // 列表追加后哨兵可能仍在视口内（IntersectionObserver 不会重复触发），
      // 等 DOM 更新完再重新 observe 一次强制按当前状态回调，
      // 否则会卡在「还有下一页但不加载」。
      nextTick(kickSentinel)
    }
  }
}
function kickSentinel() {
  const el = videoSentinel.value
  if (!videoObserver || !el) return
  videoObserver.unobserve(el)
  videoObserver.observe(el)
}
// 整表重载：搜索、切换筛选、刷新、从别的页签切回来都走这里，回到第一页。
function loadVideos() {
  videoPage.value = 1
  return fetchVideos(1, false)
}
// 底部哨兵进入视口时加载下一页（已加载完或正在加载则忽略）。
function loadMoreVideos() {
  if (videoLoading.value || videoLoadingMore.value) return Promise.resolve()
  if (!videoHasMore.value) return Promise.resolve()
  return fetchVideos(videoPage.value + 1, true)
}
async function loadAreas() {
  try { areas.value = (await request('/api/areas/v1')) || [] } catch (e) { areas.value = [] }
}
// 年份是影片上的单值属性（不分站点各一套），因此下拉只取一次全局列表；
// 选项形如 [{ year: '2025', videoCount: 12 }]，按年份倒序。
async function loadYears() {
  try { years.value = (await request('/api/videos/v1/years')) || [] } catch (e) { years.value = [] }
}
async function loadJobs() {
  jobs.value = (await request('/api/jobs/v1?limit=50')) || []
}
// 采集任务（排队/运行/暂停）与并发容量
async function loadTasks() {
  const d = (await request('/api/tasks/v1')) || {}
  tasks.value = d.items || []
  if (d.capacity) capacity.value = d.capacity
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
      loadCategories(), loadCategoryTree(), loadTags(), loadAreas(), loadYears(), loadTasks(),
    ]
    if (isAdmin.value) pending.push(loadUsers(), loadSettings())
    await Promise.all(pending)
  } catch (e) {
    error.value = e.message
  }
}

function categoryName(id) {
  return categories.value.find(c => c.id === id)?.name || '-'
}
// 站点正在进行的采集任务（排队/运行），用于把「全量/增量」换成「暂停」。
// 同一站点现在可以「1 个运行 + N 个排队」，这里优先返回真正在跑的那个，
// 让暂停操作作用在当前任务上。
function activeTaskOf(sourceId) {
  const active = tasks.value.filter(t =>
    t.sourceId === sourceId && (t.status === 'running' || t.status === 'queued'))
  return active.find(t => t.status === 'running') || active[0] || null
}
// 该站点排队中的任务数（同站点串行，多出来的都在等）
function queuedCountOf(sourceId) {
  return tasks.value.filter(t => t.sourceId === sourceId && t.status === 'queued').length
}
function showCategoryVideos(c) {
  videoCategory.value = String(c.id)
  applyLinkage()
  videoPage.value = 1
  tab.value = 'videos'
  loadVideos().catch(e => { error.value = e.message })
}
// 点击卡片上的标签：按该标签筛选资源库（标签名大小写不敏感匹配）。
// 标签按站点隔离，优先取当前影片所在站点的同名标签，避免跳到别的站点去。
function filterByTag(name, sourceId) {
  const matched = tags.value.filter(x =>
    (x.name || '').toLowerCase() === String(name).toLowerCase())
  const t = matched.find(x => Number(x.sourceId) === Number(sourceId)) || matched[0]
  if (!t) return
  videoTag.value = String(t.id)
  applyLinkage()
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

// 一级分类通常自身没有影片（纯容器），直接显示 videoCount 会是「0 部」。
// 这里把子分类的影片数累加进来，作为该分类及其子树的展示数量。
function catVideoCount(node) {
  if (!node) return 0
  return (Number(node.videoCount) || 0) +
    (node.children || []).reduce((n, ch) => n + catVideoCount(ch), 0)
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
      const walk = n => {
        catCount++
        for (const ch of n.children || []) walk(ch)
      }
      roots.forEach(walk)
      orphans.forEach(walk)
      // 父节点的 videoCount 已含子树，站点总数只累加根节点与孤儿，避免重复计数
      let videoCount = 0
      for (const n of roots) videoCount += catVideoCount(n)
      for (const n of orphans) videoCount += catVideoCount(n)
      if (orphans.length) {
        roots.push({
          key: `orphan-${site.sourceId}`, virtual: true, name: '未归类',
          hint: '父分类未采集到，暂挂在此', videoCount: 0, // 只作为容器，数量由子节点累加
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

// 站点已定时下拉里不必再重复站点名；站点未定时保留前缀，避免同名选项混淆。
function catSelectLabel(c) {
  const path = catPath.value.get(c.id) || c.name
  return videoSource.value ? path : `${siteNameOf(c.sourceId)} · ${path}`
}
function tagSelectLabel(t) {
  return videoSource.value ? t.name : `${siteNameOf(t.sourceId)} · ${t.name}`
}

// 把联动结果写回各筛选 ref。changed 为 null 表示从分类树 / 卡片标签跳转后的校正。
function applyFilterLinkage(changed) {
  const state = {
    source: videoSource.value, category: videoCategory.value,
    tag: videoTag.value, area: videoAreaId.value,
  }
  const next = linkedFilters(state, changed, {
    category: categories.value, tag: tags.value, area: areas.value,
  })
  videoSource.value = next.source
  videoCategory.value = next.category
  videoTag.value = next.tag
  videoAreaId.value = next.area
}

// 筛选下拉的统一 change 入口：先写回本次变化的值，再联动校正，最后重载列表。
// 显式从事件取值，不依赖 v-model 与 @change 的监听器执行顺序。
function onFilterChange(changed, e) {
  const value = e?.target?.value ?? ''
  if (changed === 'sort') {
    videoSort.value = value
  } else if (changed === 'year') {
    // 年份是影片上的单值属性，不属于任何站点，因此不参与站点联动。
    videoYear.value = value
  } else {
    if (changed === 'source') videoSource.value = value
    else if (changed === 'category') videoCategory.value = value
    else if (changed === 'tag') videoTag.value = value
    else videoAreaId.value = value
    applyFilterLinkage(changed)
  }
  searchVideos()
}

// 从分类树 / 卡片标签跳转过来时，同样把站点同步到已选条件所属的站点。
function applyLinkage() { applyFilterLinkage(null) }

function clearAllFilters() {
  videoQuery.value = ''
  videoSource.value = ''
  videoCategory.value = ''
  videoTag.value = ''
  videoAreaId.value = ''
  videoYear.value = ''
  searchVideos()
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
// ---------- 影片详情：作为主内容区的独立页面，而非弹窗 ----------
// 离开详情页：销毁播放器并清空详情（地址栏由调用方或 hashchange 处理）。
function leaveDetail() {
  destroyPlayer()
  detail.value = null
}

// 详情页「返回资源库」：写回列表 hash，hashchange 会同步清掉详情。
function closeDetail() {
  leaveDetail()
  const h = tabToHash(DEFAULT_TAB)
  if (location.hash !== h) location.hash = h
}

// 把影片对象渲染成详情页面（初始化逻辑与弹窗时期一致）。
async function showVideoDetail(v) {
  detail.value = v
  detailTab.value = 'play'
  onlyNonEmpty.value = true
  // 默认选中第一条有剧集的线路
  const gi = playGroups.value.findIndex(g => (g.episodes?.length || 0) > 0)
  activeGroup.value = gi >= 0 ? gi : 0
  activeEp.value = 0
  if (!fieldMeta.value.fields?.length) await loadFieldMeta()
  mountPlayer() // 内部会 await nextTick() 等详情组件挂载出 <video>
}

// 从资源库点开影片：作为一次页面跳转，pushState 让浏览器后退能回到列表。
// 已在详情页时改用 replaceState，避免历史栈里堆一串影片。
async function openDetail(v) {
  const h = detailHash(v.id)
  if (detailIdFromHash(location.hash) == null) history.pushState(null, '', h)
  else if (location.hash !== h) history.replaceState(null, '', h)
  await showVideoDetail(v)
}

// 刷新 / 分享链接 / 浏览器前进后退落到 #/video/<id>：按 ID 重新取详情。
// 优先复用列表里已有的对象，否则调后端详情接口（返回结构与列表项一致）。
async function openDetailById(id) {
  try {
    const cached = videos.value.find(x => Number(x.id) === Number(id))
    const v = cached || await request(`/api/videos/v1/${id}`)
    await showVideoDetail(v)
  } catch (e) {
    error.value = `影片不存在或已移除：${e.message}`
    notify('影片不存在或已移除', 'err')
    leaveDetail()
    history.replaceState(null, '', tabToHash(DEFAULT_TAB))
    tab.value = DEFAULT_TAB
    loadVideos().catch(() => {})
  }
}

onBeforeUnmount(destroyPlayer)

// Esc 关闭当前弹窗；详情页是页面而非弹窗，Esc 仅作为「返回资源库」的快捷方式。
function onEsc(e) {
  if (e.key !== 'Escape') return
  if (importOpen.value) { closeImport(); return }
  if (sourceOpen.value) { closeSourceForm(); return }
  if (userFormOpen.value) { closeUserForm(); return }
  if (pwdModal.value) { pwdModal.value = false; return }
  if (detail.value) closeDetail()
}
onMounted(() => window.addEventListener('keydown', onEsc))
onBeforeUnmount(() => window.removeEventListener('keydown', onEsc))

// ---------- 站点配置 ----------
const emptyForm = () => ({
  id: null, name: '', apiBase: '', format: 'seacms_json', extraParams: '',
  incrementalHours: 24, maxPages: 0, enabled: true,
  scheduleEnabled: false, scheduleCron: '',
  parseApi: '', playDomain: '', picApi: '', picDomain: '',
})
const form = reactive(emptyForm())
const editing = computed(() => !!form.id)
const sourceOpen = ref(false)

// cron 输入时实时校验：给出合法/非法原因、这条规则的中文含义与接下来几次触发时刻。
// 纯前端计算（web/src/cron.js），与后端 utils/cron.go 语义一致，输入即反馈、无需请求。
const cronFeedback = computed(() => {
  if (!form.scheduleEnabled) return { state: 'off', error: '', meaning: '', next: [] }
  return evaluateCron(form.scheduleCron, { count: 3 })
})
const cronNextText = computed(() => cronFeedback.value.next.map(d => formatCronTime(d)).join('、'))

function resetForm() { Object.assign(form, emptyForm()) }

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
    Object.assign(form, {
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
  // 定时开启时前端先拦一道，避免带着明显非法的表达式白跑一次请求。
  if (form.scheduleEnabled && cronFeedback.value.state === 'invalid') {
    error.value = `定时配置有误：${cronFeedback.value.error}`
    notify(error.value, 'err')
    return
  }
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

// 清空站点数据但保留站点配置：影片、分类、标签、采集记录一并删除，
// 采集状态与影片数复位，站点接口地址等配置不动。
async function clearSourceData(s) {
  if (!confirm(`确认清空站点「${s.name}」的数据？\n\n将删除该站点的全部影片、分类、标签与采集记录，并复位采集状态；站点配置（接口地址、解析、启用状态等）保留。\n\n此操作不可撤销。`)) return
  try {
    const r = await request(`/api/sources/v1/${s.id}/data`, { method: 'DELETE' })
    notify(`已清空「${s.name}」：影片 ${r.videos}、分类 ${r.categories}、标签 ${r.tags}、采集记录 ${r.jobs}`)
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

async function loadSettings() {
  try {
    settings.value = (await request('/api/settings/v1')) || null
    // 表单只放可编辑的 collector 字段；只读启动项与字段元信息留在 settings 里展示
    settingsForm.value = { ...(settings.value?.collector || {}) }
    settingsDirty.value = false
  } catch (e) { error.value = e.message }
}

async function saveSettings() {
  if (settingsSaving.value || !settingsDirty.value) return
  settingsSaving.value = true
  try {
    // 只提交可编辑且未被环境变量锁定的字段；其余保持后端原值
    const payload = buildPayload(settings.value.fields, settingsForm.value)
    settings.value = await request('/api/settings/v1', jsonOptions('PUT', payload))
    settingsForm.value = { ...(settings.value.collector || {}) }
    settingsDirty.value = false
    // 并发上限可能变了，刷新容量让任务页/概览卡的只读展示同步
    const c = await request('/api/tasks/v1/capacity')
    if (c) capacity.value = c
    notify('系统设置已保存并即时生效')
  } catch (e) {
    error.value = e.message
    notify(e.message, 'err')
  } finally {
    settingsSaving.value = false
  }
}

function searchVideos() { loadVideos().catch(e => { error.value = e.message }) }

// ---------- 资源库底部无限滚动 ----------
// 用一个空哨兵元素 + IntersectionObserver：哨兵进入视口就加载下一页。
// rootMargin 提前 600px 触发，滚动时基本感觉不到等待。
const videoSentinel = ref(null)
let videoObserver = null

watch(videoSentinel, (el) => {
  if (videoObserver) { videoObserver.disconnect(); videoObserver = null }
  if (!el) return
  videoObserver = new IntersectionObserver((entries) => {
    if (entries.some(e => e.isIntersecting)) loadMoreVideos().catch(e => { error.value = e.message })
  }, { rootMargin: '600px 0px' })
  videoObserver.observe(el)
})
onBeforeUnmount(() => { if (videoObserver) videoObserver.disconnect() })

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
  tab.value = DEFAULT_TAB
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
// 新增 / 编辑用户共用一个弹窗；用户管理页只保留列表与「新增用户」入口。
const userFormOpen = ref(false)

function resetUserForm() { Object.assign(userForm, emptyUserForm()) }

function openUserForm(u = null) {
  resetUserForm()
  if (u) {
    Object.assign(userForm, {
      id: u.id, username: u.username, password: '', role: u.role,
      enabled: u.enabled, sourceIds: [...(u.sourceIds || [])],
    })
  }
  userFormOpen.value = true
}

function closeUserForm() {
  userFormOpen.value = false
  resetUserForm()
}

function editUser(u) { openUserForm(u) }

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
    closeUserForm() // 保存成功才关闭；失败时保留弹窗内容便于修改
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
    if (userForm.id === u.id) closeUserForm()
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

// ---------- 侧边栏导航 ----------
const sidebarCollapsed = ref(localStorage.getItem(SIDEBAR_KEY) === '1')
const sidebarMobileOpen = ref(false)
function toggleSidebar() {
  sidebarCollapsed.value = !sidebarCollapsed.value
  localStorage.setItem(SIDEBAR_KEY, sidebarCollapsed.value ? '1' : '')
}

// 导航项：按权限过滤，并带上各页面的实时计数徽标。
const navItems = computed(() => {
  const counts = {
    videos: visibleVideoTotal.value, sources: sources.value.length,
    categories: catTotal.value, jobs: jobs.value.length, users: users.value.length,
  }
  return navItemsFor(isAdmin.value).map(i => ({ ...i, count: counts[i.key] || 0 }))
})
const currentPageTitle = computed(() => detail.value
  ? (detail.value.vodName || '影片详情')
  : (NAV_ITEMS.find(i => i.key === tab.value)?.label || '资源库'))

// 点击侧边栏：切换页面并按需加载该页数据。详情页是资源库的下钻页，
// 点任意菜单都离开它（播放器一并销毁）。
function switchTab(key) {
  let k = normalizeTab(key) || DEFAULT_TAB
  if (k === 'users' && !isAdmin.value) k = DEFAULT_TAB
  leaveDetail()
  tab.value = k
  if (k === 'videos') loadVideos().catch(e => { error.value = e.message })
  else if (k === 'categories') loadCategories().catch(e => { error.value = e.message })
  else if (k === 'users') loadUsers().catch(e => { error.value = e.message })
}

// tab 变化时：同步 URL hash 与 localStorage（刷新/重开可恢复）、收起移动端抽屉，
// 并对采集类页面立刻补一次轮询数据。用 replaceState 写 hash 不产生历史噪音、也不回环。
watch(tab, (v) => {
  // 直接赋值 tab 的路径（分类树 / 标签跳转）也要离开详情页。
  if (detail.value) leaveDetail()
  const h = tabToHash(v)
  if (location.hash !== h) history.replaceState(null, '', h)
  localStorage.setItem(TAB_KEY, v)
  sidebarMobileOpen.value = false
  if (!me.value || !collectTabs.includes(v)) return
  refreshCollectData().catch(() => {})
})
// 用户手动改 hash / 浏览器前进后退时反向同步页面，并补齐该页数据。
function onHashChange() {
  const did = detailIdFromHash(location.hash)
  if (did != null) {
    // 前进 / 手动输入到某影片：按 ID 恢复详情页。
    if (Number(detail.value?.id) !== did) openDetailById(did)
    return
  }
  // 离开详情页（返回按钮、浏览器后退、切到别的页面）。
  leaveDetail()
  const k = normalizeTab(location.hash)
  if (!k) {
    // 无效 hash：地址栏纠正回当前页，页面保持不变（不把用户踢走）。
    if (location.hash !== tabToHash(tab.value)) history.replaceState(null, '', tabToHash(tab.value))
    return
  }
  if (k === tab.value) return
  tab.value = k
  if (k === 'videos') loadVideos().catch(() => {})
  else if (k === 'categories') loadCategories().catch(() => {})
  else if (k === 'users') loadUsers().catch(() => {})
}
// 普通用户若从链接直接落到「用户管理」，登录后校正回资源库；
// 刷新 / 分享链接直达 #/video/<id> 时，登录态就绪后按 ID 恢复详情。
watch(me, () => {
  if (tab.value === 'users' && !isAdmin.value) tab.value = DEFAULT_TAB
  const did = detailIdFromHash(location.hash)
  if (me.value && did != null && Number(detail.value?.id) !== did) openDetailById(did)
})
onMounted(() => {
  // 首次进入时 tab 是初始值、watch 不会触发，这里把空 / 非法 hash 规范化一次，
  // 保证地址栏与实际页面一致。详情地址是合法页面，不能被覆盖。
  if (!detailIdFromHash(location.hash) && normalizeTab(location.hash) !== tab.value) {
    history.replaceState(null, '', tabToHash(tab.value))
  }
  window.addEventListener('hashchange', onHashChange)
})
onBeforeUnmount(() => window.removeEventListener('hashchange', onHashChange))

function onVisible() {
  if (!document.hidden && me.value && collectTabs.includes(tab.value)) refreshCollectData().catch(() => {})
}
onMounted(() => document.addEventListener('visibilitychange', onVisible))
onBeforeUnmount(() => document.removeEventListener('visibilitychange', onVisible))

// 把共享状态一次性提供给各页面组件；子组件用 inject('app') 解构出的仍是原始 ref / computed / 函数。
provide('app', {
  TOKEN_KEY, token, me, authReady, initialized, isAdmin, currentUsername, clearAuth,
  setAuth, HlsLib, loadHls, tab, health, sources, videos, categories,
  categoryTree, tags, jobs, tasks, users, capacity,
  settings, settingsForm, settingsDirty, settingsSaving, loadSettings, saveSettings,
  error, notice, videoQuery, videoSource, videoCategory, videoTag, videoAreaId, videoYear, videoSort,
  areas, years, visibleCategories, visibleTags, visibleAreas, hasActiveFilters, areaGroups, videoPage, videoSize,
  videoLoading, videoLoadingMore, videoEnd, videoSeq, visibleVideoTotal, noticeTimer, notify,
  request, jsonOptions, loadHealth, loadSources, fetchVideos, kickSentinel, loadVideos, loadMoreVideos,
  loadAreas, loadYears, loadJobs, loadTasks, loadCategories, loadCategoryTree, loadTags, loadUsers, refresh,
  categoryName, activeTaskOf, queuedCountOf, showCategoryVideos, filterByTag, catCollapsed, isCatCollapsed, toggleCat,
  catKey, siteNameOf, categoryForest, catTotal, catVideoCount, catPath, catSelectLabel, tagSelectLabel, applyFilterLinkage,
  onFilterChange, applyLinkage, clearAllFilters, toggleAllCats, fieldMeta, detail, detailTab, onlyNonEmpty,
  loadFieldMeta, hasValue, nonEmptyCount, groupedFields, extraPairs, playGroups, posterOf, markPicFailed,
  episodeLabel, scoreOf, metaLine, tagListOf, playerEl, activeGroup, activeEp, hlsInstance,
  currentGroup, currentEpisodes, currentEpisode, currentUrl, detailSource, parseEnabled, useFrame, isHls,
  isDirectVideo, isUnknownStream, canPlay, playError, playErrorTimer, onVideoLoaded, hlsErrorText, videoErrorText,
  onVideoError, destroyPlayer, mountPlayer, chooseGroup, chooseEp, switchDetailTab, closeDetail, openDetail,
  onEsc, emptyForm, form, editing, sourceOpen, resetForm, sourcePayload, openSourceForm,
  closeSourceForm, editSource, importOpen, importBusy, importFileName, importRaw, importPreview, importFileEl,
  importPreviewShown, exportSources, pickImportFile, onImportFile, closeImport, confirmImport, saveSource, removeSource,
  clearSourceData, toggleSource, startTask, crawlAll, pauseTask, resumeTask, retryTask, rerunTask,
  barWidth, isIndeterminate, fmtEta, taskStatusText, searchVideos, videoSentinel, videoObserver,
  formatTime, modeLabel, statusLabel, short, stat, jobMessage, loginForm, loginBusy,
  loginError, doLogin, doLogout, pwdModal, pwdForm, doChangePassword, bootstrap, emptyUserForm,
  userForm, userEditing, userFormOpen, openUserForm, closeUserForm, resetUserForm,
  editUser, saveUser, toggleUser, removeUser, visibleLabel,
  collectTabs, refreshCollectData, taskTimer, onVisible
})
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
  <div class="app-shell">
    <!-- 移动端抽屉遮罩 -->
    <div v-if="sidebarMobileOpen" class="sidebar-backdrop" @click="sidebarMobileOpen = false"></div>

    <aside class="sidebar" :class="{ collapsed: sidebarCollapsed, 'mobile-open': sidebarMobileOpen }">
      <div class="sidebar-head">
        <span class="brand-mark">AS</span>
        <div class="brand-text">
          <h1>AssetSpider</h1>
          <p>视频站点采集</p>
        </div>
      </div>

      <nav class="side-nav">
        <button v-for="item in navItems" :key="item.key"
                class="nav-item" :class="{ active: tab === item.key, primary: item.primary }"
                :title="item.label" @click="switchTab(item.key)">
          <svg class="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor"
               stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <path :d="item.icon" />
          </svg>
          <span class="nav-label">{{ item.label }}</span>
          <span v-if="item.count" class="nav-count">{{ item.count }}</span>
        </button>
      </nav>

      <div class="side-overview">
        <div class="ov">
          <span class="ov-label">可见站点</span>
          <b class="ov-value">{{ sources.length }}</b>
          <span class="ov-sub">启用 {{ sources.filter(s => s.enabled).length }} · 停用 {{ sources.length - sources.filter(s => s.enabled).length }}</span>
        </div>
        <div class="ov">
          <span class="ov-label">可见影片</span>
          <b class="ov-value">{{ visibleVideoTotal }}</b>
          <span class="ov-sub">按站点影片数快照累加</span>
        </div>
        <div class="ov">
          <span class="ov-label">采集任务</span>
          <b class="ov-value">{{ capacity.running }}<span class="ov-unit">/ {{ capacity.maxConcurrent }}</span></b>
          <span class="ov-sub">运行中 · 排队 {{ capacity.queued }}</span>
        </div>
        <div class="ov">
          <span class="ov-label">字段清单</span>
          <b class="ov-value">{{ fieldMeta.total || '-' }}</b>
          <span class="ov-sub">标准字段 · 全量入库</span>
        </div>
      </div>

      <div class="side-foot">
        <span class="db-chip" :title="health.database || ''">DB {{ health.database || '加载中…' }}</span>

        <!-- 账号与操作区：原在顶栏右上角，现移到导航栏最底部 -->
        <div class="side-account">
          <div class="acct-head" :title="sidebarCollapsed ? '展开侧边栏查看账号操作' : currentUsername"
               @click="sidebarCollapsed && toggleSidebar()">
            <span class="acct-avatar">{{ (currentUsername || '?').slice(0, 1).toUpperCase() }}</span>
            <span class="acct-text">
              <b class="acct-name">{{ currentUsername || '未登录' }}</b>
              <span class="badge" :class="isAdmin ? 'on' : 'off'">{{ isAdmin ? '管理员' : '普通用户' }}</span>
            </span>
          </div>
          <div class="acct-actions">
            <button class="mini ghost" @click="pwdModal = true">修改密码</button>
            <button class="mini ghost" @click="doLogout">退出登录</button>
            <button class="mini ghost acct-wide" @click="refresh">刷新</button>
          </div>
        </div>
      </div>

      <button class="sidebar-toggle" @click="toggleSidebar"
              :title="sidebarCollapsed ? '展开侧边栏' : '收起侧边栏'">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
             stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <path :d="sidebarCollapsed ? 'M9 6l6 6-6 6' : 'M15 6l-6 6 6 6'" />
        </svg>
        <span>收起侧边栏</span>
      </button>
    </aside>

    <div class="main-area">
      <header class="topbar">
        <button class="hamburger" @click="sidebarMobileOpen = !sidebarMobileOpen" aria-label="打开菜单">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
               stroke-linecap="round" aria-hidden="true"><path d="M3 6h18M3 12h18M3 18h18" /></svg>
        </button>
        <div class="topbar-title"><span>AssetSpider</span><b>{{ currentPageTitle }}</b></div>
      </header>

      <main class="page">
        <div v-if="notice" class="notice" :class="notice.type">{{ notice.text }}</div>
        <p v-if="error" class="error">{{ error }}</p>

        <!-- 影片详情是独立页面，占据整个主内容区；其余按侧边栏切换 -->
        <VideoDetailView v-if="detail" />
        <template v-else>
          <SourcesView v-if="tab === 'sources'" />
          <CategoriesView v-else-if="tab === 'categories'" />
          <VideosView v-else-if="tab === 'videos'" />
          <JobsView v-else-if="tab === 'jobs'" />
          <SettingsView v-else-if="tab === 'settings' && isAdmin" />
          <UsersView v-else-if="tab === 'users' && isAdmin" />
        </template>
      </main>
    </div>
  </div>

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
          <p class="sf-group full">定时增量采集<span class="muted">到点自动发起一次增量任务，站点停用或已有任务在跑时不触发</span></p>
          <label class="check full"><input v-model="form.scheduleEnabled" type="checkbox" /> 启用定时增量采集</label>
          <label class="sf-item full">
            <span>执行时间（5 段 cron：分 时 日 月 周）</span>
            <input v-model="form.scheduleCron" list="cron-presets" :disabled="!form.scheduleEnabled"
                   :required="form.scheduleEnabled" placeholder="例：0 3 * * *（每天 03:00）" />
            <datalist id="cron-presets">
              <option value="*/30 * * * *">每 30 分钟</option>
              <option value="0 * * * *">每小时整点</option>
              <option value="0 */6 * * *">每 6 小时</option>
              <option value="0 3 * * *">每天 03:00</option>
              <option value="0 3 * * 1-5">工作日 03:00</option>
            </datalist>
            <span class="hint">支持 <code>*</code>、<code>*/n</code>、<code>a-b</code>、逗号列表；停服期间错过的时刻不补跑，同一分钟只触发一次。</span>
          </label>
          <div class="cron-feedback full" :class="cronFeedback.state" aria-live="polite">
            <template v-if="cronFeedback.state === 'off'">
              <span class="cron-icon">○</span>未启用定时增量采集，表达式不会生效。
            </template>
            <template v-else-if="cronFeedback.state === 'empty'">
              <span class="cron-icon">○</span>填写 cron 表达式后这里会实时显示校验结果与含义；也可以点输入框选一个常用预设。
            </template>
            <template v-else-if="cronFeedback.state === 'invalid'">
              <span class="cron-icon">✕</span>表达式无效：{{ cronFeedback.error }}
            </template>
            <template v-else>
              <div><span class="cron-icon">✓</span>表达式有效 · {{ cronFeedback.meaning }}</div>
              <div v-if="cronNextText" class="cron-next">接下来 {{ cronFeedback.next.length }} 次：{{ cronNextText }}</div>
            </template>
          </div>

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
  </template>
</template>

<style>
:root {
  color-scheme: light;
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif;
  --bg: #f3f5f9;
  --panel: #fff;
  --border: #e6e9f0;
  --border-strong: #d7dde9;
  --text: #1a2230;
  --text-2: #4c5666;
  --muted: #7b8596;
  --muted-2: #9aa4b4;
  --brand: #2f6bff;
  --brand-dark: #1f57e0;
  --brand-soft: #eef4ff;
  --danger: #d93a3f;
  --radius: 14px;
  --shadow-sm: 0 1px 2px rgba(16, 24, 40, .04), 0 1px 3px rgba(16, 24, 40, .05);
  --shadow-md: 0 10px 30px rgba(16, 24, 40, .1);
}
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--text); -webkit-font-smoothing: antialiased; }
.page { max-width: 1240px; margin: 0 auto; padding: 0 20px 64px; }

/* ---------- 品牌标识 / 账号区（侧边栏与顶栏共用） ---------- */
.brand-mark {
  width: 40px; height: 40px; flex: none; border-radius: 12px;
  display: grid; place-items: center;
  color: #fff; font-size: 15px; font-weight: 700; letter-spacing: .5px;
  background: linear-gradient(135deg, #2f6bff, #7aa2ff);
  box-shadow: 0 8px 18px rgba(47, 107, 255, .28);
}
.brand-text h1 { margin: 0; font-size: 19px; letter-spacing: .2px; }
.brand-text p { margin: 2px 0 0; font-size: 12px; color: var(--muted); }
.db-chip {
  max-width: 360px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  padding: 4px 10px; border-radius: 999px; background: #f2f4f8; color: var(--muted);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 11px;
}

/* ---------- 概览指标（侧边栏内竖排，容器样式见 .side-overview） ---------- */
.ov {
  display: flex; flex-direction: column; gap: 2px;
  padding: 14px 16px; background: var(--panel);
  border: 1px solid var(--border); border-radius: var(--radius); box-shadow: var(--shadow-sm);
}
.ov-label { font-size: 12px; color: var(--muted); }
.ov-value { font-size: 24px; line-height: 1.25; font-weight: 700; letter-spacing: -.5px; }
.ov-unit { margin-left: 4px; font-size: 13px; font-weight: 500; letter-spacing: 0; color: var(--muted); }
.ov-sub { font-size: 11px; color: var(--muted-2); }

.card {
  margin-top: 16px; padding: 18px 20px 20px;
  background: var(--panel); border: 1px solid var(--border);
  border-radius: var(--radius); box-shadow: var(--shadow-sm);
}
.card h2 { margin: 0 0 14px; font-size: 15px; letter-spacing: .2px; }
.list-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 12px; flex-wrap: wrap; }
.list-head h2 { margin: 0; }
.list-head-l { display: flex; align-items: baseline; gap: 10px; }
.actions, .toolbar { display: flex; gap: 10px; flex-wrap: wrap; align-items: center; }
form, .source-form { display: flex; gap: 10px; flex-wrap: wrap; align-items: center; }

input, select {
  padding: 8px 11px; border: 1px solid var(--border-strong); border-radius: 8px;
  background: #fff; color: var(--text); font-size: 14px; font-family: inherit;
  transition: border-color .15s ease, box-shadow .15s ease;
}
input { flex: 1 1 190px; min-width: 0; }
input.wide { flex: 2 1 380px; }
input:focus, select:focus { outline: none; border-color: #9db9ff; box-shadow: 0 0 0 3px rgba(47, 107, 255, .12); }
select { cursor: pointer; }
.field { display: flex; align-items: center; gap: 6px; font-size: 13px; color: var(--text-2); }
.field input { width: 90px; flex: none; }
.field.inline { color: var(--text-2); }
.check { display: flex; align-items: center; gap: 6px; font-size: 13px; color: var(--text-2); }
.check input { flex: none; width: auto; }

button {
  padding: 8px 16px; border: 0; border-radius: 8px;
  background: var(--brand); color: #fff;
  font-size: 14px; font-family: inherit; white-space: nowrap; cursor: pointer;
  box-shadow: 0 1px 2px rgba(47, 107, 255, .28);
  transition: background .15s ease, color .15s ease, box-shadow .15s ease, transform .1s ease;
}
button:hover:not(:disabled) { background: var(--brand-dark); }
button:active:not(:disabled) { transform: translateY(1px); }
button:disabled { opacity: .5; cursor: default; box-shadow: none; }
button.ghost { background: #f3f5f9; color: var(--text-2); box-shadow: none; }
button.ghost:hover:not(:disabled) { background: #e9edf5; color: var(--text); }
button.mini { padding: 5px 10px; font-size: 12px; border-radius: 7px; box-shadow: none; }
button.danger { background: #fdeef0; color: var(--danger, #d93a3f); box-shadow: none; }
button.danger:hover:not(:disabled) { background: #fbdde1; }

.hint { margin: 12px 0 0; font-size: 12px; color: var(--muted); line-height: 1.8; }
code { padding: 1px 6px; border-radius: 5px; background: #f1f4f9; color: #43506a; font-size: .92em; }
details.tip { margin-top: 12px; }
details.tip summary {
  display: inline-flex; align-items: center; gap: 8px;
  padding: 5px 12px; border: 1px solid var(--border); border-radius: 999px;
  background: #f7f9fd; color: var(--muted);
  font-size: 12px; font-weight: 500; cursor: pointer; list-style: none;
  transition: background .15s ease, border-color .15s ease;
}
details.tip summary:hover { background: var(--brand-soft); border-color: #c9d9ff; color: var(--text-2); }
details.tip summary::-webkit-details-marker { display: none; }
details.tip summary::after { content: "展开"; font-weight: 600; color: var(--brand); }
details.tip[open] summary::after { content: "收起"; }
details.tip .hint {
  margin: 10px 0 0; padding: 12px 14px;
  border: 1px solid var(--border); border-radius: 10px; background: #fafbff;
}
details.tip .hint:first-of-type { margin-top: 10px; }

/* ---------- 表格 ---------- */
.table-wrap { overflow: auto; max-height: 74vh; border: 1px solid var(--border); border-radius: 12px; }
table { width: 100%; border-collapse: separate; border-spacing: 0; font-size: 13px; }
th, td { text-align: left; padding: 10px 12px; white-space: nowrap; }
thead th {
  position: sticky; top: 0; z-index: 1;
  background: #f7f9fc; color: var(--muted);
  font-size: 12px; font-weight: 600; letter-spacing: .2px;
  border-bottom: 1px solid var(--border);
}
tbody td { border-bottom: 1px solid #f1f3f8; color: var(--text-2); }
tbody tr:last-child td { border-bottom: 0; }
tbody tr:hover td { background: #f8faff; }
.host { font-weight: 600; color: var(--text); }
.url, .msg { max-width: 280px; overflow: hidden; text-overflow: ellipsis; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 12px; }
.tiny { font-size: 11px; margin-top: 3px; color: var(--muted); }
.muted { color: var(--muted); }
.mono.up { color: #0f9b4b; font-weight: 600; }
.badge {
  display: inline-flex; align-items: center; gap: 5px;
  padding: 3px 9px; border-radius: 999px;
  background: #f1f3f8; color: #5b6577; font-size: 12px; font-weight: 500; line-height: 1.4;
}
.badge.on, .badge.success { background: #e9f9ef; color: #0f9b4b; }
.badge.off { background: #f1f3f8; color: var(--muted); }
.badge.failed { background: #fdecee; color: #d93a3f; }
.badge.running { background: var(--brand-soft); color: var(--brand); }
.badge.queued { background: #f4efff; color: #7b40d8; }
.badge.paused { background: #fff4e5; color: #c2680a; }
.badge.running::before, .badge.success::before, .badge.failed::before,
.badge.paused::before, .badge.queued::before {
  content: ""; width: 6px; height: 6px; border-radius: 50%; background: currentColor;
}
.ops { display: flex; gap: 5px; flex-wrap: wrap; }

/* ---------- 资源库工具栏 ---------- */
.res-toolbar { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.search-box { display: flex; gap: 8px; flex: 1 1 320px; min-width: 0; }
.search-box input { flex: 1 1 auto; }
.filters { display: flex; gap: 8px; flex-wrap: wrap; }
.filters .btn-clear { border: 1px solid var(--border-strong); }

/* 实时进度条（任务列表 / 采集记录） */
.prog-col { min-width: 220px; white-space: normal; }
.prog-track { position: relative; height: 6px; border-radius: 4px; background: #eef1f6; overflow: hidden; }
.prog-track.sm { height: 4px; margin-bottom: 3px; }
.prog-track i { display: block; height: 100%; width: 0; background: linear-gradient(90deg, #2f6bff, #6f9bff); border-radius: 4px; transition: width .4s ease; }
.prog-track.indet i {
  background: linear-gradient(90deg, #2f6bff 25%, #b7ccff 50%, #2f6bff 75%);
  background-size: 200% 100%;
  animation: prog-indet 1.2s linear infinite;
}
@keyframes prog-indet { from { background-position: 120% 0; } to { background-position: -80% 0; } }
.prog-meta { display: flex; flex-wrap: wrap; gap: 8px; align-items: baseline; margin-top: 5px; font-size: 11px; color: var(--muted); }
.prog-meta b { color: var(--text); font-size: 12px; }
.tiny.msg { max-width: 340px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* ---------- 资源库：加载态与底部无限滚动 ---------- */
.spinner {
  width: 18px; height: 18px; flex: none; display: inline-block;
  border-radius: 50%; border: 2px solid #dfe4ef; border-top-color: var(--brand);
  animation: spin .7s linear infinite;
}
@keyframes spin { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) { .spinner { animation-duration: 1.6s; } }

.res-body { position: relative; }
/* 搜索/切筛选时盖住视口：固定在视口中央，避免列表很长时转圈落到屏幕外 */
.res-mask {
  position: fixed; inset: 0; z-index: 40;
  display: flex; align-items: center; justify-content: center;
  background: rgba(243, 245, 249, .45);
  backdrop-filter: blur(1px);
  animation: mask-in .12s ease-out;
}
@keyframes mask-in { from { opacity: 0; } to { opacity: 1; } }
.res-mask-inner {
  display: flex; align-items: center; gap: 10px;
  padding: 14px 24px; border-radius: 999px;
  background: #fff; box-shadow: 0 12px 34px rgba(16, 24, 40, .14);
  color: var(--text-2); font-size: 13px; font-weight: 500;
}
.res-mask-text { letter-spacing: .3px; }

.res-more {
  display: flex; align-items: center; justify-content: center; gap: 8px;
  min-height: 56px; padding: 12px 0 4px;
  font-size: 12.5px; color: var(--muted);
}
.res-more-end { display: inline-flex; align-items: center; }
.res-more-end::before, .res-more-end::after {
  content: ''; display: inline-block; width: 26px; height: 1px; vertical-align: middle;
  margin: 0 8px; background: var(--border-strong);
}
.empty {
  margin: 10px 0 0; padding: 30px 16px;
  border: 1px dashed var(--border-strong); border-radius: 12px;
  background: #fbfcfe; color: var(--muted); font-size: 13px; text-align: center;
}
.modal-body .empty { margin: 0; padding: 18px 14px; font-size: 12.5px; border-radius: 10px; }
.error { padding: 10px 14px; border-radius: 10px; margin-top: 16px; background: #fdecee; color: #c0262c; font-size: 13px; }
.notice { padding: 10px 14px; border-radius: 10px; margin-top: 16px; background: #e9f9ef; color: #0f8a44; font-size: 13px; }
.notice.err { background: #fdecee; color: #c0262c; }
.notice.info { background: var(--brand-soft); color: var(--brand); }
.modal-mask {
  position: fixed; inset: 0; z-index: 50;
  display: flex; align-items: center; justify-content: center; padding: 24px;
  background: rgba(16, 24, 40, .5); backdrop-filter: blur(2px);
}
.modal {
  width: min(960px, 100%); max-height: 88vh;
  display: flex; flex-direction: column; overflow: hidden;
  background: var(--panel); border-radius: 16px;
  box-shadow: 0 24px 60px rgba(16, 24, 40, .28);
}
.modal-head { display: flex; justify-content: space-between; align-items: flex-start; gap: 16px; padding: 18px 22px; border-bottom: 1px solid var(--border); }
.modal-head h3 { margin: 0 0 6px; font-size: 17px; }
.modal-head p { margin: 0; font-size: 12px; }
.modal-tabs, .detail-tabs { display: flex; gap: 8px; align-items: center; padding: 12px 22px; border-bottom: 1px solid var(--border); flex-wrap: wrap; }
.modal-tabs button, .detail-tabs button { padding: 5px 12px; background: #f2f4f8; color: var(--text-2); font-size: 13px; box-shadow: none; }
.modal-tabs button:hover:not(:disabled), .detail-tabs button:hover:not(:disabled) { background: #e8ecf4; }
.modal-tabs button.active, .detail-tabs button.active { background: var(--brand); color: #fff; }
.modal-tabs .check, .detail-tabs .check { margin-left: auto; }
.modal-body { overflow-y: auto; padding: 6px 22px 22px; }
/* 站点配置导入 / 导出 */
.hidden-file { display: none; }
.import-ops { flex-wrap: wrap; justify-content: flex-end; align-items: center; }
.import-list { max-height: 300px; overflow-y: auto; border: 1px solid var(--border); border-radius: 10px; }
.import-row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; padding: 9px 12px; font-size: 13px; border-bottom: 1px solid #f1f3f8; }
.import-row:last-child { border-bottom: 0; }
.import-row b { font-size: 13px; }
.modal-actions { display: flex; justify-content: flex-end; gap: 10px; padding: 14px 22px 20px; border-top: 1px solid var(--border); }
/* 站点配置弹窗表单 */
.modal.narrow { width: min(720px, 100%); }
.sf-form { display: grid; grid-template-columns: 1fr 1fr; align-items: start; gap: 12px 18px; padding: 16px 22px 22px; }
.sf-item { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
.sf-item > span { font-size: 12px; color: var(--text-2); }
.sf-item input { width: 100%; flex: none; }
.sf-form .full { grid-column: 1 / -1; }
.sf-group {
  display: flex; align-items: baseline; gap: 8px; margin: 6px 0 0; padding-top: 10px;
  border-top: 1px dashed var(--border); font-size: 13px; font-weight: 600; color: var(--brand);
}
.sf-group .muted { font-weight: 400; font-size: 12px; }
.sf-actions { display: flex; gap: 10px; justify-content: flex-end; }
.sf-form .hint { margin: 2px 0 0; }
/* cron 实时校验反馈：合法绿、非法红、未启用/未填写灰 */
.cron-feedback {
  margin-top: -4px; padding: 9px 11px; border-radius: 9px;
  border: 1px solid var(--border); background: #f6f8fc;
  font-size: 12px; line-height: 1.7; color: var(--text-2);
}
.cron-feedback.ok { background: #eefaf2; border-color: #c8ead4; color: #12713e; }
.cron-feedback.invalid { background: #fdf0f1; border-color: #f4cccc; color: #b4232c; }
.cron-feedback.off, .cron-feedback.empty { color: var(--muted); }
.cron-icon { display: inline-block; width: 16px; font-weight: 700; }
.cron-next { margin-top: 3px; padding-left: 16px; color: var(--text-2); }
.field-group { margin-top: 18px; }
.field-group h4 { margin: 0 0 10px; font-size: 14px; color: var(--brand); }
.field-group h4 span { font-weight: 400; font-size: 12px; }
.field-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(270px, 1fr)); gap: 8px 16px; }
.field-item { border-bottom: 1px dashed #f1f3f8; padding-bottom: 6px; min-width: 0; }
.field-item.long { grid-column: 1 / -1; }
.ep-list { margin: 6px 0 0; padding-left: 20px; }
.ep-list li { padding: 4px 0; border-bottom: 1px dashed #f1f3f8; }
.ep-name { display: inline-block; min-width: 110px; font-weight: 600; color: var(--text); }
.ep-url { color: var(--brand); text-decoration: none; word-break: break-all; }
.ep-url:hover { text-decoration: underline; }
.badge.info { background: var(--brand-soft); color: var(--brand); }
/* 系统设置页 */
.settings-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 14px 18px; margin-top: 16px; }
.setting-item { display: flex; flex-direction: column; gap: 6px; min-width: 0; padding: 12px 14px; border: 1px solid var(--border); border-radius: 11px; background: var(--panel); }
.setting-item.locked { background: #f6f8fc; opacity: .85; }
.setting-label { display: flex; align-items: center; gap: 8px; font-size: 13px; font-weight: 600; color: var(--text); }
.setting-control { display: flex; align-items: center; gap: 8px; }
.setting-control input { flex: 1 1 auto; min-width: 0; }
.setting-control input:disabled { cursor: not-allowed; }
.setting-unit { flex: none; font-size: 12px; color: var(--muted); }
.setting-help { font-size: 11.5px; color: var(--muted); line-height: 1.6; }
.setting-error { font-size: 11.5px; color: #d93a3f; }
.settings-actions { display: flex; align-items: center; gap: 12px; margin-top: 18px; flex-wrap: wrap; }
.settings-static { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 8px 16px; margin-top: 12px; }
.settings-static > div { display: flex; justify-content: space-between; gap: 10px; padding: 7px 0; border-bottom: 1px dashed var(--border); font-size: 12px; }
.settings-static span { color: var(--muted); }
.settings-static b { color: var(--text-2); font-weight: 600; text-align: right; word-break: break-all; }
.field-k { font-size: 11px; color: var(--muted); margin-bottom: 2px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.field-v { font-size: 13px; word-break: break-all; white-space: pre-wrap; max-height: 66px; overflow: hidden; }
.field-item.long .field-v { max-height: 130px; }
.kv-table { width: 100%; }
.kv-table th { width: 240px; text-align: left; }
.kv-table td { white-space: pre-wrap; word-break: break-all; }
/* 资源卡片（名称 / 图片 / 更新集数） */
.res-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(172px, 1fr)); gap: 18px; margin-top: 18px; }
.res-card {
  border: 1px solid var(--border); border-radius: 12px; overflow: hidden;
  background: var(--panel); cursor: pointer;
  transition: box-shadow .18s ease, transform .18s ease, border-color .18s ease;
}
.res-card:hover { transform: translateY(-3px); border-color: #d7e2ff; box-shadow: 0 14px 30px rgba(16, 24, 40, .12); }
.res-poster { position: relative; aspect-ratio: 2 / 3; background: #eef1f6; overflow: hidden; }
.res-poster img { width: 100%; height: 100%; object-fit: cover; display: block; transition: transform .35s ease; }
.res-card:hover .res-poster img { transform: scale(1.05); }
.res-poster-ph { width: 100%; height: 100%; display: flex; align-items: center; justify-content: center; font-size: 40px; color: #b9c1cf; background: linear-gradient(135deg, #f2f4f8, #e3e8f1); }
.res-score { position: absolute; left: 8px; top: 8px; padding: 2px 7px; border-radius: 6px; background: rgba(15, 23, 42, .72); color: #ffc53d; font-size: 12px; font-weight: 600; backdrop-filter: blur(4px); }
.res-eps {
  position: absolute; right: 0; bottom: 0; left: 0;
  padding: 14px 8px 6px; color: #fff; font-size: 11px; text-align: right;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  background: linear-gradient(180deg, rgba(0, 0, 0, 0), rgba(0, 0, 0, .72));
}
.res-info { padding: 9px 11px 11px; }
.res-info h4 { margin: 0 0 4px; font-size: 13px; font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.res-meta { margin: 0 0 4px; font-size: 11px; color: var(--muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.res-tags { display: flex; flex-wrap: wrap; gap: 4px; margin: 0 0 4px; }
.res-tag { font-size: 10px; line-height: 1; padding: 3px 7px; border-radius: 999px; background: var(--brand-soft); color: #2f6df6; cursor: pointer; white-space: nowrap; }
.res-tag:hover { background: #dbe6ff; }
.res-time { margin: 0; font-size: 11px; color: var(--muted-2); }
.detail-tags { margin-top: 8px; }
/* 详情播放器与线路 / 剧集选择 */
.player-wrap { margin-top: 4px; }
.player { width: 100%; max-height: 420px; background: #000; border-radius: 10px; display: block; }
.player-frame { width: 100%; aspect-ratio: 16 / 9; max-height: 420px; border: 0; border-radius: 10px; background: #000; display: block; }
.group-tabs, .ep-chips { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 12px; }
.ep-chips { max-height: 168px; overflow-y: auto; padding: 2px; }
.play-url { margin: 10px 0 0; font-size: 11px; color: var(--muted); word-break: break-all; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
.play-error { margin: 10px 0 0; padding: 9px 12px; border: 1px solid #ffc9cd; background: #fdecee; border-radius: 8px; font-size: 12px; color: #c0262c; line-height: 1.7; word-break: break-all; }
/* 分类树：资源站点 → 一级分类 → 二级分类 */
.tree { display: flex; flex-direction: column; gap: 12px; }
.tree-site { border: 1px solid var(--border); border-radius: 12px; overflow: hidden; }
.tree-row {
  display: flex; align-items: center; flex-wrap: nowrap;
  gap: 10px; padding: 9px 12px; min-width: 0;
  border-top: 1px solid var(--border);
  transition: background .12s ease;
}
.tree-row.site { background: #f7f9fc; cursor: pointer; user-select: none; border-top: 0; }
.tree-row.site:hover { background: #eef3fb; }
.tree-row.group { background: #fbfcff; }
.tree-row.group .tree-title { color: var(--text-2); font-weight: 600; }
.tree-row:hover { background: var(--brand-soft); }
.tree-row .tree-title {
  font-size: 13.5px; font-weight: 600;
  flex: 0 1 auto; min-width: 0;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
/* 分类名本身即可点击跳转，覆盖全局 button 的胶囊按钮样式 */
.tree-row .tree-title.link {
  padding: 0; border-radius: 0; background: transparent; color: var(--brand);
  font-weight: 600; line-height: inherit;
  text-align: left; box-shadow: none;
}
.tree-row .tree-title.link:hover:not(:disabled) { background: transparent; color: var(--brand-dark); text-decoration: underline; }
.tree-row .tree-title.link:active:not(:disabled) { transform: none; }
.tree-row .tree-title.link:focus-visible { outline: 2px solid var(--brand); outline-offset: 2px; border-radius: 3px; }
.tree-row > .muted { flex: none; white-space: nowrap; font-size: 12px; }
.tree-row > .badge { flex: none; }
.tree-gap { flex: 1 1 auto; min-width: 8px; height: 1px; align-self: center; }

.twisty {
  width: 12px; flex: none; font-size: 10px; line-height: 1; color: var(--muted);
  display: inline-block; transition: transform .15s ease; transform: rotate(0deg);
}
.twisty.open { transform: rotate(90deg); }
.twisty.no-kids { visibility: hidden; }
.tree-kids { border-top: 1px solid #f1f3f8; }
.tree-kids .tree-row { padding-left: 28px; }
.tree-kids .tree-kids { border-top: 1px dashed #f1f3f8; }
.tree-kids .tree-kids .tree-row { padding-left: 46px; background: #fafbfe; }
.tree-kids .tree-kids .tree-row:hover { background: #f4f7fc; }

/* ---------- 手机端适配（≤760px） ---------- */
@media (max-width: 760px) {
  body { overflow-x: hidden; } /* 兜底：任何元素都不应把整页撑出横向滚动 */
  .page { padding: 0 12px 44px; }

  /* 品牌在移动抽屉里略收窄，副标题隐藏 */
  .brand-mark { width: 34px; height: 34px; border-radius: 10px; font-size: 13px; }
  .brand-text h1 { font-size: 17px; }
  .brand-text p { display: none; }
  .ov { padding: 11px 13px; }
  .ov-value { font-size: 20px; }
  .ov-sub { font-size: 10px; }

  .card { padding: 14px; margin-top: 14px; border-radius: 12px; }
  .card h2 { font-size: 15px; }
  /* 标题行的右侧统计在窄屏改竖排，避免 nowrap 撑宽卡片、把整页挤出屏幕 */
  .list-head { flex-direction: column; align-items: flex-start; gap: 4px; }
  .list-head span { white-space: normal; }
  .hint { font-size: 11px; line-height: 1.7; }
  .hint code { word-break: break-all; }
  details.tip summary { padding: 5px 11px; }

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
  /* 任务操作区窄屏改为整列堆叠，避免按钮与「最多同时运行」互相挤压换行 */
  .actions { flex-direction: column; align-items: stretch; }
  .actions > button { width: 100%; }
  .actions .field { justify-content: space-between; }
  .actions .field input { width: 110px; }

  /* 资源库工具栏：搜索占一行，筛选下拉两列 */
  .res-toolbar { gap: 8px; }
  .search-box { flex: 1 1 100%; }
  .search-box button { flex: 0 0 auto; }
  .filters { flex: 1 1 100%; display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
  .filters select { width: 100%; }
  .filters .btn-clear { grid-column: 1 / -1; }

  /* 表格横滚 + 隐藏次要列；再压一档内边距与字号，尽量让常用列在窄屏一次看全 */
  .table-wrap { -webkit-overflow-scrolling: touch; max-height: none; border-radius: 10px; }
  th, td { padding: 8px 8px; font-size: 12px; }
  .mono { font-size: 11px; }
  .hide-sm { display: none; }
  .prog-col { min-width: 140px; }
  .url, .msg { max-width: 160px; }
  .ops { gap: 4px; }
  .ops button.mini { padding: 5px 7px; }

  /* 弹窗全屏；详情页面与播放器按比例收敛高度 */
  .modal-mask { padding: 0; align-items: stretch; }
  .modal { width: 100%; height: 100%; max-height: none; border-radius: 0; }
  .modal-head { padding: 14px 16px; flex-wrap: wrap; gap: 8px; }
  .modal-head h3 { font-size: 16px; }
  .modal-tabs, .detail-tabs { padding: 10px 16px; gap: 6px; }
  .modal-tabs button, .detail-tabs button { padding: 5px 10px; }
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

  /* 系统设置：窄屏单列 */
  .settings-grid, .settings-static { grid-template-columns: 1fr; gap: 10px; }
  /* 资源卡片：窄屏两列 */
  .res-grid { grid-template-columns: repeat(auto-fill, minmax(138px, 1fr)); gap: 10px; }
  .res-poster-ph { font-size: 30px; }
  .res-info { padding: 7px 8px 9px; }
  .res-info h4 { font-size: 12px; }
  .res-meta, .res-time { font-size: 10px; }

  /* 分类树：收窄缩进，分类名可点，不再需要给按钮留位置 */
  .tree-row { padding: 9px 10px; gap: 6px; }
  .tree-row .tree-title { font-size: 13px; }
  .tree-kids .tree-row { padding-left: 20px; }
  .tree-kids .tree-kids .tree-row { padding-left: 34px; }
  .ep-name { min-width: 0; }
  .ep-list { padding-left: 16px; }
  .res-more { min-height: 48px; font-size: 12px; }
  .notice, .error { font-size: 12px; padding: 8px 10px; }
}

/* ---------- 登录与账号 ---------- */
.boot { min-height: 100vh; display: flex; align-items: center; justify-content: center; color: var(--muted); font-size: 14px; }
.login-wrap {
  min-height: 100vh; display: flex; align-items: center; justify-content: center; padding: 24px;
  background:
    radial-gradient(900px 500px at 15% 0%, #e2ebff 0%, rgba(226, 235, 255, 0) 60%),
    radial-gradient(700px 400px at 90% 100%, #e8f1ff 0%, rgba(232, 241, 255, 0) 60%),
    #f3f5f9;
}
.login-card {
  width: min(430px, 100%); padding: 34px 32px;
  background: var(--panel); border: 1px solid var(--border); border-radius: 18px;
  box-shadow: 0 24px 60px rgba(16, 24, 40, .12);
}
.login-card h1 { margin: 0; font-size: 24px; letter-spacing: .2px; }
.login-card > p.muted { margin: 8px 0 22px; font-size: 13px; }
.login-card form { display: flex; flex-direction: column; gap: 12px; }
.login-card form input { width: 100%; flex: none; padding: 11px 12px; }
.login-card form button { width: 100%; padding: 11px; margin-top: 4px; }
.login-card .hint { margin: 18px 0 0; }
.user-form { display: flex; gap: 10px; flex-wrap: wrap; align-items: center; }
.source-grants { flex-basis: 100%; display: flex; flex-direction: column; gap: 4px; }
.grant-list { display: flex; flex-wrap: wrap; gap: 8px 16px; margin-top: 4px; }
.modal.sm { width: min(460px, 100%); }


/* ---------- 侧边栏布局（页面拆分后） ---------- */
.app-shell { display: flex; align-items: stretch; min-height: 100vh; }
.sidebar {
  position: sticky; top: 0; align-self: flex-start;
  width: 236px; flex: none; height: 100vh; z-index: 40;
  display: flex; flex-direction: column; overflow-y: auto;
  background: var(--panel); border-right: 1px solid var(--border);
  transition: width .18s ease;
}
.sidebar-head {
  display: flex; align-items: center; gap: 10px; min-height: 70px;
  padding: 14px; border-bottom: 1px solid var(--border);
}
.sidebar-head .brand-mark { width: 36px; height: 36px; border-radius: 10px; font-size: 14px; }
.sidebar-head .brand-text { min-width: 0; overflow: hidden; }
.sidebar-head .brand-text h1 { font-size: 16px; white-space: nowrap; }
.sidebar-head .brand-text p { font-size: 11px; white-space: nowrap; }

.side-nav { display: flex; flex-direction: column; gap: 3px; padding: 12px 10px; flex: 1 1 auto; }
.nav-item {
  display: flex; align-items: center; gap: 11px; width: 100%;
  padding: 10px 12px; border-radius: 9px;
  background: transparent; color: var(--text-2);
  font-size: 14px; font-weight: 500; text-align: left; white-space: nowrap;
  box-shadow: none;
}
.side-nav .nav-item:hover:not(:disabled) { background: var(--brand-soft); color: var(--brand); }
.nav-item.active { background: var(--brand-soft); color: var(--brand); font-weight: 600; }
.nav-item.primary.active { box-shadow: inset 3px 0 0 var(--brand); }
.nav-icon { width: 19px; height: 19px; flex: none; }
.nav-label { flex: 1 1 auto; overflow: hidden; text-overflow: ellipsis; }
.nav-count {
  flex: none; padding: 0 7px; border-radius: 999px;
  background: #eef1f7; color: #5b6577; font-size: 11px; font-weight: 600; line-height: 18px;
}
.nav-item.active .nav-count { background: #fff; color: var(--brand); }

.side-overview {
  display: grid; gap: 8px; padding: 12px;
  border-top: 1px solid var(--border);
}
.side-overview .ov { padding: 9px 12px; gap: 1px; box-shadow: none; }
.side-overview .ov-value { font-size: 19px; }
.side-overview .ov-sub { font-size: 10px; }
.side-foot { padding: 10px 12px 4px; }
.side-foot .db-chip { display: block; max-width: 100%; }

/* 账号与操作区（导航栏底部）：头像 + 用户名/角色 + 修改密码 / 退出登录 / 刷新 */
.side-account { margin-top: 10px; padding-top: 10px; border-top: 1px dashed var(--border); }
.acct-head { display: flex; align-items: center; gap: 9px; min-width: 0; padding: 4px 2px; border-radius: 9px; }
.acct-avatar {
  width: 30px; height: 30px; flex: none; border-radius: 50%;
  display: grid; place-items: center;
  background: var(--brand-soft); color: var(--brand);
  font-size: 13px; font-weight: 700; letter-spacing: .3px;
}
.acct-text { display: flex; flex-direction: column; align-items: flex-start; gap: 3px; min-width: 0; }
.acct-name { max-width: 100%; font-size: 13px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.acct-text .badge { padding: 1px 7px; font-size: 10px; line-height: 1.6; }
.acct-actions { display: grid; grid-template-columns: 1fr 1fr; gap: 6px; margin-top: 8px; }
.acct-actions button { padding: 6px 8px; font-size: 12px; }
.acct-actions .acct-wide { grid-column: 1 / -1; }

.sidebar-toggle {
  display: flex; align-items: center; gap: 10px; margin: 4px 10px 14px;
  padding: 9px 12px; border-radius: 9px;
  background: #f3f5f9; color: var(--text-2); font-size: 13px;
  box-shadow: none; white-space: nowrap;
}
.sidebar-toggle:hover:not(:disabled) { background: #e9edf5; color: var(--text); }
.sidebar-toggle svg { width: 16px; height: 16px; flex: none; }

.main-area { flex: 1 1 auto; min-width: 0; }
.topbar {
  position: sticky; top: 0; z-index: 30;
  display: flex; align-items: center; gap: 12px;
  padding: 12px 20px;
  background: rgba(255, 255, 255, .92);
  backdrop-filter: saturate(180%) blur(14px);
  border-bottom: 1px solid var(--border);
}
.topbar-title { display: flex; align-items: baseline; gap: 10px; min-width: 0; }
.topbar-title span { font-size: 12px; color: var(--muted); white-space: nowrap; }
.topbar-title b { font-size: 16px; white-space: nowrap; }
.hamburger {
  display: none; flex: none; align-items: center; justify-content: center;
  width: 38px; height: 38px; padding: 0; border-radius: 9px;
  background: #f3f5f9; color: var(--text-2); box-shadow: none;
}
.hamburger:hover:not(:disabled) { background: #e9edf5; color: var(--text); }
.hamburger svg { width: 20px; height: 20px; }
.sidebar-backdrop { display: none; }

/* 桌面：折叠态只留图标 */
@media (min-width: 901px) {
  .sidebar.collapsed { width: 68px; }
  .sidebar.collapsed .sidebar-head { justify-content: center; padding: 14px 0; }
  .sidebar.collapsed .brand-text,
  .sidebar.collapsed .nav-label,
  .sidebar.collapsed .nav-count,
  .sidebar.collapsed .side-overview,
  .sidebar.collapsed .sidebar-toggle span { display: none; }
  /* 折叠态：底部只留头像（点一下展开，露出操作按钮），DB 与文字全部隐藏 */
  .sidebar.collapsed .side-foot { padding: 10px 8px 4px; }
  .sidebar.collapsed .side-foot .db-chip,
  .sidebar.collapsed .acct-text,
  .sidebar.collapsed .acct-actions { display: none; }
  .sidebar.collapsed .side-account { margin-top: 8px; padding-top: 8px; border-top: 0; }
  .sidebar.collapsed .acct-head { justify-content: center; padding: 4px 0; cursor: pointer; }
  .sidebar.collapsed .acct-head:hover { background: var(--brand-soft); }
  .sidebar.collapsed .nav-item { justify-content: center; padding: 10px 0; }
  .sidebar.collapsed .side-nav { padding: 12px 8px; }
  .sidebar.collapsed .sidebar-toggle { justify-content: center; padding: 9px 0; margin: 4px 8px 14px; }
}

/* 平板 / 手机：侧边栏改为抽屉 + 汉堡按钮，折叠态不再生效 */
@media (max-width: 900px) {
  .sidebar {
    position: fixed; top: 0; left: 0; width: 236px; height: 100vh;
    transform: translateX(-100%); transition: transform .2s ease;
    box-shadow: var(--shadow-md); z-index: 60;
  }
  .sidebar.mobile-open { transform: translateX(0); }
  .sidebar-backdrop {
    display: block; position: fixed; inset: 0; z-index: 55;
    background: rgba(16, 24, 40, .42);
  }
  .hamburger { display: inline-flex; }
  .main-area { width: 100%; }
  .topbar { padding: 10px 14px; }
}
@media (max-width: 560px) {
  .topbar-title span { display: none; }
}


/* ---------- 影片详情页面（占据主内容区，不再是弹窗） ---------- */
.detail-page { padding: 0; overflow: hidden; }
.detail-head {
  display: flex; align-items: flex-start; gap: 16px; flex-wrap: wrap;
  padding: 18px 22px; border-bottom: 1px solid var(--border);
}
.detail-back { flex: none; white-space: nowrap; }
.detail-title { flex: 1 1 260px; min-width: 0; }
.detail-title h2 { margin: 0 0 6px; font-size: 19px; line-height: 1.35; word-break: break-word; }
.detail-title p { margin: 0; font-size: 12px; }
.detail-body { padding: 16px 22px 22px; }

/* ---------- 新增 / 编辑用户弹窗表单 ---------- */
.list-add { margin-left: auto; }
.modal-body.user-form { padding: 16px 22px 22px; }
.user-form-wide { flex-basis: 100%; width: 100%; }
.user-form-actions {
  flex-basis: 100%; display: flex; justify-content: flex-end;
  gap: 10px; padding-top: 4px;
}

/* 窄屏：详情页内边距收敛，弹窗表单按钮铺满 */
@media (max-width: 900px) {
  .detail-head { padding: 14px 16px; gap: 10px; }
  .detail-body { padding: 12px 16px 20px; }
  .detail-title h2 { font-size: 17px; }
  .modal-body.user-form { padding: 12px 16px 18px; }
  .user-form-actions button { flex: 1 1 auto; }
}
</style>
