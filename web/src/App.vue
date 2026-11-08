<script setup>
import { computed, onMounted, reactive, ref } from 'vue'

const TOKEN_KEY = 'assetspider_token'
const token = ref(localStorage.getItem(TOKEN_KEY) || '')
const me = ref(null)
const authReady = ref(false)
const initialized = ref(false)

const isAdmin = computed(() => !!me.value?.isAdmin)
const currentUsername = computed(() => me.value?.user?.username || '')

const tab = ref('sources')
const health = ref({})
const sources = ref([])
const videos = ref([])
const jobs = ref([])
const users = ref([])
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

function clearAuth() {
  localStorage.removeItem(TOKEN_KEY)
  token.value = ''
  me.value = null
}
function setAuth(tk) {
  token.value = tk
  localStorage.setItem(TOKEN_KEY, tk)
}

// request 统一注入 Authorization；401 时清除本地登录态并回到登录页。
async function request(path, options = {}) {
  const headers = { ...(options.headers || {}) }
  if (token.value) headers['Authorization'] = 'Bearer ' + token.value
  const res = await fetch(path, { ...options, headers })
  const body = await res.json().catch(() => ({}))
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

async function loadHealth() { health.value = await request('/api/health') }
async function loadSources() { sources.value = (await request('/api/sources')) || [] }
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
async function loadJobs() { jobs.value = (await request('/api/jobs?limit=50')) || [] }
async function loadUsers() {
  if (!isAdmin.value) { users.value = []; return }
  users.value = (await request('/api/users')) || []
}

async function refresh() {
  error.value = ''
  try {
    const tasks = [loadHealth(), loadSources(), loadVideos(), loadJobs()]
    if (isAdmin.value) tasks.push(loadUsers())
    await Promise.all(tasks)
  } catch (e) {
    error.value = e.message
  }
}

// ---------- 登录 / 会话 ----------
const loginForm = reactive({ username: '', password: '' })
const loginBusy = ref(false)
const loginError = ref('')

async function doLogin() {
  loginError.value = ''
  loginBusy.value = true
  try {
    const d = await request('/api/auth/login', jsonOptions('POST', {
      username: loginForm.username.trim(), password: loginForm.password,
    }))
    setAuth(d.token)
    me.value = { user: d.user, isAdmin: d.isAdmin, visibleSourceIds: [] }
    initialized.value = true
    loginForm.password = ''
    notify(d.firstUser ? '系统已初始化，你的账号自动注册为管理员' : `欢迎回来，${d.user.username}`)
    if (!fieldMeta.value.fields?.length) loadFieldMeta().catch(() => {})
    await refresh()
  } catch (e) {
    loginError.value = e.message
  } finally {
    loginBusy.value = false
  }
}

async function doLogout() {
  try { await request('/api/auth/logout', { method: 'POST' }) } catch (e) { /* 忽略 */ }
  clearAuth()
  notify('已退出登录', 'info')
}

const pwdModal = ref(false)
const pwdForm = reactive({ oldPassword: '', newPassword: '' })
async function doChangePassword() {
  try {
    const d = await request('/api/auth/password', jsonOptions('POST', { ...pwdForm }))
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
    const st = await request('/api/auth/status')
    initialized.value = st.initialized
    if (token.value) {
      try { me.value = await request('/api/auth/me') } catch (e) { clearAuth() }
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

// ---------- 全字段详情 ----------
const fieldMeta = ref({ groups: [], fields: [], total: 0 })
const detail = ref(null)
const detailTab = ref('fields')
const onlyNonEmpty = ref(true)

async function loadFieldMeta() {
  fieldMeta.value = await request('/api/meta/fields')
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
  notify(`正在执行${label}采集…`, 'info')
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
      await request(`/api/users/${userForm.id}`, jsonOptions('PUT', payload))
    } else {
      payload.username = userForm.username.trim()
      payload.password = userForm.password
      await request('/api/users', jsonOptions('POST', payload))
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
    await request(`/api/users/${u.id}`, jsonOptions('PUT', { enabled: !u.enabled }))
    notify(u.enabled ? '账号已停用' : '账号已启用')
    await refresh()
  } catch (e) { notify(e.message, 'err') }
}

async function removeUser(u) {
  if (!confirm(`确认删除用户「${u.username}」？该用户的登录会话与站点授权会一并清除。`)) return
  try {
    await request(`/api/users/${u.id}`, { method: 'DELETE' })
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

onMounted(bootstrap)
</script>

<template>
  <div v-if="!authReady" class="boot">正在检查登录状态…</div>

  <!-- 登录 / 首次注册 -->
  <div v-else-if="!me" class="login-wrap">
    <div class="login-card">
      <h1>AssetSpider</h1>
      <p class="muted">视频站点采集管理系统</p>
      <div v-if="!initialized" class="first-hint">
        <strong>系统尚未初始化</strong><br />
        第一个登录的账号将<strong>自动注册为管理员</strong>，拥有全部权限；之后可创建用户并分配可见站点。
      </div>
      <form @submit.prevent="doLogin">
        <input v-model="loginForm.username" placeholder="用户名" autocomplete="username" required />
        <input v-model="loginForm.password" type="password" placeholder="密码" autocomplete="current-password" required />
        <button type="submit" :disabled="loginBusy">
          {{ loginBusy ? '登录中…' : (initialized ? '登录' : '注册并登录') }}
        </button>
      </form>
      <p v-if="loginError" class="error">{{ loginError }}</p>
      <p class="hint">密码以 bcrypt 哈希存储；登录令牌仅保存在本机浏览器，退出或过期后需重新登录。</p>
    </div>
  </div>

  <!-- 主界面 -->
  <main v-else class="page">
    <header class="tophead">
      <div>
        <h1>AssetSpider</h1>
        <p>视频站点采集 · JSON 接口 · 全量 / 增量 · 全字段入库 · Vue 3 + Gin + GORM/SQLite</p>
      </div>
      <div class="userbar">
        <span class="who">
          <b>{{ currentUsername }}</b>
          <span class="badge" :class="isAdmin ? 'on' : 'off'">{{ isAdmin ? '管理员' : '普通用户' }}</span>
        </span>
        <button class="ghost mini" @click="pwdModal = true">修改密码</button>
        <button class="ghost mini" @click="doLogout">退出登录</button>
      </div>
    </header>

    <div class="stats">
      <span class="db">DB: {{ health.database || '加载中…' }}</span>
      <span class="stat">可见站点 <b>{{ sources.length }}</b></span>
      <span class="stat">可见影片 <b>{{ videoTotal }}</b></span>
      <span class="stat">字段 <b>{{ fieldMeta.total || '-' }}</b></span>
      <span v-if="!isAdmin" class="stat">站点权限 <b>受限</b></span>
    </div>

    <div v-if="notice" class="notice" :class="notice.type">{{ notice.text }}</div>
    <p v-if="error" class="error">{{ error }}</p>

    <nav class="tabs">
      <button :class="{ active: tab === 'sources' }" @click="tab = 'sources'">站点配置</button>
      <button :class="{ active: tab === 'videos' }" @click="tab = 'videos'; loadVideos()">采集数据</button>
      <button :class="{ active: tab === 'jobs' }" @click="tab = 'jobs'; loadJobs()">采集记录</button>
      <button v-if="isAdmin" :class="{ active: tab === 'users' }" @click="tab = 'users'; loadUsers()">用户管理</button>
      <button class="ghost refresh" @click="refresh">刷新</button>
    </nav>

    <!-- 站点配置 -->
    <template v-if="tab === 'sources'">
      <section class="card">
        <div class="list-head">
          <h2>批量采集</h2>
          <span class="muted">{{ isAdmin ? '对全部启用站点执行' : '仅对你可见的启用站点执行' }}，SQLite 下串行执行</span>
        </div>
        <div class="actions">
          <button @click="crawlAll('full')" :disabled="busy">全量采集</button>
          <button class="ghost" @click="crawlAll('incremental')" :disabled="busy">增量采集</button>
          <span v-if="busy" class="muted">采集中，请稍候…</span>
        </div>
      </section>

      <section v-if="isAdmin" class="card">
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
        <div class="list-head">
          <h2>站点列表</h2>
          <span class="muted">共 {{ sources.length }} 个{{ isAdmin ? '' : '（仅显示你有权限的站点）' }}</span>
        </div>
        <p v-if="!sources.length" class="empty">
          {{ isAdmin ? '还没有站点，先在上方添加一个 JSON 采集接口。' : '管理员尚未给你分配任何可见站点。' }}
        </p>
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
                <td><span class="badge" :class="s.enabled ? 'on' : 'off'">{{ s.enabled ? '启用' : '停用' }}</span></td>
                <td class="mono">{{ s.videoCount }}</td>
                <td class="muted">{{ formatTime(s.lastSyncAt) }}</td>
                <td class="muted">
                  <span class="badge" :class="s.lastStatus">{{ statusLabel(s.lastStatus) }}</span>
                  <div class="mono tiny">{{ s.lastCursor || '-' }}</div>
                </td>
                <td class="ops">
                  <button class="mini" @click="crawlOne(s, 'full')" :disabled="busy">全量</button>
                  <button class="mini ghost" @click="crawlOne(s, 'incremental')" :disabled="busy">增量</button>
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

    <!-- 采集数据 -->
    <template v-if="tab === 'videos'">
      <section class="card">
        <div class="toolbar">
          <input v-model="videoQuery" placeholder="按名称 / 别名 / 演员 / 导演 / 分类搜索" @keyup.enter="searchVideos" />
          <select v-model="videoSource" @change="searchVideos">
            <option value="">全部可见站点</option>
            <option v-for="s in sources" :key="s.id" :value="s.id">{{ s.name }}</option>
          </select>
          <button @click="searchVideos">搜索</button>
          <span class="muted">共 {{ videoTotal }} 条</span>
        </div>
        <p v-if="!videos.length" class="empty">暂无可见的采集数据。</p>
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
        <div class="list-head"><h2>采集记录</h2><span class="muted">最近 50 条（仅可见站点）</span></div>
        <p v-if="!jobs.length" class="empty">暂无可见的采集记录。</p>
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

    <!-- 用户管理（管理员） -->
    <template v-if="tab === 'users' && isAdmin">
      <section class="card">
        <h2>{{ userEditing ? `编辑用户「${userForm.username}」` : '新增用户' }}</h2>
        <form class="user-form" @submit.prevent="saveUser">
          <input v-model="userForm.username" placeholder="用户名 *" :disabled="userEditing" :required="!userEditing" />
          <input v-model="userForm.password" type="password" :placeholder="userEditing ? '新密码（留空不改）' : '密码 *（至少 6 位）'" :required="!userEditing" />
          <label class="field">角色
            <select v-model="userForm.role">
              <option value="user">普通用户</option>
              <option value="admin">管理员</option>
            </select>
          </label>
          <label class="check"><input v-model="userForm.enabled" type="checkbox" /> 启用</label>
          <button type="submit">{{ userEditing ? '保存修改' : '创建用户' }}</button>
          <button v-if="userEditing" type="button" class="ghost" @click="resetUserForm">取消</button>
        </form>
        <div class="picker">
          <div class="picker-head">
            <span class="muted">可见站点授权</span>
            <span class="hint">普通用户只能看见被勾选的站点；管理员默认可见全部，无需授权。</span>
          </div>
          <div class="picker-items">
            <label v-for="s in sources" :key="s.id" class="check">
              <input type="checkbox" :value="s.id" v-model="userForm.sourceIds" /> {{ s.name }}
            </label>
            <p v-if="!sources.length" class="empty">暂无站点可分配，请先到「站点配置」添加。</p>
          </div>
        </div>
        <p class="hint">创建/修改后立即生效，无需用户重新登录。停用、改密或变更角色会使该用户的现有会话失效。</p>
      </section>

      <section class="card">
        <div class="list-head"><h2>用户列表</h2><span class="muted">共 {{ users.length }} 个账号</span></div>
        <div class="table-wrap">
          <table>
            <thead>
              <tr><th>ID</th><th>用户名</th><th>角色</th><th>状态</th><th>可见站点</th><th>最后登录</th><th>操作</th></tr>
            </thead>
            <tbody>
              <tr v-for="u in users" :key="u.id">
                <td class="mono">{{ u.id }}</td>
                <td class="host">{{ u.username }}<span v-if="u.id === me.user?.id" class="tiny muted">（我）</span></td>
                <td><span class="badge" :class="u.role === 'admin' ? 'on' : 'off'">{{ u.role === 'admin' ? '管理员' : '普通用户' }}</span></td>
                <td><span class="badge" :class="u.enabled ? 'on' : 'off'">{{ u.enabled ? '启用' : '停用' }}</span></td>
                <td class="grants" :title="visibleLabel(u)">{{ short(visibleLabel(u), 40) }}</td>
                <td class="muted">{{ formatTime(u.lastLoginAt) }}</td>
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

    <!-- 修改密码 -->
    <div v-if="pwdModal" class="modal-mask" @click.self="pwdModal = false">
      <div class="modal small">
        <div class="modal-head">
          <div><h3>修改密码</h3><p class="muted">修改后该账号的其他登录会话将全部失效</p></div>
          <button class="ghost" @click="pwdModal = false">关闭</button>
        </div>
        <form class="pwd-form" @submit.prevent="doChangePassword">
          <input v-model="pwdForm.oldPassword" type="password" placeholder="原密码 *" required />
          <input v-model="pwdForm.newPassword" type="password" placeholder="新密码 *（至少 6 位）" minlength="6" required />
          <div class="actions">
            <button type="submit">确认修改</button>
            <button type="button" class="ghost" @click="pwdModal = false">取消</button>
          </div>
        </form>
      </div>
    </div>
  </main>
</template>

<style>
:root { color-scheme: light; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif; }
* { box-sizing: border-box; }
body { margin: 0; background: #f5f6f8; color: #1f2329; }
.boot { display: flex; align-items: center; justify-content: center; min-height: 100vh; color: #86909c; }
.login-wrap { min-height: 100vh; display: flex; align-items: center; justify-content: center; padding: 24px; background: linear-gradient(160deg, #eef2ff 0%, #f5f6f8 60%); }
.login-card { width: min(420px, 100%); background: #fff; border: 1px solid #e5e6eb; border-radius: 14px; padding: 32px; box-shadow: 0 12px 40px rgba(31,35,41,.08); }
.login-card h1 { margin: 0; font-size: 26px; }
.login-card > p.muted { margin: 6px 0 20px; font-size: 13px; }
.login-card form { display: flex; flex-direction: column; gap: 12px; }
.login-card form input { width: 100%; flex: none; padding: 10px 12px; }
.login-card form button { width: 100%; padding: 10px; margin-top: 4px; }
.first-hint { background: #e8f3ff; color: #165dff; border-radius: 8px; padding: 12px 14px; font-size: 13px; line-height: 1.8; margin-bottom: 18px; }
.login-card .hint { margin: 16px 0 0; }
.page { max-width: 1180px; margin: 0 auto; padding: 28px 20px 64px; }
.tophead { display: flex; justify-content: space-between; align-items: flex-start; gap: 20px; flex-wrap: wrap; }
header h1 { margin: 0; font-size: 28px; }
header p { margin: 6px 0 12px; color: #646a73; }
.userbar { display: flex; align-items: center; gap: 8px; padding-top: 4px; }
.who { display: inline-flex; align-items: center; gap: 8px; font-size: 13px; margin-right: 4px; }
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
form, .source-form, .user-form { display: flex; gap: 10px; flex-wrap: wrap; align-items: center; }
input, select { padding: 8px 10px; border: 1px solid #c9cdd4; border-radius: 6px; font-size: 14px; background: #fff; }
input { flex: 1 1 190px; min-width: 0; }
input:disabled { background: #f2f3f5; color: #86909c; }
input.wide { flex: 2 1 380px; }
.field { display: flex; align-items: center; gap: 6px; font-size: 13px; color: #4e5969; }
.field input { width: 90px; flex: none; }
.field select { width: auto; }
.check { display: flex; align-items: center; gap: 6px; font-size: 13px; color: #4e5969; }
.check input { flex: none; }
button { padding: 8px 16px; border: 0; border-radius: 6px; background: #165dff; color: #fff; cursor: pointer; font-size: 14px; white-space: nowrap; }
button:disabled { opacity: .55; cursor: default; }
button.ghost { background: #f2f3f5; color: #1f2329; }
button.mini { padding: 5px 9px; font-size: 12px; }
button.danger { background: #fff1f0; color: #f53f3f; }
.hint { margin: 12px 0 0; font-size: 12px; color: #86909c; line-height: 1.8; }
code { background: #f2f3f5; padding: 1px 5px; border-radius: 4px; }
.picker { margin-top: 16px; border-top: 1px dashed #e5e6eb; padding-top: 14px; }
.picker-head { display: flex; flex-direction: column; gap: 2px; margin-bottom: 10px; }
.picker-head .hint { margin: 0; }
.picker-items { display: flex; flex-wrap: wrap; gap: 10px 20px; }
.table-wrap { overflow-x: auto; }
table { width: 100%; border-collapse: collapse; font-size: 13px; }
th, td { text-align: left; padding: 10px 8px; border-bottom: 1px solid #f0f1f3; white-space: nowrap; }
th { color: #86909c; font-weight: 500; }
.host { font-weight: 600; }
.url, .msg, .grants { max-width: 260px; overflow: hidden; text-overflow: ellipsis; }
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
.modal.small { width: min(440px, 100%); }
.modal-head { display: flex; justify-content: space-between; align-items: flex-start; gap: 16px; padding: 18px 22px; border-bottom: 1px solid #e5e6eb; }
.modal-head h3 { margin: 0 0 6px; font-size: 17px; }
.modal-head p { margin: 0; font-size: 12px; }
.modal-tabs { display: flex; gap: 8px; align-items: center; padding: 12px 22px; border-bottom: 1px solid #e5e6eb; flex-wrap: wrap; }
.modal-tabs button { padding: 5px 12px; background: #f2f3f5; color: #4e5969; font-size: 13px; }
.modal-tabs button.active { background: #165dff; color: #fff; }
.modal-tabs .check { margin-left: auto; }
.modal-body { overflow-y: auto; padding: 6px 22px 22px; }
.pwd-form { display: flex; flex-direction: column; gap: 12px; padding: 20px 22px 24px; }
.pwd-form input { width: 100%; flex: none; }
.pwd-form .actions { margin-top: 4px; }
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
