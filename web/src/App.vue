<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref } from 'vue'

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
const jobs = ref([])
const busy = ref(false)
const error = ref('')
const notice = ref(null)

const videoQuery = ref('')
const videoSource = ref('')
const videoCategory = ref('')
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
  const res = await fetch(path, options)
  const body = await res.json().catch(() => ({}))
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
async function loadCategories() {
  categories.value = (await request('/api/categories/v1')) || []
}
async function loadCategoryTree() {
  categoryTree.value = (await request('/api/categories/v1/tree')) || []
}

async function refresh() {
  error.value = ''
  try {
    await Promise.all([
      loadHealth(), loadSources(), loadVideos(), loadJobs(),
      loadCategories(), loadCategoryTree(), loadAreas(),
    ])
  } catch (e) {
    error.value = e.message
  }
}

function categoryName(id) {
  return categories.value.find(c => c.id === id)?.name || '-'
}
function sourceName(id) {
  return sources.value.find(s => s.id === id)?.name || id
}
function showCategoryVideos(c) {
  videoCategory.value = String(c.id)
  videoPage.value = 1
  tab.value = 'videos'
  loadVideos().catch(e => { error.value = e.message })
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

// ---------- 详情与播放 ----------
const playerEl = ref(null)
const activeGroup = ref(0)
const activeEp = ref(0)
let hlsInstance = null

const currentGroup = computed(() => playGroups.value[activeGroup.value] || null)
const currentEpisodes = computed(() => currentGroup.value?.episodes || [])
const currentEpisode = computed(() => currentEpisodes.value[activeEp.value] || null)
const currentUrl = computed(() => currentEpisode.value?.url || '')
const isHls = computed(() => /\.m3u8(\?|#|$)/i.test(currentUrl.value))
const isDirectVideo = computed(() => /\.(mp4|webm|ogg|mov)(\?|#|$)/i.test(currentUrl.value))
const canPlay = computed(() => isHls.value || isDirectVideo.value)

function destroyPlayer() {
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

// 按地址类型挂载播放器：HLS 用 hls.js（Safari 可直接播），mp4 等直接给 src。
async function mountPlayer() {
  destroyPlayer()
  await nextTick()
  const el = playerEl.value
  const url = currentUrl.value
  if (!el || !url || !canPlay.value) return
  if (isHls.value) {
    if (el.canPlayType('application/vnd.apple.mpegurl')) {
      el.src = url
    } else {
      const Hls = await loadHls()
      if (!Hls.isSupported()) return
      hlsInstance = new Hls({ enableWorker: true })
      hlsInstance.loadSource(url)
      hlsInstance.attachMedia(el)
      hlsInstance.on(Hls.Events.MANIFEST_PARSED, () => { el.play().catch(() => {}) })
      return
    }
  } else {
    el.src = url
  }
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

// ---------- 站点配置 ----------
const emptyForm = () => ({
  id: null, name: '', apiBase: '', format: 'seacms_json', extraParams: '',
  incrementalHours: 24, maxPages: 0, enabled: true,
})
const form = reactive(emptyForm())
const editing = computed(() => !!form.id)

function resetForm() { Object.assign(form, emptyForm()) }

function sourcePayload(s) {
  return {
    name: s.name, apiBase: s.apiBase, format: s.format || 'seacms_json',
    extraParams: s.extraParams || '', enabled: s.enabled,
    incrementalHours: Number(s.incrementalHours) || 0,
    maxPages: Number(s.maxPages) || 0,
  }
}

function editSource(s) {
  Object.assign(form, {
    id: s.id, name: s.name, apiBase: s.apiBase, format: s.format,
    extraParams: s.extraParams, incrementalHours: s.incrementalHours,
    maxPages: s.maxPages, enabled: s.enabled,
  })
  tab.value = 'sources'
  window.scrollTo({ top: 0, behavior: 'smooth' })
}

async function saveSource() {
  error.value = ''
  try {
    const payload = sourcePayload(form)
    if (form.id) await request(`/api/sources/v1/${form.id}`, jsonOptions('PUT', payload))
    else await request('/api/sources/v1', jsonOptions('POST', payload))
    resetForm()
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

// ---------- 采集 ----------
function summarize(r) {
  return `拉取 ${r.fetched} · 新增 ${r.new} · 更新 ${r.updated} · 未变 ${r.unchanged} · ${r.pages} 页`
}

async function crawlOne(s, mode) {
  if (busy.value) return
  busy.value = true
  const label = mode === 'full' ? '全量' : '增量'
  notify(`正在对「${s.name}」执行${label}采集…`, 'info')
  try {
    const r = await request(`/api/sources/v1/${s.id}/crawls`, jsonOptions('POST', { mode }))
    notify(`「${s.name}」${label}采集完成：${summarize(r)}`)
    await refresh()
  } catch (e) {
    notify(`${label}采集失败：${e.message}`, 'err')
    error.value = e.message
  } finally {
    busy.value = false
  }
}

async function crawlAll(mode) {
  if (busy.value) return
  busy.value = true
  const label = mode === 'full' ? '全量' : '增量'
  notify(`正在对全部启用站点执行${label}采集…`, 'info')
  try {
    const d = await request('/api/crawls/v1', jsonOptions('POST', { mode }))
    const list = d.results || []
    const ok = list.filter(r => r.status === 'success').length
    const text = `${label}采集结束：${ok}/${list.length} 个站点成功`
    notify(d.error ? `${text}；${d.error}` : text, d.error ? 'err' : 'ok')
    await refresh()
  } catch (e) {
    notify(`批量采集失败：${e.message}`, 'err')
    error.value = e.message
  } finally {
    busy.value = false
  }
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
  return { success: '成功', failed: '失败', running: '进行中', never: '未采集' }[s] || s || '-'
}
function short(text, n = 46) {
  const s = String(text || '')
  return s.length > n ? s.slice(0, n) + '…' : s
}

onMounted(() => { refresh(); loadFieldMeta() })
</script>

<template>
  <main class="page">
    <header>
      <h1>AssetSpider</h1>
      <p>视频站点采集 · JSON 接口 · 全量 / 增量 · 全字段入库 · Vue 3 + Gin + GORM/SQLite</p>
      <div class="stats">
        <span class="db">DB: {{ health.database || '加载中…' }}</span>
        <span class="stat">站点 <b>{{ health.sources ?? '-' }}</b></span>
        <span class="stat">影片 <b>{{ health.videos ?? '-' }}</b></span>
        <span class="stat">字段 <b>{{ fieldMeta.total || '-' }}</b></span>
      </div>
    </header>

    <div v-if="notice" class="notice" :class="notice.type">{{ notice.text }}</div>
    <p v-if="error" class="error">{{ error }}</p>

    <nav class="tabs">
      <button :class="{ active: tab === 'sources' }" @click="tab = 'sources'">站点配置</button>
      <button :class="{ active: tab === 'videos' }" @click="tab = 'videos'; loadVideos()">资源库</button>
      <button :class="{ active: tab === 'categories' }" @click="tab = 'categories'; loadCategories()">视频分类</button>
      <button :class="{ active: tab === 'jobs' }" @click="tab = 'jobs'; loadJobs()">采集记录</button>
      <button class="ghost refresh" @click="refresh">刷新</button>
    </nav>

    <!-- 站点配置 -->
    <template v-if="tab === 'sources'">
      <section class="card">
        <div class="list-head">
          <h2>批量采集</h2>
          <span class="muted">对全部启用站点执行，SQLite 下串行执行</span>
        </div>
        <div class="actions">
          <button @click="crawlAll('full')" :disabled="busy">全量采集全部站点</button>
          <button class="ghost" @click="crawlAll('incremental')" :disabled="busy">增量采集全部站点</button>
          <span v-if="busy" class="muted">采集中，请稍候…</span>
        </div>
      </section>

      <section class="card">
        <h2>{{ editing ? '编辑站点' : '新增采集站点' }}</h2>
        <form class="source-form" @submit.prevent="saveSource">
          <input v-model="form.name" placeholder="站点名称 *" required />
          <input v-model="form.apiBase" class="wide" placeholder="JSON 接口地址 * 例：https://site.com/api.php/provide/vod/" required />
          <input v-model="form.extraParams" placeholder="附加参数，如 t=1&amp;wd=电影" />
          <label class="field">增量小时数
            <input v-model.number="form.incrementalHours" type="number" min="0" />
          </label>
          <label class="field">最大页数 (0=全局)
            <input v-model.number="form.maxPages" type="number" min="0" />
          </label>
          <label class="check"><input v-model="form.enabled" type="checkbox" /> 启用</label>
          <button type="submit">{{ editing ? '保存修改' : '添加站点' }}</button>
          <button v-if="editing" type="button" class="ghost" @click="resetForm">取消</button>
        </form>
        <p class="hint">采集协议：苹果CMS V10 Provide <code>api.php/provide/vod/?ac=videolist</code>，返回 JSON 的 <code>page / pagecount / total / list[]</code>（含 <code>vod_play_url</code> 播放地址）。增量采集附加 <code>h=&lt;小时数&gt;</code> 并按 <code>vod_time</code> 游标去重。接口返回的<strong>全部标准字段</strong>结构化入库，站点自定义字段存入 <code>extra</code>，不丢字段。</p>
      </section>

      <section class="card">
        <div class="list-head"><h2>站点列表</h2><span class="muted">共 {{ sources.length }} 个</span></div>
        <p v-if="!sources.length" class="empty">还没有站点，先在上方添加一个 JSON 采集接口。</p>
        <div v-else class="table-wrap">
          <table>
            <thead>
              <tr>
                <th>ID</th><th>名称</th><th>接口地址</th><th>状态</th><th>影片数</th>
                <th>上次采集</th><th>游标 / 结果</th><th>操作</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="s in sources" :key="s.id">
                <td class="mono">{{ s.id }}</td>
                <td class="host">{{ s.name }}</td>
                <td class="url" :title="s.apiBase">{{ short(s.apiBase, 38) }}</td>
                <td>
                  <span class="badge" :class="s.enabled ? 'on' : 'off'">{{ s.enabled ? '启用' : '停用' }}</span>
                </td>
                <td class="mono">{{ s.videoCount }}</td>
                <td class="muted">{{ formatTime(s.lastSyncAt) }}</td>
                <td class="muted">
                  <span class="badge" :class="s.lastStatus">{{ statusLabel(s.lastStatus) }}</span>
                  <div class="mono tiny">{{ s.lastCursor || '-' }}</div>
                </td>
                <td class="ops">
                  <button class="mini" @click="crawlOne(s, 'full')" :disabled="busy">全量</button>
                  <button class="mini ghost" @click="crawlOne(s, 'incremental')" :disabled="busy">增量</button>
                  <button class="mini ghost" @click="editSource(s)">编辑</button>
                  <button class="mini ghost" @click="toggleSource(s)">{{ s.enabled ? '停用' : '启用' }}</button>
                  <button class="mini danger" @click="removeSource(s)">删除</button>
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
          <span class="muted">按资源站自动归档，共 {{ categories.length }} 个分类（支持二级）</span>
        </div>
        <p class="hint">采集时按接口返回的 type_id / type_name 自动创建分类并关联影片，每个资源站各自一套；依据苹果CMS 的 type_pid 模型支持二级分类：视频的 <code>type_id_1</code> 即所属一级分类 ID，二级分类会挂到对应一级分类之下。点「查看影片」可按分类（含其子分类）筛选影片。</p>
        <p v-if="!categoryTree.length" class="empty">还没有分类，执行一次采集后会自动生成。</p>
        <div v-else class="cat-tree">
          <div v-for="node in categoryTree" :key="node.id" class="cat-node">
            <div class="cat-row">
              <span class="badge" :class="node.level === 1 ? 'on' : 'off'">{{ node.level === 1 ? '一级' : '二级' }}</span>
              <b>{{ node.name }}</b>
              <span class="muted">站点ID {{ node.typeId || '-' }}</span>
              <span class="muted">{{ node.videoCount }} 部</span>
              <button class="mini ghost" @click="showCategoryVideos(node)">查看影片</button>
            </div>
            <div v-if="node.children && node.children.length" class="cat-children">
              <div v-for="ch in node.children" :key="ch.id" class="cat-row child">
                <span class="badge off">二级</span>
                <b>{{ ch.name }}</b>
                <span class="muted">站点ID {{ ch.typeId || '-' }}</span>
                <span class="muted">{{ ch.videoCount }} 部</span>
                <button class="mini ghost" @click="showCategoryVideos(ch)">查看影片</button>
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
            <option v-for="c in categories" :key="c.id" :value="c.id">{{ sourceName(c.sourceId) }} · {{ c.name }}</option>
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
        <p v-if="!videos.length" class="empty">暂无采集数据，可到「站点配置」执行一次全量采集。</p>
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
              <tr><th>ID</th><th>站点</th><th>模式</th><th>状态</th><th>页数</th><th>拉取</th><th>新增</th><th>更新</th><th>未变</th><th>开始时间</th><th>说明</th></tr>
            </thead>
            <tbody>
              <tr v-for="j in jobs" :key="j.id">
                <td class="mono">{{ j.id }}</td>
                <td class="host">{{ j.sourceName }}</td>
                <td>{{ modeLabel(j.mode) }}</td>
                <td><span class="badge" :class="j.status">{{ statusLabel(j.status) }}</span></td>
                <td class="mono">{{ j.pages }}</td>
                <td class="mono">{{ j.fetched }}</td>
                <td class="mono up">{{ j.new }}</td>
                <td class="mono">{{ j.updated }}</td>
                <td class="mono muted">{{ j.unchanged }}</td>
                <td class="muted">{{ formatTime(j.startedAt) }}</td>
                <td class="muted msg" :title="j.message">{{ short(j.message || '-', 30) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </template>

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
              <video ref="playerEl" class="player" controls playsinline preload="metadata"></video>
              <p v-if="!playGroups.length" class="empty">该影片没有播放地址（vod_play_url 为空）。</p>
              <p v-else-if="!canPlay" class="hint">
                当前集不是可直链播放的视频地址：<code>{{ currentUrl || '（空地址）' }}</code>
                <a v-if="currentUrl" :href="currentUrl" target="_blank" rel="noopener" class="ep-url">在新窗口打开</a>
              </p>
              <p v-else class="hint">支持 m3u8（自动用 hls.js 播放）与 mp4 / webm 直链；切换线路或集数即时切换。</p>
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
              <p v-if="currentEpisode?.url" class="play-url">{{ currentEpisode.url }}</p>
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
  </main>
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
.ops { display: flex; gap: 5px; }
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
.res-time { margin: 0; font-size: 11px; color: #a9aeb8; }
/* 详情播放器与线路 / 剧集选择 */
.player-wrap { margin-top: 4px; }
.player { width: 100%; max-height: 420px; background: #000; border-radius: 8px; display: block; }
.group-tabs, .ep-chips { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 12px; }
.ep-chips { max-height: 168px; overflow-y: auto; padding: 2px; }
.play-url { margin: 10px 0 0; font-size: 11px; color: #86909c; word-break: break-all; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
.cat-tree { display: flex; flex-direction: column; gap: 10px; }
.cat-node { border: 1px solid #e5e6eb; border-radius: 8px; overflow: hidden; }
.cat-row { display: flex; align-items: center; gap: 10px; padding: 10px 12px; flex-wrap: wrap; }
.cat-row.child { background: #fafbfc; border-top: 1px solid #f0f1f3; padding-left: 34px; }
.cat-row b { font-size: 14px; }
.cat-children { }
</style>
