import test from 'node:test'
import assert from 'node:assert/strict'
import { groupLabel, groupHint, LABEL_MAX } from './playGroups.js'

test('线路名称优先沿用站点的播放组原名', () => {
  assert.equal(groupLabel({ from: 'ffm3u8' }, 0), 'ffm3u8')
  assert.equal(groupLabel({ from: '量子资源' }, 1), '量子资源')
  assert.equal(groupLabel({ from: '  lzm3u8  ' }, 2), 'lzm3u8')
})

test('播放组没有名字时退回「线路 N」，并且不影响其它线路编号', () => {
  assert.equal(groupLabel({}, 2), '线路 3')
  assert.equal(groupLabel({ from: '   ' }, 0), '线路 1')
  assert.equal(groupLabel(null, 1), '线路 2')
})

test('过长的播放组名截断到 LABEL_MAX 并加省略号', () => {
  const long = 'x'.repeat(LABEL_MAX + 5)
  const label = groupLabel({ from: long }, 0)
  assert.equal(label, `${'x'.repeat(LABEL_MAX)}…`)
  assert.equal(groupLabel({ from: 'x'.repeat(LABEL_MAX) }, 0), 'x'.repeat(LABEL_MAX))
})

test('悬浮提示包含完整原名、备注与服务器组', () => {
  assert.equal(groupHint({ from: 'ffm3u8', note: '高清', server: 's1' }), 'ffm3u8 · 高清 · s1')
  assert.equal(groupHint({ from: 'ffm3u8' }), 'ffm3u8')
  assert.equal(groupHint({ from: 'ffm3u8', server: 'no' }), 'ffm3u8')
  assert.equal(groupHint(null), '')
})

test('截断后的线路仍能从提示里看到完整名称', () => {
  const full = '线路名称' + '很长'.repeat(8) // 20 字，超过 LABEL_MAX
  const g = { from: full, note: '备用' }
  assert.ok(full.length > LABEL_MAX)
  assert.ok(groupLabel(g, 0).endsWith('…'))
  assert.ok(groupHint(g).startsWith(full))
})
