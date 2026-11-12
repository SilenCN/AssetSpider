// 装配契约测试：保证 app/index.js 组装出的 ctx 覆盖了页面组件 inject('app')
// 所依赖的全部键，并且关键动作可用（登录 → 拉列表 → 详情状态）。
// 运行：cd web && npm test
import test from 'node:test'
import assert from 'node:assert/strict'

// ---------- 浏览器 API 的极简替身（只覆盖装配与请求路径用到的部分）----------
const store = new Map()
globalThis.localStorage = {
  getItem: k => (store.has(k) ? store.get(k) : null),
  setItem: (k, v) => store.set(k, String(v)),
  removeItem: k => store.delete(k),
}
globalThis.window = { scrollTo() {}, scrollY: 0 }
globalThis.location = { hash: '' }
// 通知条会在 7 秒后自动消失，测试进程会为此空等 7 秒；这里把长延时压到 5ms。
const realSetTimeout = globalThis.setTimeout
globalThis.setTimeout = (fn, ms, ...args) => realSetTimeout(fn, Math.min(ms, 5), ...args)
globalThis.history = { replaceState() {}, pushState() {} }

const { createApp } = await import('./app/index.js')

// 页面组件 inject('app') 解构出来的键（与 views/*.vue 保持一致）。
const REQUIRED = [
  'TOKEN_KEY', 'token', 'me', 'authReady', 'initialized', 'isAdmin', 'currentUsername',
  'clearAuth', 'setAuth', 'tab', 'health', 'sources', 'videos', 'categories', 'categoryTree',
  'jobs', 'tasks', 'users', 'capacity', 'settings', 'settingsForm', 'settingsDirty',
  'settingsSaving', 'loadSettings', 'saveSettings', 'error', 'notice', 'notify',
  'videoQuery', 'videoSource', 'videoCategory', 'videoSort',
  'visibleCategories', 'hasActiveFilters',
  'videoPage', 'videoSize', 'videoLoading', 'videoLoadingMore', 'videoEnd',
  'videoSentinel', 'visibleVideoTotal', 'request', 'jsonOptions', 'loadHealth', 'loadSources',
  'fetchVideos', 'kickSentinel', 'loadVideos', 'loadMoreVideos',
  'loadJobs', 'loadTasks', 'loadCategories', 'loadCategoryTree', 'loadUsers',
  'categoryName', 'activeTaskOf', 'queuedCountOf', 'showCategoryVideos',
  'catCollapsed', 'isCatCollapsed', 'toggleCat', 'catKey', 'siteNameOf', 'categoryForest',
  'catTotal', 'catVideoCount', 'catPath', 'catSelectLabel', 'applyFilterLinkage',
  'onFilterChange', 'applyLinkage', 'clearAllFilters', 'toggleAllCats', 'fieldMeta',
  'detailLoading', 'detail', 'detailTab', 'onlyNonEmpty', 'loadFieldMeta', 'hasValue',
  'nonEmptyCount', 'groupedFields', 'extraPairs', 'playGroups', 'posterOf', 'markPicFailed',
  'episodeLabel', 'scoreOf', 'metaLine', 'tagListOf', 'playerEl', 'activeGroup', 'activeEp',
  'currentGroup', 'currentEpisodes', 'currentEpisode', 'currentUrl', 'detailSource',
  'parseEnabled', 'useFrame', 'isHls', 'isDirectVideo', 'isUnknownStream', 'canPlay',
  'playError', 'onVideoLoaded', 'hlsErrorText', 'videoErrorText', 'onVideoError',
  'destroyPlayer', 'mountPlayer', 'chooseGroup', 'chooseEp', 'switchDetailTab',
  'closeDetail', 'openDetail', 'onEsc', 'emptyForm', 'emptyUserForm', 'form',
  'editing', 'sourceOpen', 'resetForm', 'sourcePayload', 'openSourceForm', 'closeSourceForm',
  'editSource', 'importOpen', 'importBusy', 'importFileName', 'importRaw', 'importPreview',
  'importFileEl', 'importPreviewShown', 'exportSources', 'pickImportFile', 'onImportFile',
  'closeImport', 'confirmImport', 'saveSource', 'removeSource', 'clearSourceData', 'toggleSource',
  'startTask', 'crawlAll', 'pauseTask', 'resumeTask', 'retryTask', 'rerunTask', 'barWidth',
  'isIndeterminate', 'fmtEta', 'taskStatusText', 'searchVideos', 'debouncedSearch',
  'clearSearch', 'formatTime', 'modeLabel', 'statusLabel', 'short', 'stat', 'jobMessage',
  'loginForm', 'loginBusy', 'loginError', 'doLogin', 'doLogout', 'pwdModal', 'pwdForm',
  'doChangePassword', 'bootstrap', 'userForm', 'userEditing', 'userFormOpen', 'openUserForm',
  'closeUserForm', 'resetUserForm', 'editUser', 'saveUser', 'toggleUser', 'removeUser',
  'visibleLabel', 'collectTabs', 'refreshCollectData', 'onVisible', 'refresh', 'switchTab',
  'navItems', 'currentPageTitle', 'toggleSidebar', 'sidebarCollapsed', 'sidebarMobileOpen',
  'loadHls', 'emptySubForm', 'subscriptions', 'subForm', 'subOpen', 'subBusy', 'subRefreshing',
  'subEditing', 'subNameOf', 'subStatusText', 'subIntervalText', 'openSubForm',
  'closeSubForm', 'saveSubscription', 'refreshSubscription', 'toggleSubscription',
  'removeSubscription', 'loadSubscriptions',
]

test('createApp 暴露页面组件依赖的全部键', () => {
  const app = createApp()
  const missing = REQUIRED.filter(k => app[k] === undefined)
  assert.deepEqual(missing, [], `缺少键：${missing.join(', ')}`)
})

test('关键动作是可调用的函数，关键状态是 ref', () => {
  const app = createApp()
  for (const k of ['request', 'jsonOptions', 'doLogin', 'loadVideos', 'openDetail', 'saveSource', 'startTask']) {
    assert.equal(typeof app[k], 'function', `${k} 应该是函数`)
  }
  for (const k of ['token', 'me', 'videos']) {
    assert.ok('value' in app[k], `${k} 应该是 ref`)
  }
})

test('登录后拉取资源库列表，状态落到对应 ref 上', async () => {
  const routes = {
    '/api/auth/v1/sessions': { token: 'tk-1', user: { id: 1, username: 'admin' }, isAdmin: true },
    '/api/videos/v1': { items: [{ id: 7, vodName: '测试影片' }], page: 1, size: 24 },
    '/api/system/v1/health': { database: 'ok' },
    '/api/subscriptions/v1': [],
  }
  const calls = []
  globalThis.fetch = async (path) => {
    calls.push(path)
    const key = Object.keys(routes).find(k => String(path).startsWith(k))
    return {
      ok: true, status: 200,
      json: async () => (key ? routes[key] : { items: [] }),
    }
  }

  const app = createApp()
  app.loginForm.username = 'admin'
  app.loginForm.password = 'admin123456'
  await app.doLogin()

  assert.equal(app.token.value, 'tk-1')
  assert.equal(app.me.value.user.username, 'admin')
  assert.equal(app.isAdmin.value, true)
  assert.equal(app.videos.value[0].vodName, '测试影片')
  assert.ok(calls.some(p => p.startsWith('/api/videos/v1')), '应当拉取资源库列表')
})

test('筛选联动：从分类跳转后站点校正到分类所属站点', () => {
  const app = createApp()
  app.categories.value = [{ id: 10, name: '动作片', sourceId: 1 }, { id: 20, name: '喜剧片', sourceId: 2 }]
  app.videoCategory.value = '10'
  app.videoSource.value = '2'
  app.applyLinkage()
  // 分类是筛选主轴：与站点冲突时，站点被纠正到分类所属的站点。
  assert.equal(app.videoSource.value, '1')
  assert.equal(app.videoCategory.value, '10')
})

test('搜索输入防抖合并连续输入，clearSearch 只清搜索词', async () => {
  const calls = []
  globalThis.fetch = async (path) => {
    calls.push(String(path))
    return { ok: true, status: 200, json: async () => ({ items: [], page: 1, size: 24 }) }
  }
  const app = createApp()
  app.videoQuery.value = '科幻'
  app.videoSource.value = '1'
  app.videoCategory.value = '2'
  app.debouncedSearch()
  app.debouncedSearch()
  // 文件顶部把长延时压到 5ms，等 20ms 足以让防抖落地。
  await new Promise(r => realSetTimeout(r, 20))
  const videoCalls = () => calls.filter(p => p.startsWith('/api/videos/v1')).length
  assert.equal(videoCalls(), 1, '连续输入只应触发一次检索')

  app.clearSearch()
  assert.equal(app.videoQuery.value, '')
  assert.equal(app.videoSource.value, '1', '清除搜索词不应清掉站点筛选')
  assert.equal(app.videoCategory.value, '2', '清除搜索词不应清掉分类筛选')
  await new Promise(r => realSetTimeout(r, 20))
  assert.equal(videoCalls(), 2, 'clearSearch 应立即重载一次，且不再有防抖请求')
})
