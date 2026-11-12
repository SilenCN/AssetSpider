// 影片详情页：全字段资料、结构化播放组、hash 路由进出详情、列表滚动位置恢复。
// 播放器本身在 ./player.js，这里只负责把详情数据喂给它。
import { computed, nextTick } from 'vue'
import { DEFAULT_TAB, detailHash, detailIdFromHash, tabToHash } from '../layout.js'
import { createPlayer } from './player.js'

export function createDetail(ctx) {
  // 请求序号：连续点开不同影片 / 快速前进后退时只认最后一次详情请求，
  // 避免先发后到的旧响应把当前影片覆盖掉。
  let detailSeq = 0

  // ---------- 全字段详情 ----------
  async function loadFieldMeta() {
    try { ctx.fieldMeta.value = await ctx.request('/api/meta/v1/fields') } catch (e) { /* 元数据不影响主流程 */ }
  }

  const nonEmptyCount = computed(() => {
    if (!ctx.detail.value) return 0
    return (ctx.fieldMeta.value.fields || []).filter(f => ctx.hasValue(ctx.detail.value[f.json])).length
  })

  const groupedFields = computed(() => {
    const groups = ctx.fieldMeta.value.groups || []
    const fields = ctx.fieldMeta.value.fields || []
    return groups.map(g => ({
      key: g.key,
      label: g.label,
      items: fields.filter(f => f.group === g.key && (!ctx.onlyNonEmpty.value || ctx.hasValue(ctx.detail.value?.[f.json]))),
    })).filter(g => g.items.length)
  })

  const extraPairs = computed(() => {
    if (!ctx.detail.value?.extra) return []
    try {
      const obj = JSON.parse(ctx.detail.value.extra)
      return Object.entries(obj).map(([k, v]) => [k, typeof v === 'string' ? v : JSON.stringify(v)])
    } catch (e) { return [] }
  })

  // 按苹果CMS 字段解析出的结构化播放组（来自 detail.vodPlayList 的 JSON）。
  const playGroups = computed(() => {
    const raw = ctx.detail.value?.vodPlayList
    if (!raw) return []
    try {
      const arr = JSON.parse(raw)
      return Array.isArray(arr) ? arr : []
    } catch (e) { return [] }
  })

  const player = createPlayer(ctx, playGroups)

  // ---------- 影片详情：作为主内容区的独立页面，而非弹窗 ----------
  // 离开详情页：销毁播放器并清空详情（地址栏由调用方或 hashchange 处理）。
  function leaveDetail() {
    player.destroyPlayer()
    ctx.detail.value = null
  }

  // 返回资源库后把滚动位置恢复到点开影片前的原处。等详情 DOM 卸载、列表重新铺开
  // 再滚动，否则列表高度还没恢复，浏览器会把目标位置夹到错误的地方。
  function restoreListScroll() {
    nextTick(() => window.scrollTo(0, ctx.listScrollY.value))
  }

  // 详情页「返回资源库」：写回列表 hash，hashchange 会同步清掉详情。
  function closeDetail() {
    leaveDetail()
    const h = tabToHash(DEFAULT_TAB)
    if (location.hash !== h) location.hash = h
    // 注意：上面改 hash 会触发 onHashChange，但那时 detail 已被 leaveDetail 清空，
    // onHashChange 的「离开详情」分支据此不会重复恢复。这里显式恢复一次即可。
    restoreListScroll()
  }

  // 把影片对象渲染成详情页面（初始化逻辑与弹窗时期一致）。
  async function showVideoDetail(v) {
    ctx.detail.value = v
    ctx.detailTab.value = 'play'
    ctx.onlyNonEmpty.value = true
    // 默认选中第一条有剧集的线路
    const gi = playGroups.value.findIndex(g => (g.episodes?.length || 0) > 0)
    ctx.activeGroup.value = gi >= 0 ? gi : 0
    ctx.activeEp.value = 0
    // 进入详情后滚到页面顶部：详情是独立页面，不该继承资源库列表的滚动位置，
    // 否则会停在页面中段、看不到标题与播放器，还得用户手动往上滚。
    // 等详情 DOM 渲染后再滚，避免被列表卸载时的高度变化影响。
    // 进入前的列表滚动位置已由 openDetail 存进 listScrollY，返回时再恢复。
    nextTick(() => window.scrollTo(0, 0))
    if (!ctx.fieldMeta.value.fields?.length) await loadFieldMeta()
    player.mountPlayer() // 内部会 await nextTick() 等详情组件挂载出 <video>
  }

  // 列表只返回卡片投影，完整记录一律按 id 调详情接口：列表缓存对象不能再复用，
  // 否则播放组 / 简介 / 扩展字段等会缺失。加载期间用 detailLoading 占位，避免重复点击。
  async function loadDetailById(id) {
    const seq = ++detailSeq
    ctx.detailLoading.value = true
    try {
      const v = await ctx.request(`/api/videos/v1/${id}`)
      if (seq !== detailSeq) return // 已有更新的详情请求，丢弃这次结果
      await showVideoDetail(v)
    } catch (e) {
      if (seq !== detailSeq) return
      ctx.error.value = `影片不存在或已移除：${e.message}`
      ctx.notify('影片不存在或已移除', 'err')
      leaveDetail()
      history.replaceState(null, '', tabToHash(DEFAULT_TAB))
      ctx.tab.value = DEFAULT_TAB
      ctx.loadVideos().catch(() => {})
    } finally {
      if (seq === detailSeq) ctx.detailLoading.value = false
    }
  }

  // 从资源库点开影片：作为一次页面跳转，pushState 让浏览器后退能回到列表。
  // 已在详情页时改用 replaceState，避免历史栈里堆一串影片。
  async function openDetail(v) {
    // 仅在「从资源库列表首次下钻」时记录滚动位置；已在详情页内切换影片时不覆盖，
    // 这样连续翻看多部影片后返回，仍回到最初进入列表时的位置。
    if (!ctx.detail.value && ctx.tab.value === 'videos') ctx.listScrollY.value = window.scrollY
    const h = detailHash(v.id)
    if (detailIdFromHash(location.hash) == null) history.pushState(null, '', h)
    else if (location.hash !== h) history.replaceState(null, '', h)
    await loadDetailById(v.id)
  }

  // 刷新 / 分享链接 / 浏览器前进后退落到 #/video/<id>：按 ID 重新取详情。
  async function openDetailById(id) {
    await loadDetailById(id)
  }

  return {
    loadFieldMeta, nonEmptyCount, groupedFields, extraPairs, playGroups,
    leaveDetail, restoreListScroll, closeDetail, showVideoDetail, openDetail, openDetailById,
    ...player,
  }
}
