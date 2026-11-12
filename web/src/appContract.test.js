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
  'tags', 'jobs', 'tasks', 'users', 'capacity', 'settings', 'settingsForm', 'settingsDirty',
  'settingsSaving', 'loadSettings', 'saveSettings', 'error', 'notice', 'notify',
  'videoQuery', 'videoSource', 'videoCategory', 'videoTag', 'videoAreaId', 'videoYear', 'videoSort',
  'areas', 'years', 'visibleCategories', 'visibleTags', 'visibleAreas', 'hasActiveFilters',
  'areaGroups', 'videoPage', 'videoSize', 'videoLoading', 'videoLoadingMore', 'videoEnd',
  'videoSentinel', 'visibleVideoTotal', 'request', 'jsonOptions', 'loadHealth', 'loadSources',
  'fetchVideos', 'kickSentinel', 'loadVideos', 'loadMoreVideos', 'loadAreas', 'loadYears',
  'loadJobs', 'loadTasks', 'loadCategories', 'loadCategoryTree', 'loadTags', 'loadUsers',
  'categoryName', 'activeTaskOf', 'queuedCountOf', 'showCategoryVideos', 'filterByTag',
  'catCollapsed', 'isCatCollapsed', 'toggleCat', 'catKey', 'siteNameOf', 'categoryForest',
  'catTotal', 'catVideoCount', 'catPath', 'catSelectLabel', 'tagSelectLabel', 'applyFilterLinkage',
  'onFilterChange', 'applyLinkage', 'clearAllFilters', 'toggleAllCats', 'fieldMeta', 'detail',
  'detailTab', 'onlyNonEmpty', 'loadFieldMeta', 'hasValue', 'nonEmptyCount', 'groupedFields',
  'extraPairs', 'playGroups', 'posterOf', 'markPicFailed', 'episodeLabel', 'scoreOf', 'metaLine',
  'tagListOf', 'playerEl', 'activeGroup', 'activeEp', 'currentGroup', 'currentEpisodes',
  'currentEpisode', 'currentUrl', 'detailSource', 'parseEnabled', 'useFrame', 'isHls',
  'isDirectVideo', 'isUnknownStream', 'canPlay', 'playError', 'onVideoLoaded', 'hlsErrorText',
  'videoErrorText', 'onVideoError', 'destroyPlayer', 'mountPlayer', 'chooseGroup', 'chooseEp',
  'switchDetailTab', 'closeDetail', 'openDetail', 'onEsc', 'emptyForm', 'emptyUserForm', 'form',
  'editing', 'sourceOpen', 'resetForm', 'sourcePayload', 'openSourceForm', 'closeSourceForm',
  'editSource', 'importOpen', 'importBusy', 'importFileName', 'importRaw', 'importPreview',
  'importFileEl', 'importPreviewShown', 'exportSources', 'pickImportFile', 'onImportFile',
  'closeImport', 'confirmImport', 'saveSource', 'removeSource', 'clearSourceData', 'toggleSource',
  'startTask', 'crawlAll', 'pauseTask', 'resumeTask', 'retryTask', 'rerunTask', 'barWidth',
  'isIndeterminate', 'fmtEta', 'taskStatusText', 'searchVideos', 'formatTime', 'modeLabel',
  'statusLabel', 'short', 'stat', 'jobMessage', 'loginForm', 'loginBusy', 'loginError', 'doLogin',
  'doLogout', 'pwdModal', 'pwdForm', 'doChangePassword', 'bootstrap', 'userForm', 'userEditing',
  'userFormOpen', 'openUserForm', 'closeUserForm', 'resetUserForm', 'editUser', 'saveUser',
  'toggleUser', 'removeUser', 'visibleLabel', 'collectTabs', 'refreshCollectData', 'onVisible',
  'refresh', 'switchTab', 'navItems', 'currentPageTitle', 'toggleSidebar', 'sidebarCollapsed',
  'sidebarMobileOpen', 'loadHls',
  'emptySubForm', 'subscriptions', 'subForm', 'subOpen', 'subBusy', 'subRefreshing',
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

test('筛选联动：选站点后清掉其它站点已选条件', () => {
  const app = createApp()
  app.categories.value = [{ id: 10, name: '动作片', sourceId: 1 }, { id: 20, name: '喜剧片', sourceId: 2 }]
  app.tags.value = []
  app.areas.value = []
  app.videoCategory.value = '10'
  app.videoSource.value = '2'
  app.applyLinkage()
  // 分类 / 标签 / 地域是筛选主轴：与站点冲突时，站点被纠正到条件所属的站点。
  assert.equal(app.videoSource.value, '1')
  assert.equal(app.videoCategory.value, '10')
})
