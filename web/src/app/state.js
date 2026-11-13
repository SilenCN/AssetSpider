// 应用共享状态：只放 ref / computed 与最基础的本地存储读写，不含业务逻辑。
// 各领域模块（认证、资源库、分类、站点、任务……）从这里取状态，再把行为
// 挂到同一个 ctx 对象上，最后由 app/index.js 一次性 provide 给页面组件。
import { computed, reactive, ref } from 'vue'
import { DEFAULT_TAB, detailIdFromHash, normalizeTab } from '../layout.js'

export const TOKEN_KEY = 'assetspider_token'
export const TAB_KEY = 'assetspider_tab'
export const SIDEBAR_KEY = 'assetspider_sidebar_collapsed'

// 侧边栏布局：记住当前页面（URL hash 优先，localStorage 兜底）与折叠状态。
export function initialTab() {
  const hash = globalThis.location?.hash || ''
  // 影片详情页地址（#/video/<id>）以资源库为底层页面，登录后按 id 恢复详情。
  if (detailIdFromHash(hash)) return DEFAULT_TAB
  const fromHash = normalizeTab(hash)
  if (fromHash) return fromHash
  // URL 显式带了但非法的 hash：忽略 localStorage，直接回退到主页面，
  // 避免一个无效链接把用户送到上一次访问的无关页面。
  if (hash) return DEFAULT_TAB
  return normalizeTab(localStorage.getItem(TAB_KEY)) || DEFAULT_TAB
}

// 站点配置表单的空白值（新增 / 编辑共用）。
export const emptyForm = () => ({
  id: null, name: '', apiBase: '', format: 'seacms_json', extraParams: '',
  incrementalHours: 24, maxPages: 0, enabled: true,
  scheduleEnabled: false, scheduleCron: '',
  parseApi: '', playDomain: '', picApi: '', picDomain: '',
})

// 订阅表单的空白值（新增 / 编辑共用）。间隔 24 小时是个折中：站点配置一般一天
// 变不了几次，0 则等于「只手动刷新」。
export const emptySubForm = () => ({ id: null, name: '', url: '', intervalHours: 24, enabled: true })

// 用户表单的空白值（新增 / 编辑共用）。
export const emptyUserForm = () => ({
  id: null, username: '', password: '', role: 'user', enabled: true, sourceIds: [],
})

export function createState() {
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

  const tab = ref(initialTab()) // 资源库为主：默认 / 回退页都是资源库
  const health = ref({})
  const sources = ref([])
  const videos = ref([])
  const categories = ref([])
  const categoryTree = ref([])
  const jobs = ref([])
  const tasks = ref([])
  const users = ref([])
  const capacity = ref({ maxConcurrent: 3, running: 0, queued: 0 })
  // 系统设置：采集运行时配置（并发上限已并入此处，任务页只做只读展示）
  const settings = ref(null)
  const settingsForm = ref({})
  // 数据库配置表单：后台可编辑，提交后写回配置文件、重启服务生效。
  // DSN 不回显明文，表单里留空表示「保留现有连接串」。
  const dbForm = ref({
    driver: 'sqlite', path: '', dsn: '',
    maxOpenConns: 16, maxIdleConns: 8, connMaxLifetimeMin: 30,
  })
  const settingsDirty = ref(false)
  const settingsSaving = ref(false)
  const error = ref('')
  const notice = ref(null)
  let noticeTimer = null

  function notify(text, type = 'ok') {
    notice.value = { text, type }
    clearTimeout(noticeTimer)
    noticeTimer = setTimeout(() => { notice.value = null }, 7000)
  }

  // ---------- 资源库筛选 ----------
  // 筛选维度只保留关键词 + 站点 + 分类：标签 / 地域 / 年份筛选与评分排序已移除，
  // 影片上的这些原始字段仍保留，作为卡片与详情页的纯信息展示。
  const videoQuery = ref('')
  const videoSource = ref('')
  const videoCategory = ref('')
  const videoSort = ref('id_desc')
  const videoPage = ref(1)
  const videoSize = ref(24)
  // 列表接口只返回 items/page/size：不返回总数、也不返回 hasMore。
  // 前端按「本页条数 >= 分页条数」认为还有下一页；正好整除时会多发一次空页请求为止。
  const videoHasMore = ref(true)
  // 资源库改成「底部无限滚动」：列表是累计的，这里记是否还有下一页。
  const videoLoading = ref(false)     // 首屏 / 搜索 / 切筛选：整表重载
  const videoLoadingMore = ref(false) // 滚到底加载下一页
  const videoEnd = computed(() => !videoHasMore.value)
  const videoSentinel = ref(null)     // 底部哨兵元素，由 VideosView 挂载

  // ---------- 影片详情与播放 ----------
  const fieldMeta = ref({ groups: [], fields: [], total: 0 })
  // 列表只返回卡片投影，点开影片后按 id 拉完整详情；加载期间显示占位，避免重复点击。
  const detailLoading = ref(false)
  const detail = ref(null)
  const detailTab = ref('fields')
  const onlyNonEmpty = ref(true)
  // 从资源库点开影片前的滚动位置，返回时恢复到原处（避免回到顶部丢失浏览进度）。
  const listScrollY = ref(0)
  const playerEl = ref(null)
  const activeGroup = ref(0)
  const activeEp = ref(0)
  const playError = ref('')

  // ---------- 登录 / 会话 ----------
  const loginForm = reactive({ username: '', password: '' })
  const loginBusy = ref(false)
  const loginError = ref('')
  const pwdModal = ref(false)
  const pwdForm = reactive({ oldPassword: '', newPassword: '' })

  // ---------- 站点配置 ----------
  const form = reactive(emptyForm())
  const sourceOpen = ref(false)
  const importOpen = ref(false)
  const importBusy = ref(false)
  const importFileName = ref('')
  const importRaw = ref('')
  const importPreview = ref([])
  const importFileEl = ref(null)

  // ---------- 站点配置订阅 ----------
  const subscriptions = ref([])
  const subForm = reactive(emptySubForm())
  const subOpen = ref(false)
  const subBusy = ref(false)
  const subRefreshing = ref(null) // 正在刷新的订阅 ID，按钮据此禁用

  // ---------- 用户管理 ----------
  const userForm = reactive(emptyUserForm())
  const userFormOpen = ref(false)

  // ---------- 侧边栏 ----------
  const sidebarCollapsed = ref(localStorage.getItem(SIDEBAR_KEY) === '1')
  const sidebarMobileOpen = ref(false)

  return {
    TOKEN_KEY, TAB_KEY, SIDEBAR_KEY, emptyForm, emptyUserForm, emptySubForm,
    token, me, authReady, initialized, isAdmin, currentUsername, clearAuth, setAuth,
    tab, health, sources, videos, categories, categoryTree, jobs, tasks, users, capacity,
    settings, settingsForm, dbForm, settingsDirty, settingsSaving, error, notice, notify,
    videoQuery, videoSource, videoCategory, videoSort,
    videoPage, videoSize, videoHasMore, videoLoading, videoLoadingMore,
    videoEnd, videoSentinel,
    fieldMeta, detailLoading, detail, detailTab, onlyNonEmpty, listScrollY,
    playerEl, activeGroup, activeEp, playError,
    loginForm, loginBusy, loginError, pwdModal, pwdForm,
    form, sourceOpen, importOpen, importBusy, importFileName, importRaw, importPreview, importFileEl,
    subscriptions, subForm, subOpen, subBusy, subRefreshing,
    userForm, userFormOpen, sidebarCollapsed, sidebarMobileOpen,
  }
}
