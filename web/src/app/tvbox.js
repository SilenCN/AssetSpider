// TVBox 专属接口：鉴权键的查看 / 生成 / 重置 / 撤销，以及配置地址拼装。
// 盒子无法填 Bearer 头，因此用「域名 + /tvbox/<key>/config」这一个 URL 配置，
// key 由登录用户自助管理，可见站点范围与登录态完全一致。
import { computed } from 'vue'

export function createTvbox(ctx) {
  async function loadApiKey() {
    if (!ctx.token.value) return
    try {
      ctx.apiKey.value = await ctx.request('/api/auth/v1/api-key')
    } catch (e) {
      ctx.notify(e.message, 'err')
    }
  }

  async function regenerateApiKey() {
    if (ctx.apiKeyBusy.value) return
    ctx.apiKeyBusy.value = true
    try {
      const d = await ctx.request('/api/auth/v1/api-key', ctx.jsonOptions('POST', {}))
      ctx.apiKey.value = { ...ctx.apiKey.value, key: d.key, enabled: d.enabled }
      ctx.notify(d.key ? '接口键已生成，旧键立即失效' : '接口键已生成')
    } catch (e) {
      ctx.notify(e.message, 'err')
    } finally {
      ctx.apiKeyBusy.value = false
    }
  }

  async function revokeApiKey() {
    if (ctx.apiKeyBusy.value) return
    if (!confirm('确认撤销接口键？已配置该地址的 TVBox 将立即无法访问。')) return
    ctx.apiKeyBusy.value = true
    try {
      await ctx.request('/api/auth/v1/api-key', { method: 'DELETE' })
      ctx.apiKey.value = { ...ctx.apiKey.value, key: '' }
      ctx.notify('接口键已撤销')
    } catch (e) {
      ctx.notify(e.message, 'err')
    } finally {
      ctx.apiKeyBusy.value = false
    }
  }

  // 配置地址：用当前站点根地址拼上 /tvbox/<key>/config。
  // 未生成 key 时返回空串，页面据此提示先生成。
  const tvboxConfigURL = computed(() => {
    const key = ctx.apiKey.value?.key
    if (!key) return ''
    const base = (typeof window !== 'undefined' && window.location?.origin) || ''
    return `${base}/tvbox/${key}/config`
  })

  async function copyText(text, label = '内容') {
    if (!text) return
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(text)
      } else {
        const ta = document.createElement('textarea')
        ta.value = text
        ta.style.position = 'fixed'
        ta.style.opacity = '0'
        document.body.appendChild(ta)
        ta.select()
        document.execCommand('copy')
        ta.remove()
      }
      ctx.notify(`${label}已复制`)
    } catch (e) {
      ctx.notify('复制失败，请手动复制', 'err')
    }
  }

  return {
    loadApiKey, regenerateApiKey, revokeApiKey, tvboxConfigURL, copyText,
  }
}
