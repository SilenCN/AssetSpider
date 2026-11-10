// 资源库筛选条件的联动规则（纯逻辑，不依赖 Vue，可单独测试）。
//
// 分类 / 标签 / 地域都归属于某个站点（sourceId），站点是筛选的主轴：
//   - 选中站点：其它三类下拉只列该站点的选项，跨站点的已选条件被清掉；
//   - 选中分类 / 标签 / 地域：站点自动定位到它的所属站点，并清掉跨站点的条件；
//   - 从分类树、卡片标签跳转过来（changed 为 null）：按已有条件推断站点并校正。
//
// 所有 id 统一按字符串比较：下拉的 value 既可能是数字也可能是字符串。

export const FILTER_KINDS = ['category', 'tag', 'area']

// 按当前站点收窄候选项；站点为空时返回全部。
export function scopeOptions(items, sourceId) {
  const sid = Number(sourceId || 0)
  if (!sid) return items
  return items.filter(item => Number(item.sourceId || 0) === sid)
}

// 某个已选条件归属的站点；选项列表还没加载完时返回 0，表示暂不参与联动。
export function ownerOf(kind, id, dataset) {
  if (!id) return 0
  const item = (dataset[kind] || []).find(x => String(x.id) === String(id))
  return item ? Number(item.sourceId || 0) : 0
}

// 计算联动后的筛选状态，不修改入参。
// state:   { source, category, tag, area }
// changed: 'source' | 'category' | 'tag' | 'area' | null
// dataset: { category: [...], tag: [...], area: [...] }
export function linkedFilters(state, changed, dataset) {
  const next = { ...state }
  let sid = 0

  if (changed === 'source') {
    // 站点是主轴：它变了，其余条件只能被动校正。
    sid = Number(next.source || 0)
  } else if (changed && FILTER_KINDS.includes(changed)) {
    // 双向联动：选中带站点归属的选项，就把站点定位过去。
    sid = ownerOf(changed, next[changed], dataset)
    if (sid) next.source = String(sid)
  } else {
    // 外部跳转后的校正：按已选条件依次推断站点。
    for (const kind of FILTER_KINDS) {
      sid = ownerOf(kind, next[kind], dataset)
      if (sid) {
        next.source = String(sid)
        break
      }
    }
  }

  if (!sid) sid = Number(next.source || 0)
  if (sid) {
    for (const kind of FILTER_KINDS) {
      const owner = ownerOf(kind, next[kind], dataset)
      // 只清「明确属于别的站点」的条件；查不到归属时保留，避免数据未加载完被误清。
      if (owner && owner !== sid) next[kind] = ''
    }
  }
  return next
}
