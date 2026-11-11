// 影片详情页与播放器：线路 / 选集、按地址类型挂载 hls.js 或 <video>、详情路由。
import { computed, nextTick, onBeforeUnmount } from 'vue'
import { DEFAULT_TAB, detailHash, detailIdFromHash, tabToHash } from '../layout.js'

export function createDetail(ctx) {
  // hls.js 体积较大，只在真正播放 m3u8 时按需加载，避免拖慢首屏。
  let HlsLib = null
  async function loadHls() {
    if (!HlsLib) HlsLib = (await import('hls.js')).default
    return HlsLib
  }

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

  // ---------- 播放 ----------
  let hlsInstance = null
  let playErrorTimer = null

  const currentGroup = computed(() => playGroups.value[ctx.activeGroup.value] || null)
  const currentEpisodes = computed(() => currentGroup.value?.episodes || [])
  const currentEpisode = computed(() => currentEpisodes.value[ctx.activeEp.value] || null)
  const currentUrl = computed(() => currentEpisode.value?.url || '')

  // 站点配置了「视频解析地址」时，后端把播放地址拼成解析服务网页地址并打上
  // frame 标记；此时必须用 iframe 承载，<video> 无法直链播放（地址里常带
  // ?url=...m3u8，容易被后缀判断误认成媒体流）。parseEnabled 作为旧数据兜底。
  const detailSource = computed(() => ctx.sources.value.find(s => s.id === ctx.detail.value?.sourceId) || null)
  const parseEnabled = computed(() => !!detailSource.value?.parseApi)
  const useFrame = computed(() => !!currentUrl.value && (currentEpisode.value?.frame === true || parseEnabled.value))

  // 含 m3u8 即按 HLS 处理（很多站点地址是 ...?url=xxx.m3u8，不能只匹配结尾）。
  const isHls = computed(() => !!currentUrl.value && /m3u8/i.test(currentUrl.value))
  const isDirectVideo = computed(() => !!currentUrl.value && /\.(mp4|webm|ogg|mov|m4v)(\?|#|$)/i.test(currentUrl.value))
  // 苹果CMS 不少站点的播放地址不带后缀（如 /play/abc123），不能按后缀判死。
  // 这类地址先交给 hls.js 试播（多数实际是 m3u8），失败再回退 <video> 直链。
  const isUnknownStream = computed(() => !!currentUrl.value && !useFrame.value && !isHls.value && !isDirectVideo.value)
  const canPlay = computed(() => !!currentUrl.value && (isHls.value || isDirectVideo.value || isUnknownStream.value))

  function onVideoLoaded() {
    ctx.playError.value = ''
    if (playErrorTimer) { clearTimeout(playErrorTimer); playErrorTimer = null }
  }

  function hlsErrorText(data) {
    const map = {
      manifestLoadError: '播放列表加载失败（地址不可达、跨域或被拦截）',
      manifestLoadTimeOut: '播放列表加载超时',
      manifestParsingError: '播放列表格式错误（不是合法 m3u8）',
      levelLoadError: '线路加载失败',
      fragLoadError: '视频分片加载失败',
      fragLoadTimeOut: '视频分片加载超时',
      manifestIncompatibleCodecsError: '浏览器不支持该视频编码',
      bufferAppendError: '视频缓冲写入失败',
    }
    return map[data?.details] || data?.details || '未知错误'
  }

  function videoErrorText(code) {
    return ({
      1: '加载被中止',
      2: '网络错误（地址不可达或被拦截）',
      3: '视频解码失败（浏览器不支持该编码）',
      4: '媒体地址不支持或格式不匹配',
    })[code] || '未知错误'
  }

  function onVideoError(e) {
    // destroyPlayer 里的 removeAttribute('src') + load() 也会触发 error，忽略空 src 的情况。
    if (!e.target?.getAttribute('src')) return
    ctx.playError.value = '播放失败：' + videoErrorText(e.target?.error?.code) + '。可点「在新窗口打开」用浏览器直接播放。'
  }

  function destroyPlayer() {
    if (playErrorTimer) { clearTimeout(playErrorTimer); playErrorTimer = null }
    if (hlsInstance) {
      try { hlsInstance.destroy() } catch (e) { /* 忽略销毁异常 */ }
      hlsInstance = null
    }
    const el = ctx.playerEl.value
    if (el) {
      try { el.pause() } catch (e) { /* 元素可能已卸载 */ }
      try { el.removeAttribute('src'); el.load() } catch (e) { /* 忽略 */ }
    }
  }

  // 按地址类型挂载播放器。
  //
  // 关键：Linux/Chrome 的 canPlayType('application/vnd.apple.mpegurl') 会返回 'maybe'，
  // 但原生 <video> 实际解不了 m3u8（只有 Safari 能），直接赋 src 必然
  // MEDIA_ERR_SRC_NOT_SUPPORTED。因此只要 MediaSource 可用就一律优先用 hls.js，
  // 原生播放仅作为 hls.js 不可用且浏览器明确返回 'probably' 时的兜底。
  async function mountPlayer() {
    destroyPlayer()
    ctx.playError.value = ''
    await nextTick()
    if (useFrame.value) return // 解析地址走 iframe，无需挂载 <video>
    const el = ctx.playerEl.value
    const url = currentUrl.value
    if (!el || !url || !canPlay.value) return
    if (isHls.value || isUnknownStream.value) {
      try {
        const Hls = await loadHls()
        if (!Hls.isSupported()) throw new Error('浏览器不支持 MediaSource，无法播放 HLS')
        const inst = new Hls({ enableWorker: true })
        hlsInstance = inst
        let recovered = false
        inst.on(Hls.Events.MANIFEST_PARSED, () => { el.play().catch(() => {}) })
        inst.on(Hls.Events.ERROR, (_evt, data) => {
          if (!data?.fatal) return
          // 无后缀地址可能其实是直链视频：hls.js 试播失败后回退 <video> 再试一次。
          if (isUnknownStream.value) {
            try { inst.destroy() } catch (e) { /* 忽略 */ }
            hlsInstance = null
            el.src = url
            el.play().catch(() => {})
            return
          }
          if (data.type === Hls.ErrorTypes.NETWORK_ERROR && !recovered) {
            recovered = true
            ctx.playError.value = '播放加载失败，正在重试…'
            try { inst.startLoad() } catch (e) { /* 忽略 */ }
            // 重试迟迟不成功时给出明确结论，避免一直停在「正在重试」。
            playErrorTimer = setTimeout(() => {
              if (ctx.playError.value.includes('正在重试')) {
                ctx.playError.value = '播放失败：' + hlsErrorText(data) + '。可点「在新窗口打开」用浏览器直接播放，或切换其它线路。'
              }
            }, 8000)
            return
          }
          ctx.playError.value = '播放失败：' + hlsErrorText(data) + '。可点「在新窗口打开」用浏览器直接播放，或切换其它线路。'
        })
        inst.loadSource(url)
        inst.attachMedia(el)
        return
      } catch (e) {
        // 只有明确支持原生 HLS（Safari 返回 'probably'）时才回退，'maybe' 不可信。
        if (el.canPlayType('application/vnd.apple.mpegurl') === 'probably') {
          el.src = url
          el.play().catch(() => {})
          return
        }
        ctx.playError.value = '该线路暂时无法播放，请切换其它线路，或点「在新窗口打开」重试。'
        return
      }
    }
    el.src = url
    el.play().catch(() => { /* 自动播放被浏览器拦截时由用户点播放键 */ })
  }

  function chooseGroup(i) {
    if (i === ctx.activeGroup.value) return
    ctx.activeGroup.value = i
    ctx.activeEp.value = 0
    mountPlayer()
  }
  function chooseEp(i) {
    ctx.activeEp.value = i
    mountPlayer()
  }
  function switchDetailTab(t) {
    if (t === ctx.detailTab.value) return
    if (t === 'play') {
      ctx.detailTab.value = t
      mountPlayer()
    } else {
      destroyPlayer()
      ctx.detailTab.value = t
    }
  }

  // ---------- 影片详情：作为主内容区的独立页面，而非弹窗 ----------
  // 离开详情页：销毁播放器并清空详情（地址栏由调用方或 hashchange 处理）。
  function leaveDetail() {
    destroyPlayer()
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
    mountPlayer() // 内部会 await nextTick() 等详情组件挂载出 <video>
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
    await showVideoDetail(v)
  }

  // 刷新 / 分享链接 / 浏览器前进后退落到 #/video/<id>：按 ID 重新取详情。
  // 优先复用列表里已有的对象，否则调后端详情接口（返回结构与列表项一致）。
  async function openDetailById(id) {
    try {
      const cached = ctx.videos.value.find(x => Number(x.id) === Number(id))
      const v = cached || await ctx.request(`/api/videos/v1/${id}`)
      await showVideoDetail(v)
    } catch (e) {
      ctx.error.value = `影片不存在或已移除：${e.message}`
      ctx.notify('影片不存在或已移除', 'err')
      leaveDetail()
      history.replaceState(null, '', tabToHash(DEFAULT_TAB))
      ctx.tab.value = DEFAULT_TAB
      ctx.loadVideos().catch(() => {})
    }
  }

  onBeforeUnmount(destroyPlayer)

  return {
    loadHls, loadFieldMeta, nonEmptyCount, groupedFields, extraPairs, playGroups,
    currentGroup, currentEpisodes, currentEpisode, currentUrl, detailSource, parseEnabled,
    useFrame, isHls, isDirectVideo, isUnknownStream, canPlay,
    onVideoLoaded, hlsErrorText, videoErrorText, onVideoError, destroyPlayer, mountPlayer,
    chooseGroup, chooseEp, switchDetailTab, leaveDetail, restoreListScroll, closeDetail,
    showVideoDetail, openDetail, openDetailById,
  }
}
