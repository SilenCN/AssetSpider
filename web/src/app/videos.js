// 资源库：筛选条件（关键词 / 站点 / 分类的双向联动）、分页与底部无限滚动。
import { computed, nextTick, onBeforeUnmount, watch } from 'vue'
import { linkedFilters, scopeOptions } from '../filterLinkage.js'

export function createVideos(ctx) {
  // 请求序号：搜索词或筛选条件连续变化时，只认最后一次请求的结果，
  // 避免先发后到的旧响应把新结果覆盖掉。
  let videoSeq = 0
  let videoObserver = null
  // 输入防抖：连续打字只发最后一次请求；回车 / 按钮 / 切筛选等显式动作
  // 会立刻重载并取消尚未触发的定时器，避免重复请求。
  const SEARCH_DEBOUNCE_MS = 300
  let searchTimer = null

  // ---------- 筛选条件双向联动 ----------
  // 分类归属于某个站点（sourceId），站点是筛选的主轴：选了站点就只列出该站点的
  // 分类；反过来选中某个分类，站点会自动定位过去，避免必然为空的组合。
  const visibleCategories = computed(() => scopeOptions(ctx.categories.value, ctx.videoSource.value))
  const hasActiveFilters = computed(() => !!(
    ctx.videoQuery.value.trim() || ctx.videoSource.value || ctx.videoCategory.value
  ))

  // 「可见影片」与资源库页签的计数徽标都取站点表上的影片数快照（videoCount）：
  // 列表接口不返回总数，也就不需要对 videos 表做 COUNT。
  // 它与「可见站点」同源，因此资源库里的搜索 / 分类筛选不会改变这个数值。
  const visibleVideoTotal = computed(() =>
    ctx.sources.value.reduce((n, s) => n + (Number(s.videoCount) || 0), 0)
  )

  // 拉取一页影片。append=true 时追加到现有列表（无限滚动），否则整表替换（搜索/筛选/刷新）。
  // 接口只返回卡片投影，是否还有下一页由本页条数与分页条数推导。
  async function fetchVideos(page, append) {
    const seq = ++videoSeq
    const requestedSize = ctx.videoSize.value
    if (append) {
      ctx.videoLoadingMore.value = true
    } else {
      ctx.videoLoading.value = true
      // 新的搜索/筛选从第一页开始，顺带回到顶部，否则还停在上一次的滚动位置
      if (ctx.tab.value === 'videos') window.scrollTo(0, 0)
    }
    try {
      const p = new URLSearchParams({ page: String(page), size: String(ctx.videoSize.value) })
      if (ctx.videoQuery.value.trim()) p.set('q', ctx.videoQuery.value.trim())
      if (ctx.videoSource.value) p.set('source_id', String(ctx.videoSource.value))
      if (ctx.videoCategory.value) p.set('category_id', String(ctx.videoCategory.value))
      if (ctx.videoSort.value) p.set('sort', ctx.videoSort.value)
      const d = await ctx.request('/api/videos/v1?' + p.toString())
      if (seq !== videoSeq) return // 已经有更新的请求在跑，丢弃这次结果
      const items = d.items || []
      ctx.videos.value = append ? ctx.videos.value.concat(items) : items
      // 本页取满就认为还有下一页；不足一页（含空页）即到底。
      ctx.videoHasMore.value = items.length >= requestedSize
      ctx.videoPage.value = d.page || page
      ctx.videoSize.value = d.size || 24
    } finally {
      if (seq === videoSeq) {
        ctx.videoLoading.value = false
        ctx.videoLoadingMore.value = false
        // 列表追加后哨兵可能仍在视口内（IntersectionObserver 不会重复触发），
        // 等 DOM 更新完再重新 observe 一次强制按当前状态回调，
        // 否则会卡在「还有下一页但不加载」。
        nextTick(kickSentinel)
      }
    }
  }

  function kickSentinel() {
    const el = ctx.videoSentinel.value
    if (!videoObserver || !el) return
    videoObserver.unobserve(el)
    videoObserver.observe(el)
  }
  // 整表重载：搜索、切换筛选、刷新、从别的页签切回来都走这里，回到第一页。
  function loadVideos() {
    if (searchTimer) { clearTimeout(searchTimer); searchTimer = null }
    ctx.videoPage.value = 1
    return fetchVideos(1, false)
  }
  // 输入框连续输入时延迟检索，回车 / 搜索按钮仍走 searchVideos 立即执行。
  function debouncedSearch() {
    if (searchTimer) clearTimeout(searchTimer)
    searchTimer = setTimeout(() => {
      searchTimer = null
      loadVideos().catch(e => { ctx.error.value = e.message })
    }, SEARCH_DEBOUNCE_MS)
  }
  // 只清搜索词、保留其余筛选条件（与「清空筛选」区分）。
  function clearSearch() {
    if (searchTimer) { clearTimeout(searchTimer); searchTimer = null }
    ctx.videoQuery.value = ''
    searchVideos()
  }
  // 底部哨兵进入视口时加载下一页（已加载完或正在加载则忽略）。
  function loadMoreVideos() {
    if (ctx.videoLoading.value || ctx.videoLoadingMore.value) return Promise.resolve()
    if (!ctx.videoHasMore.value) return Promise.resolve()
    return fetchVideos(ctx.videoPage.value + 1, true)
  }
  function searchVideos() { loadVideos().catch(e => { ctx.error.value = e.message }) }

  // 把联动结果写回筛选 ref。changed 为 null 表示从分类树跳转后的校正。
  function applyFilterLinkage(changed) {
    const state = { source: ctx.videoSource.value, category: ctx.videoCategory.value }
    const next = linkedFilters(state, changed, { category: ctx.categories.value })
    ctx.videoSource.value = next.source
    ctx.videoCategory.value = next.category
  }

  // 筛选下拉的统一 change 入口：先写回本次变化的值，再联动校正，最后重载列表。
  // 显式从事件取值，不依赖 v-model 与 @change 的监听器执行顺序。
  function onFilterChange(changed, e) {
    const value = e?.target?.value ?? ''
    if (changed === 'sort') {
      ctx.videoSort.value = value
    } else {
      if (changed === 'source') ctx.videoSource.value = value
      else if (changed === 'category') ctx.videoCategory.value = value
      applyFilterLinkage(changed)
    }
    searchVideos()
  }

  // 从分类树跳转过来时，同样把站点同步到已选分类所属的站点。
  function applyLinkage() { applyFilterLinkage(null) }

  function clearAllFilters() {
    ctx.videoQuery.value = ''
    ctx.videoSource.value = ''
    ctx.videoCategory.value = ''
    searchVideos()
  }

  function showCategoryVideos(c) {
    ctx.videoCategory.value = String(c.id)
    applyLinkage()
    ctx.videoPage.value = 1
    ctx.tab.value = 'videos'
    loadVideos().catch(e => { ctx.error.value = e.message })
  }

  // ---------- 资源库底部无限滚动 ----------
  // 用一个空哨兵元素 + IntersectionObserver：哨兵进入视口就加载下一页。
  // rootMargin 提前 600px 触发，滚动时基本感觉不到等待。
  watch(ctx.videoSentinel, (el) => {
    if (videoObserver) { videoObserver.disconnect(); videoObserver = null }
    if (!el) return
    videoObserver = new IntersectionObserver((entries) => {
      if (entries.some(e => e.isIntersecting)) loadMoreVideos().catch(e => { ctx.error.value = e.message })
    }, { rootMargin: '600px 0px' })
    videoObserver.observe(el)
  })
  onBeforeUnmount(() => {
    if (videoObserver) videoObserver.disconnect()
    if (searchTimer) { clearTimeout(searchTimer); searchTimer = null }
  })

  return {
    visibleCategories, hasActiveFilters, visibleVideoTotal,
    fetchVideos, kickSentinel, loadVideos, loadMoreVideos, searchVideos,
    debouncedSearch, clearSearch,
    applyFilterLinkage, onFilterChange, applyLinkage, clearAllFilters, showCategoryVideos,
  }
}
