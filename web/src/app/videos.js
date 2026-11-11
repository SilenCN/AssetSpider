// 资源库：筛选条件（含站点主轴的双向联动）、分页与底部无限滚动。
import { computed, nextTick, onBeforeUnmount, watch } from 'vue'
import { linkedFilters, scopeOptions } from '../filterLinkage.js'

export function createVideos(ctx) {
  // 请求序号：搜索词或筛选条件连续变化时，只认最后一次请求的结果，
  // 避免先发后到的旧响应把新结果覆盖掉。
  let videoSeq = 0
  let videoObserver = null

  // ---------- 筛选条件双向联动 ----------
  // 分类 / 标签 / 地域都归属于某个站点（sourceId），站点是筛选的主轴：
  // 选了站点就只列出该站点的选项；反过来选中某个分类 / 标签 / 地域，
  // 站点会自动定位过去，并清掉属于其它站点的已选条件，避免必然为空的组合。
  const visibleCategories = computed(() => scopeOptions(ctx.categories.value, ctx.videoSource.value))
  const visibleTags = computed(() => scopeOptions(ctx.tags.value, ctx.videoSource.value))
  const visibleAreas = computed(() => scopeOptions(ctx.areas.value, ctx.videoSource.value))
  const hasActiveFilters = computed(() => !!(
    ctx.videoQuery.value.trim() || ctx.videoSource.value || ctx.videoCategory.value ||
    ctx.videoTag.value || ctx.videoAreaId.value || ctx.videoYear.value
  ))
  // 地域与标签同构：每个资源站一套，因此站点未定时下拉按站点分组（optgroup），
  // 避免「美国」在多站点各出现一次时看起来像重复项；站点已定时只剩一组，直接平铺。
  const areaGroups = computed(() => {
    const groups = new Map()
    for (const a of visibleAreas.value) {
      if (!groups.has(a.sourceId)) {
        groups.set(a.sourceId, { sourceId: a.sourceId, name: ctx.siteNameOf(a.sourceId), items: [] })
      }
      groups.get(a.sourceId).items.push(a)
    }
    return [...groups.values()]
  })

  // 「可见影片」与资源库页签的计数徽标都取站点表上的影片数快照（videoCount）：
  // 列表接口不返回总数，也就不需要对 videos 表做 COUNT。
  // 它与「可见站点」同源，因此资源库里的搜索/分类/标签筛选不会改变这个数值。
  const visibleVideoTotal = computed(() =>
    ctx.sources.value.reduce((n, s) => n + (Number(s.videoCount) || 0), 0)
  )

  // 拉取一页影片。append=true 时追加到现有列表（无限滚动），否则整表替换（搜索/筛选/刷新）。
  // 接口只返回 items / page / size，是否还有下一页由本页条数与分页条数推导。
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
      if (ctx.videoTag.value) p.set('tag_id', String(ctx.videoTag.value))
      if (ctx.videoAreaId.value) p.set('area_id', String(ctx.videoAreaId.value))
      if (ctx.videoYear.value) p.set('year', ctx.videoYear.value)
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
    ctx.videoPage.value = 1
    return fetchVideos(1, false)
  }
  // 底部哨兵进入视口时加载下一页（已加载完或正在加载则忽略）。
  function loadMoreVideos() {
    if (ctx.videoLoading.value || ctx.videoLoadingMore.value) return Promise.resolve()
    if (!ctx.videoHasMore.value) return Promise.resolve()
    return fetchVideos(ctx.videoPage.value + 1, true)
  }
  function searchVideos() { loadVideos().catch(e => { ctx.error.value = e.message }) }

  async function loadAreas() {
    try { ctx.areas.value = (await ctx.request('/api/areas/v1')) || [] } catch (e) { ctx.areas.value = [] }
  }
  // 年份是影片上的单值属性（不分站点各一套），因此下拉只取一次全局列表；
  // 选项形如 [{ year: '2025', videoCount: 12 }]，按年份倒序。
  async function loadYears() {
    try { ctx.years.value = (await ctx.request('/api/videos/v1/years')) || [] } catch (e) { ctx.years.value = [] }
  }

  // 把联动结果写回各筛选 ref。changed 为 null 表示从分类树 / 卡片标签跳转后的校正。
  function applyFilterLinkage(changed) {
    const state = {
      source: ctx.videoSource.value, category: ctx.videoCategory.value,
      tag: ctx.videoTag.value, area: ctx.videoAreaId.value,
    }
    const next = linkedFilters(state, changed, {
      category: ctx.categories.value, tag: ctx.tags.value, area: ctx.areas.value,
    })
    ctx.videoSource.value = next.source
    ctx.videoCategory.value = next.category
    ctx.videoTag.value = next.tag
    ctx.videoAreaId.value = next.area
  }

  // 筛选下拉的统一 change 入口：先写回本次变化的值，再联动校正，最后重载列表。
  // 显式从事件取值，不依赖 v-model 与 @change 的监听器执行顺序。
  function onFilterChange(changed, e) {
    const value = e?.target?.value ?? ''
    if (changed === 'sort') {
      ctx.videoSort.value = value
    } else if (changed === 'year') {
      // 年份是影片上的单值属性，不属于任何站点，因此不参与站点联动。
      ctx.videoYear.value = value
    } else {
      if (changed === 'source') ctx.videoSource.value = value
      else if (changed === 'category') ctx.videoCategory.value = value
      else if (changed === 'tag') ctx.videoTag.value = value
      else ctx.videoAreaId.value = value
      applyFilterLinkage(changed)
    }
    searchVideos()
  }

  // 从分类树 / 卡片标签跳转过来时，同样把站点同步到已选条件所属的站点。
  function applyLinkage() { applyFilterLinkage(null) }

  function clearAllFilters() {
    ctx.videoQuery.value = ''
    ctx.videoSource.value = ''
    ctx.videoCategory.value = ''
    ctx.videoTag.value = ''
    ctx.videoAreaId.value = ''
    ctx.videoYear.value = ''
    searchVideos()
  }

  function showCategoryVideos(c) {
    ctx.videoCategory.value = String(c.id)
    applyLinkage()
    ctx.videoPage.value = 1
    ctx.tab.value = 'videos'
    loadVideos().catch(e => { ctx.error.value = e.message })
  }
  // 点击卡片上的标签：按该标签筛选资源库（标签名大小写不敏感匹配）。
  // 标签按站点隔离，优先取当前影片所在站点的同名标签，避免跳到别的站点去。
  function filterByTag(name, sourceId) {
    const matched = ctx.tags.value.filter(x =>
      (x.name || '').toLowerCase() === String(name).toLowerCase())
    const t = matched.find(x => Number(x.sourceId) === Number(sourceId)) || matched[0]
    if (!t) return
    ctx.videoTag.value = String(t.id)
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
  onBeforeUnmount(() => { if (videoObserver) videoObserver.disconnect() })

  return {
    visibleCategories, visibleTags, visibleAreas, hasActiveFilters, areaGroups,
    visibleVideoTotal, fetchVideos, kickSentinel, loadVideos, loadMoreVideos, searchVideos,
    loadAreas, loadYears, applyFilterLinkage, onFilterChange, applyLinkage, clearAllFilters,
    showCategoryVideos, filterByTag,
  }
}
