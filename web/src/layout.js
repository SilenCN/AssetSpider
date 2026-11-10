// 侧边栏导航配置与「记住当前页面」的 hash 规则。
// 纯数据 + 纯函数，不依赖 Vue / DOM，便于单测。

// 资源库为主页面：排第一，并作为默认页与非法值的回退页。
// icon 为 24×24 的 stroke 风格 SVG path，折叠态只显示图标。
export const NAV_ITEMS = [
  {
    key: 'videos', label: '资源库', primary: true,
    icon: 'M3 3h7v7H3zM14 3h7v7h-7zM14 14h7v7h-7zM3 14h7v7H3z',
  },
  {
    key: 'sources', label: '站点配置',
    icon: 'M3 4h18v6H3zM3 14h18v6H3zM7 7h.01M7 17h.01',
  },
  {
    key: 'categories', label: '视频分类',
    icon: 'M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01',
  },
  {
    key: 'jobs', label: '采集记录',
    icon: 'M3 12a9 9 0 1 0 9-9 9 9 0 0 0-9 9zM12 7v5l3 2',
  },
  {
    key: 'users', label: '用户管理', adminOnly: true,
    icon: 'M17 21v-2a4 4 0 0 0-4-4H5a4 4 0 0 0-4 4v2M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM23 21v-2a4 4 0 0 0-3-3.87M16 3.13a4 4 0 0 1 0 7.75',
  },
]

export const TAB_KEYS = NAV_ITEMS.map(i => i.key)
export const DEFAULT_TAB = 'videos'

export function tabToHash(tab) {
  return '#/' + tab
}

// 把任意输入规范化为合法 tab，非法/空值返回 null。
// 兼容 '#/videos'、'#videos'、'/videos'、'videos/'、' videos ' 等写法。
export function normalizeTab(value) {
  if (typeof value !== 'string') return null
  const raw = value.trim().replace(/^#/, '').replace(/^\/+/, '').replace(/\/+$/, '')
  return TAB_KEYS.includes(raw) ? raw : null
}

// 按权限过滤导航项；普通用户看不到「用户管理」。
export function navItemsFor(isAdmin) {
  return NAV_ITEMS.filter(i => !i.adminOnly || isAdmin)
}

// ---------- 影片详情页 ----------
// 详情页不是侧边栏常驻入口，用 #/video/<数字ID> 作为独立页面地址，
// 因此刷新、分享链接、浏览器前进后退都能按 ID 恢复（由 App.vue 调后端详情接口）。
export const DETAIL_HASH_PREFIX = '#/video/'

export function detailHash(id) {
  return DETAIL_HASH_PREFIX + encodeURIComponent(String(id))
}

// 兼容 '#/video/12'、'/video/12'、'video/12'、'#/video/12/' 等写法；
// 非数字、非正数、普通 tab 或其它路径一律返回 null。
export function detailIdFromHash(value) {
  if (typeof value !== 'string') return null
  const raw = value.trim().replace(/^#/, '').replace(/^\/+/, '').replace(/\/+$/, '')
  const m = raw.match(/^video\/(\d+)$/i)
  if (!m) return null
  const n = Number(m[1])
  return Number.isSafeInteger(n) && n > 0 ? n : null
}
