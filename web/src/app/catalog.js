// 分类树（站点 → 一级分类 → 二级分类）与分类 / 标签下拉的展示标签。
import { computed, reactive } from 'vue'

export function createCatalog(ctx) {
  // 分类按站点隔离（source_id + cat_key 唯一），因此顶层用资源站点名分组，
  // 站点下再挂一级分类，一级分类下挂二级分类，构成最多三层的树。
  const catCollapsed = reactive(new Set())

  function isCatCollapsed(key) { return catCollapsed.has(key) }
  function toggleCat(key) {
    if (catCollapsed.has(key)) catCollapsed.delete(key)
    else catCollapsed.add(key)
  }
  // 虚拟分组节点没有分类 ID，用自身的 key 作为折叠标识
  function catKey(node) { return node.virtual ? node.key : `cat-${node.id}` }

  // 一级分类通常自身没有影片（纯容器），直接显示 videoCount 会是「0 部」。
  // 这里把子分类的影片数累加进来，作为该分类及其子树的展示数量。
  function catVideoCount(node) {
    if (!node) return 0
    return (Number(node.videoCount) || 0) +
      (node.children || []).reduce((n, ch) => n + catVideoCount(ch), 0)
  }

  const categoryForest = computed(() => {
    const sites = new Map()
    for (const node of ctx.categoryTree.value) {
      const sid = node.sourceId || 0
      if (!sites.has(sid)) {
        sites.set(sid, {
          key: `site-${sid}`, sourceId: sid, name: ctx.siteNameOf(sid),
          children: [], catCount: 0, videoCount: 0,
        })
      }
      sites.get(sid).children.push(node)
    }
    const order = new Map(ctx.sources.value.map((s, i) => [s.id, i]))
    return [...sites.values()]
      .map(site => {
        // 后端把「找不到父分类」的二级分类当成根节点一并返回（父分类可能没被采到，
        // 例如站点只返回了二级分类的影片）。这类节点不直接铺在站点下，否则看起来
        // 像是站点的一级分类，统一收进「未归类」分组。
        const roots = []
        const orphans = []
        for (const node of site.children) {
          if (node.level === 2) orphans.push(node)
          else roots.push(node)
        }
        let catCount = 0
        const walk = n => {
          catCount++
          for (const ch of n.children || []) walk(ch)
        }
        roots.forEach(walk)
        orphans.forEach(walk)
        // 父节点的 videoCount 已含子树，站点总数只累加根节点与孤儿，避免重复计数
        let videoCount = 0
        for (const n of roots) videoCount += catVideoCount(n)
        for (const n of orphans) videoCount += catVideoCount(n)
        if (orphans.length) {
          roots.push({
            key: `orphan-${site.sourceId}`, virtual: true, name: '未归类',
            hint: '父分类未采集到，暂挂在此', videoCount: 0, // 只作为容器，数量由子节点累加
            children: orphans,
          })
        }
        site.children = roots
        site.catCount = catCount
        site.videoCount = videoCount
        return site
      })
      .sort((a, b) => (order.get(a.sourceId) ?? 1e6) - (order.get(b.sourceId) ?? 1e6))
  })

  const catTotal = computed(() => categoryForest.value.reduce(
    (n, s) => n + s.catCount, 0))

  // 分类下拉里带上层级路径（一级 / 二级），避免不同站点的同名分类难以区分
  const catPath = computed(() => {
    const m = new Map()
    const walk = (n, prefix) => {
      const path = prefix ? `${prefix} / ${n.name}` : n.name
      m.set(n.id, path)
      for (const ch of n.children || []) walk(ch, path)
    }
    ctx.categoryTree.value.forEach(n => walk(n, ''))
    return m
  })

  // 站点已定时下拉里不必再重复站点名；站点未定时保留前缀，避免同名选项混淆。
  function catSelectLabel(c) {
    const path = catPath.value.get(c.id) || c.name
    return ctx.videoSource.value ? path : `${ctx.siteNameOf(c.sourceId)} · ${path}`
  }
  function tagSelectLabel(t) {
    return ctx.videoSource.value ? t.name : `${ctx.siteNameOf(t.sourceId)} · ${t.name}`
  }

  function toggleAllCats() {
    const allOpen = categoryForest.value.every(s => !isCatCollapsed(s.key))
    for (const site of categoryForest.value) {
      const keys = [site.key]
      const walk = n => { if (n.children?.length) keys.push(catKey(n)); (n.children || []).forEach(walk) }
      site.children.forEach(walk)
      for (const k of keys) {
        if (allOpen) catCollapsed.add(k)
        else catCollapsed.delete(k)
      }
    }
  }

  return {
    catCollapsed, isCatCollapsed, toggleCat, catKey, catVideoCount,
    categoryForest, catTotal, catPath, catSelectLabel, tagSelectLabel, toggleAllCats,
  }
}
