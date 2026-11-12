import test from 'node:test'
import assert from 'node:assert/strict'
import { scopeOptions, ownerOf, linkedFilters } from './filterLinkage.js'

// 两个站点各有一套分类，用来覆盖跨站点组合（标签 / 地域 / 年份筛选已移除）。
const dataset = {
  category: [
    { id: 11, sourceId: 1, name: '电影' },
    { id: 12, sourceId: 1, name: '剧集' },
    { id: 21, sourceId: 2, name: '电影' },
  ],
}
const empty = () => ({ source: '', category: '' })

test('scopeOptions：站点为空列出全部，站点确定后只留该站点选项', () => {
  assert.equal(scopeOptions(dataset.category, ''), dataset.category)
  assert.deepEqual(scopeOptions(dataset.category, '1').map(x => x.id), [11, 12])
  assert.deepEqual(scopeOptions(dataset.category, 2).map(x => x.id), [21])
})

test('ownerOf：数字 / 字符串 id 都能定位，未知项返回 0', () => {
  assert.equal(ownerOf('category', 11, dataset), 1)
  assert.equal(ownerOf('category', '21', dataset), 2)
  assert.equal(ownerOf('category', '', dataset), 0)
  assert.equal(ownerOf('category', 999, dataset), 0)
  assert.equal(ownerOf('category', 11, {}), 0)
})

test('选站点：只收窄选项，不改动已有分类', () => {
  const s = { ...empty(), source: '1' }
  assert.deepEqual(linkedFilters(s, 'source', dataset), { ...empty(), source: '1' })
})

test('站点内选同站点分类：站点保持不变', () => {
  const s = { ...empty(), source: '1', category: '11' }
  assert.deepEqual(linkedFilters(s, 'category', dataset), s)
})

test('双向联动：选了别的站点的分类，站点自动定位过去', () => {
  const s = { ...empty(), source: '1', category: '21' }
  assert.deepEqual(linkedFilters(s, 'category', dataset), { ...empty(), source: '2', category: '21' })
})

test('切换站点：清掉属于原站点的分类', () => {
  const s = { source: '2', category: '11' }
  assert.deepEqual(linkedFilters(s, 'source', dataset), { ...empty(), source: '2' })
})

test('外部跳转校正：按分类推断站点', () => {
  const s = { source: '', category: '11' }
  assert.deepEqual(linkedFilters(s, null, dataset), { ...empty(), source: '1', category: '11' })
})

test('把分类清空：站点保持不变', () => {
  const s = { source: '1', category: '' }
  assert.deepEqual(linkedFilters(s, 'category', dataset), s)
})

test('站点切回「全部站点」：保留已选分类，不误清', () => {
  const s = { source: '', category: '11' }
  assert.deepEqual(linkedFilters(s, 'source', dataset), s)
})

test('选项数据尚未加载：不误清已选分类', () => {
  const s = { source: '1', category: '11' }
  assert.deepEqual(linkedFilters(s, 'source', { category: [] }), s)
})

test('纯函数：不修改入参', () => {
  const s = { source: '1', category: '11' }
  const snapshot = JSON.stringify(s)
  linkedFilters(s, 'source', dataset)
  assert.equal(JSON.stringify(s), snapshot)
})
