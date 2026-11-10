import test from 'node:test'
import assert from 'node:assert/strict'
import { scopeOptions, ownerOf, linkedFilters } from './filterLinkage.js'

// 两个站点各有一套分类 / 标签 / 地域，用来覆盖跨站点组合。
const dataset = {
  category: [
    { id: 11, sourceId: 1, name: '电影' },
    { id: 12, sourceId: 1, name: '剧集' },
    { id: 21, sourceId: 2, name: '电影' },
  ],
  tag: [
    { id: 31, sourceId: 1, name: '动作' },
    { id: 41, sourceId: 2, name: '喜剧' },
  ],
  area: [
    { id: 51, sourceId: 1, name: '美国' },
    { id: 61, sourceId: 2, name: '日本' },
  ],
}
const empty = () => ({ source: '', category: '', tag: '', area: '' })

test('scopeOptions：站点为空列出全部，站点确定后只留该站点选项', () => {
  assert.equal(scopeOptions(dataset.category, ''), dataset.category)
  assert.deepEqual(scopeOptions(dataset.category, '1').map(x => x.id), [11, 12])
  assert.deepEqual(scopeOptions(dataset.area, 2).map(x => x.id), [61])
})

test('ownerOf：数字 / 字符串 id 都能定位，未知项返回 0', () => {
  assert.equal(ownerOf('category', 11, dataset), 1)
  assert.equal(ownerOf('tag', '41', dataset), 2)
  assert.equal(ownerOf('area', '', dataset), 0)
  assert.equal(ownerOf('tag', 999, dataset), 0)
  assert.equal(ownerOf('tag', 31, {}), 0)
})

test('选站点：只收窄选项，不改动已有条件', () => {
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

test('切换站点：清掉属于原站点的分类与标签', () => {
  const s = { source: '2', category: '11', tag: '31', area: '' }
  assert.deepEqual(linkedFilters(s, 'source', dataset), { ...empty(), source: '2' })
})

test('选别的站点的标签：站点定位过去，旧地域被清掉', () => {
  const s = { source: '2', category: '', tag: '31', area: '61' }
  assert.deepEqual(linkedFilters(s, 'tag', dataset), { ...empty(), source: '1', tag: '31' })
})

test('外部跳转校正：按分类推断站点，并清掉跨站点的标签', () => {
  const s = { source: '', category: '11', tag: '41', area: '' }
  assert.deepEqual(linkedFilters(s, null, dataset), { ...empty(), source: '1', category: '11' })
})

test('把某个条件清空：站点保持，其余条件不受影响', () => {
  const s = { source: '1', category: '', tag: '31', area: '51' }
  assert.deepEqual(linkedFilters(s, 'category', dataset), s)
})

test('站点切回「全部站点」：保留已选条件，不误清', () => {
  const s = { source: '', category: '11', tag: '', area: '' }
  assert.deepEqual(linkedFilters(s, 'source', dataset), s)
})

test('选项数据尚未加载：不误清任何已选条件', () => {
  const s = { source: '1', category: '11', tag: '31', area: '61' }
  assert.deepEqual(linkedFilters(s, 'source', { category: [], tag: [], area: [] }), s)
})

test('纯函数：不修改入参', () => {
  const s = { source: '1', category: '11', tag: '41', area: '' }
  const snapshot = JSON.stringify(s)
  linkedFilters(s, 'source', dataset)
  assert.equal(JSON.stringify(s), snapshot)
})
