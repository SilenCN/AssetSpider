import test from 'node:test'
import assert from 'node:assert/strict'
import { validateField, buildPayload, buildDatabasePayload } from './settings.js'

const fields = [
  { key: 'maxConcurrent', unit: '个', min: 1, max: 64 },
  { key: 'pageSize', unit: '条', min: 0, max: 1000 },
  { key: 'pageIntervalMs', unit: '毫秒', min: 0, max: 60000 },
  { key: 'userAgent', unit: '字符串' },
  { key: 'timeoutSeconds', unit: '秒', min: 1, max: 600, readOnly: true }, // 环境变量接管
]

test('validateField 校验整数与范围', () => {
  assert.equal(validateField(fields[0], 3), '')
  assert.equal(validateField(fields[0], 0), '不能小于 1')
  assert.equal(validateField(fields[0], 100), '不能大于 64')
  assert.equal(validateField(fields[0], 2.5), '需填整数')
  assert.equal(validateField(fields[0], ''), '需填数字')
  assert.equal(validateField(fields[0], 'abc'), '需填数字')
  assert.equal(validateField(fields[1], 0), '', 'pageSize 允许 0')
  assert.equal(validateField(fields[2], 0), '', '每页间隔 0 表示不限速，合法')
  assert.equal(validateField(fields[2], 1500), '', '每页间隔正常值合法')
  assert.equal(validateField(fields[2], 60001), '不能大于 60000', '每页间隔越界拒绝')
  assert.equal(validateField(fields[2], -1), '不能小于 0', '每页间隔不能为负')
  assert.equal(validateField(fields[3], '任意文本'), '', '字符串字段跳过范围')
})

test('validateField 跳过只读字段', () => {
  assert.equal(validateField(fields[3], ''), '')
  assert.equal(validateField(fields[3], -999), '')
})

test('buildPayload 跳过只读字段并转换类型', () => {
  const form = { maxConcurrent: '7', pageSize: '50', pageIntervalMs: '800', userAgent: '  MyBot/1.0  ', timeoutSeconds: 999 }
  const payload = buildPayload(fields, form)
  assert.deepEqual(payload, { maxConcurrent: 7, pageSize: 50, pageIntervalMs: 800, userAgent: 'MyBot/1.0' })
  assert.equal('timeoutSeconds' in payload, false, '只读字段不应提交')
})

test('buildDatabasePayload 生成数据库提交载荷', () => {
  assert.deepEqual(buildDatabasePayload({
    driver: 'mysql', path: '', dsn: ' u:p@tcp(h:3306)/db ', maxOpenConns: '32', maxIdleConns: 8, connMaxLifetimeMin: 30,
  }), {
    driver: 'mysql', path: '', dsn: 'u:p@tcp(h:3306)/db', maxOpenConns: 32, maxIdleConns: 8, connMaxLifetimeMin: 30,
  }, 'dsn 应去空白、数字字段转 Number')

  // 缺省值兜底：空表单也能产出合法载荷。
  assert.deepEqual(buildDatabasePayload({}), {
    driver: 'sqlite', path: '', dsn: '', maxOpenConns: 0, maxIdleConns: 0, connMaxLifetimeMin: 0,
  })
})
