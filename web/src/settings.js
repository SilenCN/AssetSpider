// 系统设置页的纯逻辑：字段校验与提交载荷生成。
// 不依赖 Vue / DOM，便于单测；规则与后端 service/settings.go 的元信息对应。

// validateField 返回字段的错误提示，空串表示合法。只读字段与字符串字段跳过范围校验。
export function validateField(field, value) {
  if (field.readOnly) return ''
  if (field.unit === '字符串') return ''
  const n = Number(value)
  if (value === '' || value === null || value === undefined || !Number.isFinite(n)) return '需填数字'
  if (!Number.isInteger(n)) return '需填整数'
  if (field.min != null && n < field.min) return `不能小于 ${field.min}`
  if (field.max && n > field.max) return `不能大于 ${field.max}`
  return ''
}

// buildPayload 从表单值里挑出可编辑字段生成提交载荷：
// 跳过只读（环境变量接管）字段；数字字段转 Number，字符串字段原样去空白。
export function buildPayload(fields, form) {
  const payload = {}
  for (const f of fields || []) {
    if (f.readOnly) continue
    const raw = form[f.key]
    if (f.unit === '字符串') {
      payload[f.key] = String(raw ?? '').trim()
    } else {
      payload[f.key] = Number(raw)
    }
  }
  return payload
}

// buildDatabasePayload 生成数据库配置的提交载荷。
// dsn 留空表示「保留现有连接串」（页面不回显明文，避免误清空）；driver=sqlite 时后端忽略 dsn。
export function buildDatabasePayload(form) {
  return {
    driver: String(form.driver || 'sqlite'),
    path: String(form.path ?? '').trim(),
    dsn: String(form.dsn ?? '').trim(),
    maxOpenConns: Number(form.maxOpenConns) || 0,
    maxIdleConns: Number(form.maxIdleConns) || 0,
    connMaxLifetimeMin: Number(form.connMaxLifetimeMin) || 0,
  }
}

