// 播放器：线路 / 选集、地址类型判定、hls.js 与原生 <video> 的挂载与错误提示。
import { computed, nextTick, onBeforeUnmount } from 'vue'

export function createPlayer(ctx, playGroups) {
  // hls.js 体积较大，只在真正播放 m3u8 时按需加载，避免拖慢首屏。
  let HlsLib = null
  async function loadHls() {
    if (!HlsLib) HlsLib = (await import('hls.js')).default
    return HlsLib
  }

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

  function clearPlayErrorTimer() {
    if (playErrorTimer) {
      clearTimeout(playErrorTimer)
      playErrorTimer = null
    }
  }

  function onVideoLoaded() {
    ctx.playError.value = ''
    clearPlayErrorTimer()
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
    clearPlayErrorTimer()
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

  // 用 hls.js 接管当前地址；返回 false 表示已放弃（调用方无需再回退）。
  async function mountHls(el, url) {
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
        await mountHls(el, url)
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

  onBeforeUnmount(destroyPlayer)

  return {
    loadHls, currentGroup, currentEpisodes, currentEpisode, currentUrl,
    detailSource, parseEnabled, useFrame, isHls, isDirectVideo, isUnknownStream, canPlay,
    onVideoLoaded, hlsErrorText, videoErrorText, onVideoError, destroyPlayer, mountPlayer,
    chooseGroup, chooseEp, switchDetailTab,
  }
}
