// HTTP 客户端：统一带上令牌、统一解析错误、令牌失效时清除本地登录态。
export function createClient(ctx) {
  async function request(path, options = {}) {
    const headers = { ...(options.headers || {}) }
    if (ctx.token.value) headers['Authorization'] = 'Bearer ' + ctx.token.value
    const res = await fetch(path, { ...options, headers })
    const body = await res.json().catch(() => ({}))
    // 令牌失效时立即清除本地登录态，回到登录页。
    if (res.status === 401 && ctx.token.value) {
      ctx.clearAuth()
      throw new Error('登录已失效，请重新登录')
    }
    if (!res.ok) throw new Error(body.error || `请求失败 (${res.status})`)
    return body
  }

  function jsonOptions(method, data) {
    return {
      method,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(data),
    }
  }

  return { request, jsonOptions }
}
