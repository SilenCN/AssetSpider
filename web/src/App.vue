<script setup>
import { computed, onMounted, reactive, ref } from 'vue'

const tab = ref('sources')
const health = ref({})
const sources = ref([])
const videos = ref([])
const jobs = ref([])
const busy = ref(false)
const error = ref('')
const notice = ref(null)

const videoQuery = ref('')
const videoSource = ref('')
const videoPage = ref(1)
const videoSize = ref(20)
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
  health.value = await request('/api/health')
}
async function loadSources() {
  sources.value = (await request('/api/sources')) || []
}
async function loadVideos() {
  const p = new URLSearchParams({ page: String(videoPage.value), size: String(videoSize.value) })
  if (videoQuery.value.trim()) p.set('q', videoQuery.value.trim())
  if (videoSource.value) p.set('source_id', String(videoSource.value))
  const d = await request('/api/videos?' + p.toString())
  videos.value = d.items || []
  videoTotal.value = d.total || 0
  videoPage.value = d.page || 1
  videoSize.value = d.size || 20
}
async function loadJobs() {
  jobs.value = (await request('/api/jobs?limit=50')) || []
}

async function refresh() {
  error.value = ''
  try {
    await Promise.all([loadHealth(), loadSources(), loadVideos(), loadJobs()])
  } catch (e) {
    error.value = e.message
  }
}

// ---------- 全字段详情 ----------
const fieldMeta = ref({ groups: [], fields: [], total: 0 })
const detail = ref(null)
const detailTab = ref('fields')
const onlyNonEmpty = ref(true)

async function loadFieldMeta() {
  try { fieldMeta.value = await request('/api/meta/fields') } catch (e) { /* 元数据不影响主流程 */ }
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

function prettyRaw(raw) {
  try { return JSON.stringify(JSON.parse(raw), null, 2) } catch (e) { return raw || '' }
}

async function openDetail(v) {
  detail.value = v
  detailTab.value = 'fields'
  onlyNonEmpty.value = true
  if (!fieldMeta.value.fields?.length) await loadFieldMeta()
}

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
    if (form.id) await request(`/api/sources/${form.id}`, jsonOptions('PUT', payload))
    else await request('/api/sources', jsonOptions('POST', payload))
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
    await request(`/api/sources/${s.id}`, { method: 'DELETE' })
    notify('站点已删除')
    await refresh()
  } catch (e) { notify(e.message, 'err') }
}

async function toggleSource(s) {
  try {
    await request(`/api/sources/${s.id}`,
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
    const r = await request(`/api/sources/${s.id}/crawl?mode=${mode}`, { method: 'POST' })
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
    const d = await request(`/api/crawl?mode=${mode}`, { method: 'POST' })
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
      <button :class="{ active: tab === 'videos' }" @click="tab = 'videos'; loadVideos()">采集数据</button>
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
        <p class="hint">采集协议：海洋CMS / 苹果CMS 系 <code>api.php/provide/vod/?ac=list</code>，返回 JSON 的 <code>page / pagecount / total / list[]</code>。增量采集附加 <code>h=&lt;小时数&gt;</code> 并按 <code>vod_time</code> 游标去重。接口返回的<strong>全部标准字段</strong>结构化入库，站点自定义字段存入 <code>extra</code>，原始记录全文存入 <code>raw</code>，不丢字段。</p>
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

    <!-- 采集数据 -->
    <template v-if="tab === 'videos'">
      <section class="card">
        <div class="toolbar">
          <input v-model="videoQuery" placeholder="按名称 / 别名 / 演员 / 导演 / 分类搜索" @keyup.enter="searchVideos" />
          <select v-model="videoSource" @change="searchVideos">
            <option value="">全部站点</option>
            <option v-for="s in sources" :key="s.id" :value="s.id">{{ s.name }}</option>
          </select>
          <button @click="searchVideos">搜索</button>
          <span class="muted">共 {{ videoTotal }} 条</span>
        </div>
        <p v-if="!videos.length" class="empty">暂无采集数据，可到「站点配置」执行一次全量采集。</p>
        <div v-else class="table-wrap">
          <table>
            <thead>
              <tr><th>ID</th><th>来源</th><th>VodID</th><th>影片名称</th><th>分类</th><th>备注</th><th>站点更新时间</th><th>首次入库</th><th>字段</th></tr>
            </thead>
            <tbody>
              <tr v-for="v in videos" :key="v.id">
                <td class="mono">{{ v.id }}</td>
                <td class="muted">{{ sources.find(s => s.id === v.sourceId)?.name || v.sourceId }}</td>
                <td class="mono">{{ v.vodId }}</td>
                <td class="host" :title="v.vodPlayUrl">{{ v.vodName || '-' }}</td>
                <td>{{ v.typeName || '-' }}</td>
                <td>{{ v.vodRemarks || '-' }}</td>
                <td class="mono">{{ v.vodTime || '-' }}</td>
                <td class="muted">{{ formatTime(v.firstSeenAt) }}</td>
                <td><button class="mini ghost" @click="openDetail(v)">详情</button></td>
              </tr>
            </tbody>
          </table>
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

    <!-- 全字段详情 -->
    <div v-if="detail" class="modal-mask" @click.self="detail = null">
      <div class="modal">
        <div class="modal-head">
          <div>
            <h3>{{ detail.vodName || detail.vodId }}</h3>
            <p class="muted">全字段明细 · 标准字段 {{ fieldMeta.total }} 个，本条有值 {{ nonEmptyCount }} 个 · VodID {{ detail.vodId }} · 来源 {{ detail.sourceId }}</p>
          </div>
          <button class="ghost" @click="detail = null">关闭</button>
        </div>
        <div class="modal-tabs">
          <button :class="{ active: detailTab === 'fields' }" @click="detailTab = 'fields'">标准字段</button>
          <button :class="{ active: detailTab === 'extra' }" @click="detailTab = 'extra'">扩展字段 ({{ extraPairs.length }})</button>
          <button :class="{ active: detailTab === 'raw' }" @click="detailTab = 'raw'">原始 JSON</button>
          <label v-if="detailTab === 'fields'" class="check"><input v-model="onlyNonEmpty" type="checkbox" /> 只看非空</label>
        </div>
        <div class="modal-body">
          <template v-if="detailTab === 'fields'">
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
          <template v-else>
            <p class="hint">接口返回的该条记录原始 JSON 全文，未经裁剪，可用于还原任意字段。</p>
            <pre class="raw-json">{{ prettyRaw(detail.raw) }}</pre>
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
.field-k { font-size: 11px; color: #86909c; margin-bottom: 2px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.field-v { font-size: 13px; word-break: break-all; white-space: pre-wrap; max-height: 66px; overflow: hidden; }
.field-item.long .field-v { max-height: 130px; }
.kv-table { width: 100%; }
.kv-table th { width: 240px; text-align: left; }
.kv-table td { white-space: pre-wrap; word-break: break-all; }
.raw-json { background: #1f2329; color: #e8eaed; padding: 14px; border-radius: 8px; font-size: 12px; line-height: 1.6; overflow: auto; max-height: 58vh; white-space: pre-wrap; word-break: break-all; }
</style>
