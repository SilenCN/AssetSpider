import test from 'node:test'
import assert from 'node:assert/strict'
import {
  NAV_ITEMS, TAB_KEYS, DEFAULT_TAB, tabToHash, normalizeTab, navItemsFor,
  DETAIL_HASH_PREFIX, detailHash, detailIdFromHash,
} from './layout.js'

test('资源库是默认页且排在第一位', () => {
  assert.equal(DEFAULT_TAB, 'videos')
  assert.equal(NAV_ITEMS[0].key, 'videos')
  assert.equal(NAV_ITEMS[0].primary, true)
  assert.deepEqual(TAB_KEYS, ['videos', 'sources', 'categories', 'jobs', 'settings', 'users'])
})

test('tabToHash 输出稳定的 hash', () => {
  assert.equal(tabToHash('videos'), '#/videos')
  assert.equal(tabToHash('sources'), '#/sources')
})

test('normalizeTab 兼容各种 hash / 裸值写法', () => {
  for (const v of ['videos', '#/videos', '#videos', '/videos', 'videos/', ' videos ']) {
    assert.equal(normalizeTab(v), 'videos', `输入 ${JSON.stringify(v)}`)
  }
  assert.equal(normalizeTab('#/sources'), 'sources')
  assert.equal(normalizeTab('#/categories'), 'categories')
  assert.equal(normalizeTab('#/jobs'), 'jobs')
  assert.equal(normalizeTab('#/users'), 'users')
})

test('normalizeTab 对非法/空值返回 null', () => {
  for (const v of ['', '#', '#/', '/', 'unknown', '#/unknown', null, undefined, 42, {}, []]) {
    assert.equal(normalizeTab(v), null, `输入 ${JSON.stringify(v)}`)
  }
})

test('TAB_KEYS 里的每个值都能被 normalizeTab 往返', () => {
  for (const key of TAB_KEYS) {
    assert.equal(normalizeTab(tabToHash(key)), key)
  }
})

test('navItemsFor 按管理员权限过滤用户管理', () => {
  assert.deepEqual(navItemsFor(false).map(i => i.key), ['videos', 'sources', 'categories', 'jobs'])
  assert.deepEqual(navItemsFor(true).map(i => i.key), TAB_KEYS)
})

test('导航配置结构完整：每项都有 key/label/icon', () => {
  for (const item of NAV_ITEMS) {
    assert.ok(item.key && item.label && item.icon, `缺少字段: ${JSON.stringify(item)}`)
  }
  assert.equal(new Set(NAV_ITEMS.map(i => i.key)).size, NAV_ITEMS.length, 'key 不应重复')
  assert.equal(NAV_ITEMS.filter(i => i.adminOnly).length, 2)
})

test('detailHash 输出稳定的详情页面地址', () => {
  assert.equal(DETAIL_HASH_PREFIX, '#/video/')
  assert.equal(detailHash(123), '#/video/123')
  assert.equal(detailHash('456'), '#/video/456')
})

test('detailIdFromHash 兼容多种写法', () => {
  for (const v of ['#/video/123', '/video/123', 'video/123', '#video/123', '#/video/123/', ' #/video/123 ']) {
    assert.equal(detailIdFromHash(v), 123, `输入 ${JSON.stringify(v)}`)
  }
})

test('detailIdFromHash 对非法值返回 null', () => {
  for (const v of [
    '', '#', '#/', '/', '#/videos', '#/users', '#/video', '#/video/',
    '#/video/abc', '#/video/0', '#/video/-1', '#/video/12/34',
    null, undefined, 123, {}, [],
  ]) {
    assert.equal(detailIdFromHash(v), null, `输入 ${JSON.stringify(v)}`)
  }
})

test('详情 hash 与 tab hash 互不混淆，且可往返', () => {
  for (let id = 1; id <= 500; id += 37) {
    assert.equal(detailIdFromHash(detailHash(id)), id)
    assert.equal(normalizeTab(detailHash(id)), null, `详情地址 ${detailHash(id)} 不应被当成普通 tab`)
  }
  for (const key of TAB_KEYS) {
    assert.equal(detailIdFromHash(tabToHash(key)), null, `${key} 的 hash 不应被当成详情`)
  }
  assert.deepEqual(TAB_KEYS, ['videos', 'sources', 'categories', 'jobs', 'settings', 'users'], '详情页不应进入侧边栏')
})
