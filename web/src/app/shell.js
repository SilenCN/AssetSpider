// 应用外壳：数据刷新、页签与 hash 路由、侧边栏、弹窗快捷键、采集数据轮询。
import { computed, onBeforeUnmount, onMounted, watch } from 'vue'
import {
  NAV_ITEMS, DEFAULT_TAB, normalizeTab, tabToHash, navItemsFor, detailIdFromHash,
} from '../layout.js'

export function createShell(ctx) {
  async function loadHealth() {
    ctx.health.value = await ctx.request('/api/system/v1/health')
  }
  async function loadSources() {
    ctx.sources.value = (await ctx.request('/api/sources/v1')) || []
  }
  async function loadCategories() {
    ctx.categories.value = (await ctx.request('/api/categories/v1')) || []
  }
  async function loadCategoryTree() {
    ctx.categoryTree.value = (await ctx.request('/api/categories/v1/tree')) || []
  }
  async function loadSubscriptions() {
    ctx.subscriptions.value = (await ctx.request('/api/subscriptions/v1')) || []
  }
  async function loadTags() {
    ctx.tags.value = (await ctx.request('/api/tags/v1')) || []
  }

  async function refresh() {
    ctx.error.value = ''
    try {
      const pending = [
        loadHealth(), loadSources(), ctx.loadVideos(), ctx.loadJobs(),
        loadCategories(), loadCategoryTree(), loadTags(), ctx.loadAreas(), ctx.loadYears(), ctx.loadTasks(),
        loadSubscriptions(),
      ]
      if (ctx.isAdmin.value) pending.push(ctx.loadUsers(), ctx.loadSettings())
      await Promise.all(pending)
    } catch (e) {
      ctx.error.value = e.message
    }
  }

  // ---------- 侧边栏导航 ----------
  function toggleSidebar() {
    ctx.sidebarCollapsed.value = !ctx.sidebarCollapsed.value
    localStorage.setItem(ctx.SIDEBAR_KEY, ctx.sidebarCollapsed.value ? '1' : '')
  }

  // 导航项：按权限过滤，并带上各页面的实时计数徽标。
  const navItems = computed(() => {
    const counts = {
      videos: ctx.visibleVideoTotal.value, sources: ctx.sources.value.length,
      categories: ctx.catTotal.value, jobs: ctx.jobs.value.length, users: ctx.users.value.length,
    }
    return navItemsFor(ctx.isAdmin.value).map(i => ({ ...i, count: counts[i.key] || 0 }))
  })
  // 顶部栏始终显示当前页签名。详情页是资源库的下钻页（tab 仍为 videos），
  // 因此标题保持「资源库」，不切换成影片名；影片名在详情页头部 h2 展示。
  const currentPageTitle = computed(() =>
    NAV_ITEMS.find(i => i.key === ctx.tab.value)?.label || '资源库')

  // 点击侧边栏：切换页面并按需加载该页数据。详情页是资源库的下钻页，
  // 点任意菜单都离开它（播放器一并销毁）。
  function switchTab(key) {
    let k = normalizeTab(key) || DEFAULT_TAB
    if (k === 'users' && !ctx.isAdmin.value) k = DEFAULT_TAB
    ctx.leaveDetail()
    ctx.tab.value = k
    if (k === 'videos') ctx.loadVideos().catch(e => { ctx.error.value = e.message })
    else if (k === 'categories') loadCategories().catch(e => { ctx.error.value = e.message })
    else if (k === 'users') ctx.loadUsers().catch(e => { ctx.error.value = e.message })
  }

  // tab 变化时：同步 URL hash 与 localStorage（刷新/重开可恢复）、收起移动端抽屉，
  // 并对采集类页面立刻补一次轮询数据。用 replaceState 写 hash 不产生历史噪音、也不回环。
  watch(ctx.tab, (v) => {
    // 直接赋值 tab 的路径（分类树 / 标签跳转）也要离开详情页。
    if (ctx.detail.value) ctx.leaveDetail()
    const h = tabToHash(v)
    if (location.hash !== h) history.replaceState(null, '', h)
    localStorage.setItem(ctx.TAB_KEY, v)
    ctx.sidebarMobileOpen.value = false
    if (!ctx.me.value || !collectTabs.includes(v)) return
    refreshCollectData().catch(() => {})
  })

  // 用户手动改 hash / 浏览器前进后退时反向同步页面，并补齐该页数据。
  function onHashChange() {
    const did = detailIdFromHash(location.hash)
    if (did != null) {
      // 前进 / 手动输入到某影片：按 ID 恢复详情页。
      if (Number(ctx.detail.value?.id) !== did) ctx.openDetailById(did)
      return
    }
    // 离开详情页（返回按钮、浏览器后退、切到别的页面）。
    // 只有「确实从详情页退回资源库」才恢复滚动位置：closeDetail 会先 leaveDetail
    // 把 detail 置空、其触发的 hashchange 因此不会走进来；切菜单离开详情也会先把
    // detail 置空。故这里 detail 非空即代表浏览器后退，需要恢复。
    const leavingDetail = !!ctx.detail.value
    ctx.leaveDetail()
    const k = normalizeTab(location.hash)
    if (leavingDetail && k === 'videos') ctx.restoreListScroll()
    if (!k) {
      // 无效 hash：地址栏纠正回当前页，页面保持不变（不把用户踢走）。
      if (location.hash !== tabToHash(ctx.tab.value)) history.replaceState(null, '', tabToHash(ctx.tab.value))
      return
    }
    if (k === ctx.tab.value) return
    ctx.tab.value = k
    if (k === 'videos') ctx.loadVideos().catch(() => {})
    else if (k === 'categories') loadCategories().catch(() => {})
    else if (k === 'users') ctx.loadUsers().catch(() => {})
  }

  // 普通用户若从链接直接落到「用户管理」，登录后校正回资源库；
  // 刷新 / 分享链接直达 #/video/<id> 时，登录态就绪后按 ID 恢复详情。
  watch(ctx.me, () => {
    if (ctx.tab.value === 'users' && !ctx.isAdmin.value) ctx.tab.value = DEFAULT_TAB
    const did = detailIdFromHash(location.hash)
    if (ctx.me.value && did != null && Number(ctx.detail.value?.id) !== did) ctx.openDetailById(did)
  })

  // Esc 关闭当前弹窗；详情页是页面而非弹窗，Esc 仅作为「返回资源库」的快捷方式。
  function onEsc(e) {
    if (e.key !== 'Escape') return
    if (ctx.importOpen.value) { ctx.closeImport(); return }
    if (ctx.subOpen.value) { ctx.closeSubForm(); return }
    if (ctx.sourceOpen.value) { ctx.closeSourceForm(); return }
    if (ctx.userFormOpen.value) { ctx.closeUserForm(); return }
    if (ctx.pwdModal.value) { ctx.pwdModal.value = false; return }
    if (ctx.detail.value) ctx.closeDetail()
  }

  // 采集数据（站点状态 / 实时任务 / 采集记录）只在这两个页签自动刷新。
  // 停留在资源库、视频分类、用户管理时不发轮询请求，避免无谓流量和其它页面被反复重绘。
  const collectTabs = ['sources', 'jobs']
  function refreshCollectData() {
    // 站点配置页看任务卡片与站点「上次采集」状态；采集记录页看记录（含运行中的 live 进度）。
    if (ctx.tab.value === 'sources') return Promise.all([ctx.loadTasks(), loadSources(), loadSubscriptions()])
    if (ctx.tab.value === 'jobs') return ctx.loadJobs()
    return Promise.resolve()
  }

  function onVisible() {
    if (!document.hidden && ctx.me.value && collectTabs.includes(ctx.tab.value)) {
      refreshCollectData().catch(() => {})
    }
  }

  let taskTimer = null
  onMounted(() => window.addEventListener('keydown', onEsc))
  onBeforeUnmount(() => window.removeEventListener('keydown', onEsc))

  onMounted(async () => {
    await ctx.bootstrap()
    taskTimer = setInterval(() => {
      if (!ctx.me.value || document.hidden || !collectTabs.includes(ctx.tab.value)) return
      refreshCollectData().catch(() => {})
    }, 1500)
  })
  onBeforeUnmount(() => { clearInterval(taskTimer) })

  onMounted(() => {
    // 首次进入时 tab 是初始值、watch 不会触发，这里把空 / 非法 hash 规范化一次，
    // 保证地址栏与实际页面一致。详情地址是合法页面，不能被覆盖。
    if (!detailIdFromHash(location.hash) && normalizeTab(location.hash) !== ctx.tab.value) {
      history.replaceState(null, '', tabToHash(ctx.tab.value))
    }
    window.addEventListener('hashchange', onHashChange)
  })
  onBeforeUnmount(() => window.removeEventListener('hashchange', onHashChange))

  onMounted(() => document.addEventListener('visibilitychange', onVisible))
  onBeforeUnmount(() => document.removeEventListener('visibilitychange', onVisible))

  return {
    loadHealth, loadSources, loadSubscriptions, loadCategories, loadCategoryTree, loadTags, refresh,
    toggleSidebar, navItems, currentPageTitle, switchTab, onHashChange, onEsc,
    collectTabs, refreshCollectData, onVisible,
  }
}
