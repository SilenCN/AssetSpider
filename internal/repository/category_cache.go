package repository

import (
	"sort"

	"github.com/SilenCN/AssetSpider/internal/model"
	"gorm.io/gorm"
)

// catColumnsUpdate 是一条待写回的分类字段更新（按主键定点 UPDATE）。
type catColumnsUpdate struct {
	id   uint
	cols map[string]interface{}
}

// ensureCategoriesCached 是带任务级类目缓存的归档路径。
//
// 与 ensureCategoriesDB 的语义完全等价（同样的候选、同样的改名 / 补父级 / 归树
// 规则），只是把「每页一次批量 SELECT + 若干无差别 UPDATE + 每页全表扫孤儿」
// 换成缓存里的内存比对：命中且无变化就一条 SQL 都不发。
func (r *categoryRepository) ensureCategoriesCached(
	sourceID uint,
	candidates map[string]model.Category,
	c *CrawlCache,
) (map[string]uint, error) {
	keys := make([]string, 0, len(candidates))
	for key := range candidates {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	// ① 内存比对：区分「新增」「字段需更新」「完全不变」，并收集本批涉及的快照。
	var (
		updates     []catColumnsUpdate
		missing     []model.Category
		missingKeys []string
		touched     []*cachedCategory
	)
	out := make(map[string]uint, len(keys))
	for _, key := range keys {
		cand := candidates[key]
		snap, ok := c.cats[key]
		if !ok {
			if cand.Name == "" {
				cand.Name = cand.TypeID // 父级无名称时以 type_id 兜底展示
			}
			missing = append(missing, cand)
			missingKeys = append(missingKeys, key)
			continue
		}
		cols := make(map[string]interface{}, 2)
		// 同 type_id 改名时同步分类名；父级信息后续补到也允许回填。
		if cand.TypePID != "" && snap.TypePID == "" {
			cols["type_pid"] = cand.TypePID
			snap.TypePID = cand.TypePID
		}
		if cand.Name != "" && snap.Name != cand.Name {
			cols["name"] = cand.Name
			snap.Name = cand.Name
		}
		if len(cols) > 0 {
			updates = append(updates, catColumnsUpdate{id: snap.ID, cols: cols})
		}
		out[key] = snap.ID
		touched = append(touched, snap)
	}

	// ② 无新增时 catTypeID 已经是完整快照，可以先在内存里算归树差异；
	// 若连字段更新和归树差异都没有，整个事务都不必开（0 次查询、0 次写入）。
	var treeUpdates []catTreeUpdate
	if len(missing) == 0 {
		treeUpdates = collectCategoryTreeUpdates(c, touched)
		if len(updates) == 0 && len(treeUpdates) == 0 {
			return out, nil
		}
	}

	// ③ 有变化才开事务：先插入新增（拿到主键并写回缓存），再按内存算出的差异写回。
	err := r.db.Transaction(func(tx *gorm.DB) error {
		if len(missing) > 0 {
			if err := tx.CreateInBatches(&missing, 200).Error; err != nil {
				return err
			}
			for i, cat := range missing {
				out[missingKeys[i]] = cat.ID
				touched = append(touched, c.newCategorySnapshot(cat))
			}
			// 新增分类的主键此刻才确定，归树差异要在这之后重算。
			treeUpdates = collectCategoryTreeUpdates(c, touched)
		}
		for _, u := range updates {
			if err := tx.Model(&model.Category{}).Where("id = ?", u.id).
				Updates(u.cols).Error; err != nil {
				return err
			}
		}
		for _, u := range treeUpdates {
			if err := tx.Model(&model.Category{}).Where("id = ?", u.id).
				Updates(u.cols).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// catTreeUpdate 与 catColumnsUpdate 同构，单独命名只为让归树逻辑更易读。
type catTreeUpdate = catColumnsUpdate

// collectCategoryTreeUpdates 在内存里复刻 ensureCategoriesDB 的第三遍与 orphan
// 兜底，返回需要写回的差异，并同步推进缓存快照（下一页无需重算）。
//
//   - 本批候选（touched）：无父级 → level=1；父级已知 → 挂 parent_id 并 level=2；
//     父级尚未采到 → 先记 level=2。
//   - 全站孤儿：type_pid 非空且 parent_id=0 的分类，按 type_pid 找父级挂接。
//
// 原路径的这两步每页都要查库（orphan 还要全表扫描 + 逐个查父级）；这里全部落在
// 缓存里，只在结果真的与库内不同时才产出一条 UPDATE。
func collectCategoryTreeUpdates(c *CrawlCache, touched []*cachedCategory) []catTreeUpdate {
	var out []catTreeUpdate
	// 本批候选：与 ensureCategoriesDB 第三遍逐条对应。
	for _, snap := range touched {
		if snap.TypePID == "" {
			if snap.Level != 1 {
				out = append(out, catTreeUpdate{id: snap.ID,
					cols: map[string]interface{}{"level": 1}})
				snap.Level = 1
			}
			continue
		}
		if parentID, ok := c.catTypeID[snap.TypePID]; ok && parentID != snap.ID {
			if snap.ParentID != parentID {
				out = append(out, catTreeUpdate{id: snap.ID,
					cols: map[string]interface{}{"parent_id": parentID, "level": 2}})
				snap.ParentID, snap.Level = parentID, 2
			}
			continue
		}
		if snap.Level != 2 {
			out = append(out, catTreeUpdate{id: snap.ID,
				cols: map[string]interface{}{"level": 2}})
			snap.Level = 2
		}
	}
	// 兜底归树：覆盖父级由本批其它分类推断创建、或更早批次已存在但当时未挂接的子树。
	// 条件与原 SQL 一致（只看 type_pid 非空且 parent_id=0），不能按 level 过滤：
	// 「父级尚未采到」的子分类正是 level=2 / parent_id=0，等父级出现后要靠这一步挂上。
	for _, snap := range c.cats {
		if snap.TypePID == "" || snap.ParentID != 0 {
			continue
		}
		parentID, ok := c.catTypeID[snap.TypePID]
		if !ok || parentID == snap.ID {
			continue
		}
		out = append(out, catTreeUpdate{id: snap.ID,
			cols: map[string]interface{}{"parent_id": parentID, "level": 2}})
		snap.ParentID, snap.Level = parentID, 2
	}
	return out
}
