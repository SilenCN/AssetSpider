import test from 'node:test'
import assert from 'node:assert/strict'
import { validateField, buildPayload } from './settings.js'

const fields = [
  { key: 'maxConcurrent', unit: '个', min: 1, max: 64 },
  { key: 'pageSize', unit: '条', min: 0, max: 1000 },
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
  assert.equal(validateField(fields[2], '任意文本'), '', '字符串字段跳过范围')
})

test('validateField 跳过只读字段', () => {
  assert.equal(validateField(fields[3], ''), '')
  assert.equal(validateField(fields[3], -999), '')
})

test('buildPayload 跳过只读字段并转换类型', () => {
  const form = { maxConcurrent: '7', pageSize: '50', userAgent: '  MyBot/1.0  ', timeoutSeconds: 999 }
  const payload = buildPayload(fields, form)
  assert.deepEqual(payload, { maxConcurrent: 7, pageSize: 50, userAgent: 'MyBot/1.0' })
  assert.equal('timeoutSeconds' in payload, false, '只读字段不应提交')
})
