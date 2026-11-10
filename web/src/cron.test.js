import test from 'node:test'
import assert from 'node:assert/strict'
import { parseCron, describeCron, nextRuns, evaluateCron, formatCronTime } from './cron.js'

const at = (y, m, d, h = 0, mi = 0) => new Date(y, m - 1, d, h, mi)
const hhmm = d => `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`

test('parseCron 接受合法的 5 段表达式', () => {
  for (const spec of [
    '* * * * *', '*/30 * * * *', '0 3 * * *', '0 3 * * 1-5',
    '15,45 2-4 1,15 * 0,6', '0 0 1 1 *', '0 0 * * 7', '0 */6 * * *',
  ]) {
    assert.equal(parseCron(spec).valid, true, `${spec} 应合法`)
  }
})

test('parseCron 拒绝非法表达式并给出可读原因', () => {
  for (const spec of [
    '', '* * * *', '* * * * * *', '60 * * * *', '* 24 * * *',
    '* * 0 * *', '* * * 13 *', '* * * * 8', '*/0 * * * *',
    'a * * * *', '5-2 * * * *', '1,,2 * * * *',
  ]) {
    const r = parseCron(spec)
    assert.equal(r.valid, false, `${JSON.stringify(spec)} 应非法`)
    assert.ok(r.error && r.error.length > 0, `${JSON.stringify(spec)} 应带错误说明`)
  }
  assert.match(parseCron('0 99 * * *').error, /小时/)
})

test('describeCron 把常用预设解释成中文', () => {
  assert.match(describeCron('*/30 * * * *'), /每 30 分钟/)
  assert.match(describeCron('0 */6 * * *'), /每 6 小时/)
  assert.match(describeCron('0 3 * * *'), /每天 03:00/)
  assert.match(describeCron('0 3 * * 1-5'), /周一至周五/)
  assert.match(describeCron('0 * * * *'), /每小时的第 0 分钟/)
  assert.match(describeCron('0 0 1 1 *'), /每年 1 月 1 日 00:00/)
  assert.match(describeCron('15,45 2-4 * * *'), /复杂规则|按分钟/)
  assert.equal(describeCron('bad'), '')
})

test('nextRuns 返回接下来几次触发时刻，且都命中表达式', () => {
  const now = at(2026, 11, 10, 20, 1)
  const runs = nextRuns('0 3 * * *', now, 3)
  assert.equal(runs.length, 3)
  const stamp = d => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')} ${hhmm(d)}`
  assert.deepEqual(runs.map(stamp), [
    '2026-11-11 03:00', '2026-11-12 03:00', '2026-11-13 03:00',
  ])
  assert.equal(`${runs[0].getMonth() + 1}-${runs[0].getDate()}`, '11-11')
  assert.equal(hhmm(runs[0]), '03:00')
  assert.equal(hhmm(runs[2]), '03:00')
  assert.ok(runs[2] > runs[0])

  const half = nextRuns('*/30 * * * *', now, 2)
  assert.equal(hhmm(half[0]), '20:30')
  assert.equal(hhmm(half[1]), '21:00')

  const yearly = nextRuns('0 0 1 1 *', at(2026, 11, 10, 20, 0), 1)
  assert.equal(yearly.length, 1)
  assert.equal(`${yearly[0].getFullYear()}-${yearly[0].getMonth() + 1}-${yearly[0].getDate()}`, '2027-1-1')
})

test('evaluateCron 覆盖空 / 非法 / 正常三种状态', () => {
  assert.equal(evaluateCron('').state, 'empty')
  assert.equal(evaluateCron('  ').state, 'empty')
  const bad = evaluateCron('0 99 * * *')
  assert.equal(bad.state, 'invalid')
  assert.match(bad.error, /小时/)
  assert.deepEqual(bad.next, [])

  const good = evaluateCron('0 3 * * *', { now: at(2026, 11, 10, 20, 0), count: 2 })
  assert.equal(good.state, 'ok')
  assert.match(good.meaning, /每天 03:00/)
  assert.equal(good.next.length, 2)
})

test('formatCronTime 跨年时带年份', () => {
  const now = at(2026, 11, 10)
  assert.equal(formatCronTime(at(2026, 11, 11, 3, 0), now), '11-11 03:00')
  assert.equal(formatCronTime(at(2027, 1, 1, 0, 0), now), '2027-01-01 00:00')
})
