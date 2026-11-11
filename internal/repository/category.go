package repository

import (
	"fmt"
	"sort"
	"strings"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/utils"
	"gorm.io/gorm"
)

// CategoryRepository 是 Category 实体的数据访问契约。
type CategoryRepository interface {
	// EnsureForVideos 按视频自带的分类信息幂等创建本站分类，
	// 返回 utils.CategoryKey → 分类主键 的映射，供 service 回填 Video.CategoryID。
	EnsureForVideos(sourceID uint, videos []model.Video) (map[string]uint, error)
	// ApplyTypeNames 用苹果CMS typelist 返回的权威分类名称补全本源分类的名称。
	ApplyTypeNames(sourceID uint, names map[string]string) error
	// ApplyTypeTree 用苹果CMS typelist 的权威父子关系校正本站分类层级，
	// 并补建引用了却不存在的一级分类（defs 为空时也能按库内数据重建）。
	ApplyTypeTree(sourceID uint, defs []CategoryDef) error
	// visible 为可选站点白名单，语义见 visibility.go。
	List(sourceID uint, visible ...[]uint) ([]model.Category, error)
	Get(id uint, visible ...[]uint) (*model.Category, error)
}

// categoryRepository 是 Category 实体专属的仓储对象。
type categoryRepository struct {
	db *gorm.DB
}

func NewCategoryRepository(db *gorm.DB) CategoryRepository {
	return &categoryRepository{db: db}
}

var _ CategoryRepository = (*categoryRepository)(nil)

// EnsureForVideos 在单个事务里为本批次出现的全部分类做「有则复用、无则创建」，
// 并按苹果CMS 的 type_pid 模型建立二级分类层级：视频的 type_id_1 即所属一级分类 ID。
// 分类键带 sourceID 限定，因此每个资源站拥有独立的一套分类，不会跨站串分类。
//
// 关键点：真实苹果CMS 二级结构里，一级分类常是纯容器（自身没有影片），其 type_id
// 只会出现在子分类的 type_id_1 中，永远不会作为某条影片的 type_id 出现。因此本函数
// 除了影片自身的 type_id，还会从每条影片的 type_id_1 推断并预建父级分类，否则父级
// 永远无从创建，二级分类会沦为永久孤儿。
func (r *categoryRepository) EnsureForVideos(sourceID uint, videos []model.Video) (map[string]uint, error) {
	// 第一遍：去重收集候选分类，并记录每个分类的父级 type_id（来自 type_id_1）。
	candidates := make(map[string]model.Category)
	for i := range videos {
		v := &videos[i]
		key := utils.CategoryKey(v.TypeID, v.TypeName)
		if key == "" {
			continue
		}
		// 苹果CMS 视频的 type_id_1 是一级分类 ID；空、"0" 或与自身 type_id 相同视为无父级。
		pid := strings.TrimSpace(v.TypeID1)
		if pid == "" || pid == "0" || pid == strings.TrimSpace(v.TypeID) {
			pid = ""
		}
		name := strings.TrimSpace(v.TypeName)
		if name == "" {
			name = strings.TrimSpace(v.TypeID) // 站点只给了 ID 时用 ID 兜底展示
		}
		// 注册本视频自身的分类（可能是二级，也可能是一级）。
		addCandidate(candidates, key, model.Category{
			SourceID: sourceID,
			Key:      key,
			TypeID:   strings.TrimSpace(v.TypeID),
			Name:     name,
			TypePID:  pid,
		})
		// 从 type_id_1 推断并预建父级（一级）分类：父级可能是纯容器，
		// type_id 只经此途径出现，不预建则二级分类永远挂不上父级。
		if pid != "" {
			pkey := utils.CategoryKey(pid, "")
			addCandidate(candidates, pkey, model.Category{
				SourceID: sourceID,
				Key:      pkey,
				TypeID:   pid,
				Name:     "", // 视频未携带父级名称，留空待下方补 type_id 兜底 / 后续同名影片补名
				TypePID:  "",
			})
		}
	}
	out := make(map[string]uint, len(candidates))
	if len(candidates) == 0 {
		return out, nil
	}

	// 第二、三遍：事务内创建/复用分类，再按 type_pid 把二级分类挂到一级分类。
	err := r.db.Transaction(func(tx *gorm.DB) error {
		typeIDToID := make(map[string]uint)
		created := make([]model.Category, 0, len(candidates))

		// 批量 find-or-create：一次查询取回本批所有已存在分类（按 cat_key），
		// 只把缺失的批量插入；替代原先逐条 Find+Create 的 O(N) 往返。
		keys := make([]string, 0, len(candidates))
		for key := range candidates {
			keys = append(keys, key)
		}
		var found []model.Category
		if err := tx.Where("source_id = ? AND cat_key IN ?", sourceID, keys).
			Find(&found).Error; err != nil {
			return err
		}
		existingByKey := make(map[string]model.Category, len(found))
		for _, row := range found {
			existingByKey[row.Key] = row
		}

		missing := make([]model.Category, 0, len(candidates))
		missingKeys := make([]string, 0, len(candidates))
		// 按 key 排序遍历，保证 out / created 顺序稳定（map 迭代本无序）。
		sort.Strings(keys)
		for _, key := range keys {
			cat := candidates[key]
			if ex, ok := existingByKey[key]; ok {
				// 同 type_id 改名时同步分类名；父级信息后续补到也允许回填。
				if cat.TypePID != "" && ex.TypePID == "" {
					if err := tx.Model(&ex).Update("type_pid", cat.TypePID).Error; err != nil {
						return err
					}
				}
				if cat.Name != "" && ex.Name != cat.Name {
					if err := tx.Model(&ex).Update("name", cat.Name).Error; err != nil {
						return err
					}
					ex.Name = cat.Name
				}
				if ex.TypePID == "" && cat.TypePID != "" {
					ex.TypePID = cat.TypePID
				}
				out[key] = ex.ID
				if ex.TypeID != "" {
					typeIDToID[ex.TypeID] = ex.ID
				}
				created = append(created, ex)
				continue
			}
			if cat.Name == "" {
				cat.Name = cat.TypeID // 父级无名称时以 type_id 兜底展示
			}
			missing = append(missing, cat)
			missingKeys = append(missingKeys, key)
		}
		if len(missing) > 0 {
			if err := tx.CreateInBatches(&missing, 200).Error; err != nil {
				return fmt.Errorf("批量创建分类: %w", err)
			}
			for i, cat := range missing {
				out[missingKeys[i]] = cat.ID
				if cat.TypeID != "" {
					typeIDToID[cat.TypeID] = cat.ID
				}
				created = append(created, cat)
			}
		}

		// 第三遍：根据 type_pid 建立父子关联并标记层级（1=一级 2=二级）。
		for _, cat := range created {
			if cat.TypePID == "" {
				if cat.Level != 1 {
					if err := tx.Model(&model.Category{}).Where("id = ?", cat.ID).
						Update("level", 1).Error; err != nil {
						return err
					}
				}
				continue
			}
			// 父级在同批中出现：挂接并记为二级。
			if parentID, ok := typeIDToID[cat.TypePID]; ok && parentID != cat.ID {
				if cat.ParentID != parentID {
					if err := tx.Model(&model.Category{}).Where("id = ?", cat.ID).
						Updates(map[string]interface{}{"parent_id": parentID, "level": 2}).Error; err != nil {
						return err
					}
				}
				continue
			}
			// 父级尚未采集到：先记为二级，待后续采集到父级分类时再挂接。
			if cat.Level != 2 {
				if err := tx.Model(&model.Category{}).Where("id = ?", cat.ID).
					Update("level", 2).Error; err != nil {
					return err
				}
			}
		}
		// 兜底归树：把本源内所有「父级 ID 已知（type_pid 非空）但尚未挂接
		// （parent_id=0）」的二级分类，按其 type_pid 挂到已存在的父级上。
		// 覆盖两类情况：① 父级在本批由子分类的 type_id_1 推断创建；
		// ② 父级在更早批次已存在、但本批未直接出现——原逻辑只认本批父级，
		// 会导致这类子树永远归不上父级。
		var orphans []model.Category
		if err := tx.Where("source_id = ? AND type_pid <> '' AND parent_id = 0", sourceID).
			Find(&orphans).Error; err != nil {
			return err
		}
		for _, o := range orphans {
			var parent model.Category
			if err := tx.Where("source_id = ? AND type_id = ?", sourceID, o.TypePID).
				Limit(1).Find(&parent).Error; err != nil {
				return err
			}
			if parent.ID == 0 || parent.ID == o.ID {
				continue
			}
			if err := tx.Model(&model.Category{}).Where("id = ?", o.ID).
				Updates(map[string]interface{}{"parent_id": parent.ID, "level": 2}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// ApplyTypeNames 用苹果CMS typelist 返回的权威分类名称补全本源分类的名称。
//
// videolist 不携带父级（纯容器）分类名称，这些分类在 EnsureForVideos 里只能以
// type_id 兜底展示。typelist 一次性给出本站全部分类的真实名称，这里按 type_id
// 回填：仅当分类「尚无名称」或「名称只是 type_id 兜底占位」时才更新，已带真实
// 名称的分类不会被 typelist 覆盖。幂等，可重复调用；names 为空时直接返回。
func (r *categoryRepository) ApplyTypeNames(sourceID uint, names map[string]string) error {
	if len(names) == 0 {
		return nil
	}
	var items []model.Category
	if err := r.db.Where("source_id = ?", sourceID).Find(&items).Error; err != nil {
		return err
	}
	for _, cat := range items {
		if cat.TypeID == "" {
			continue
		}
		nm, ok := names[cat.TypeID]
		if !ok {
			continue
		}
		nm = strings.TrimSpace(nm)
		if nm == "" {
			continue
		}
		// 仅回填缺失/占位的名称，保护已存在的真实名称。
		if cat.Name == "" || cat.Name == cat.TypeID {
			if err := r.db.Model(&model.Category{}).Where("id = ?", cat.ID).
				Update("name", nm).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// CategoryDef 是苹果CMS typelist 返回的一条分类定义。
type CategoryDef struct {
	TypeID       string
	Name         string
	ParentTypeID string // type_pid，"0" / 空表示一级分类
}

// ApplyTypeTree 把 typelist 的权威父子关系写回本站分类，再重建整棵树。
//
// 与 EnsureForVideos 的差别：有的站点 videolist 根本不返回 type_id_1，层级只存在于
// typelist 的 type_pid 里；也有老库只存了二级分类、一级分类从未被建出来。这里统一
// 以「被引用过的父级 ID」为准补建一级分类，避免二级分类永远沦为「未归类」。
// 只在库里已存在该分类时改写它的父级信息，不凭空创建没有影片的叶子分类。
func (r *categoryRepository) ApplyTypeTree(sourceID uint, defs []CategoryDef) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		names := make(map[string]string)
		referenced := make(map[string]bool)
		items := make([]CategoryDef, 0, len(defs))
		for _, d := range defs {
			id := strings.TrimSpace(d.TypeID)
			if id == "" {
				continue
			}
			pid := strings.TrimSpace(d.ParentTypeID)
			if pid == "0" || pid == id {
				pid = ""
			}
			nm := strings.TrimSpace(d.Name)
			if nm != "" {
				names[id] = nm
			}
			if pid != "" {
				referenced[pid] = true
			}
			items = append(items, CategoryDef{TypeID: id, Name: nm, ParentTypeID: pid})
		}
		for _, d := range items {
			var cat model.Category
			if err := tx.Where("source_id = ? AND TRIM(COALESCE(type_id, '')) = ?", sourceID, d.TypeID).
				Order("id ASC").Limit(1).Find(&cat).Error; err != nil {
				return err
			}
			if cat.ID == 0 {
				continue // 站点定义了但本站还没采到影片的叶子分类，不预先建空分类
			}
			updates := map[string]interface{}{}
			if d.ParentTypeID != "" && cat.TypePID != d.ParentTypeID {
				updates["type_pid"] = d.ParentTypeID
			}
			// typelist 明确说它是父级时清掉残留的父级引用，避免一级分类被当成二级。
			if referenced[d.TypeID] && d.ParentTypeID == "" && cat.TypePID != "" {
				updates["type_pid"] = ""
			}
			if d.Name != "" && (cat.Name == "" || cat.Name == cat.TypeID || cat.Name == d.TypeID) && cat.Name != d.Name {
				updates["name"] = d.Name
			}
			if len(updates) > 0 {
				if err := tx.Model(&model.Category{}).Where("id = ?", cat.ID).Updates(updates).Error; err != nil {
					return err
				}
			}
		}
		return rebuildCategoryTree(tx, sourceID, names)
	})
}

// categoryParentRef 是一条「本源内被引用过的父级 type_id」。
type categoryParentRef struct {
	SourceID uint   `gorm:"column:source_id"`
	PID      string `gorm:"column:pid"`
}

// referencedParentIDs 汇总两个来源的父级引用：
//   - videos.type_id_1：采集时的一级分类 ID；
//   - categories.type_pid：老库里已经记下的父级 ID。
//
// 排除 "0"、空值与「父级等于自身」这三种无效引用。sourceID 为 0 时查全部站点。
func referencedParentIDs(tx *gorm.DB, sourceID uint) ([]categoryParentRef, error) {
	const sql = `
WITH refs AS (
  SELECT source_id, TRIM(type_id_1) AS pid FROM videos
   WHERE TRIM(COALESCE(type_id_1, '')) NOT IN ('', '0')
     AND TRIM(COALESCE(type_id_1, '')) <> TRIM(COALESCE(type_id, ''))
  UNION
  SELECT source_id, TRIM(type_pid) AS pid FROM categories
   WHERE TRIM(COALESCE(type_pid, '')) NOT IN ('', '0')
     AND TRIM(COALESCE(type_pid, '')) <> TRIM(COALESCE(type_id, ''))
)
SELECT source_id, pid FROM refs
 WHERE (? = 0 OR source_id = ?)
 GROUP BY source_id, pid`
	var rows []categoryParentRef
	if err := tx.Raw(sql, sourceID, sourceID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// rebuildCategoryTree 补建缺失的一级分类并把二级分类挂到父级之下。
//
// 这是「缺失一级分类」的通用修复入口：只依赖库内已有数据，不需要重新采集、也不需要
// 网络。可重复执行（幂等）；names 提供 typelist 的权威名称，没有时以 type_id 占位。
func rebuildCategoryTree(tx *gorm.DB, sourceID uint, names map[string]string) error {
	refs, err := referencedParentIDs(tx, sourceID)
	if err != nil {
		return err
	}
	// 第一遍：确保每个被引用的父级都有一级分类记录，并把它规范成一级。
	for _, ref := range refs {
		pid := strings.TrimSpace(ref.PID)
		if pid == "" {
			continue
		}
		var parent model.Category
		if err := tx.Where("source_id = ? AND TRIM(COALESCE(type_id, '')) = ?", ref.SourceID, pid).
			Order("id ASC").Limit(1).Find(&parent).Error; err != nil {
			return err
		}
		key := utils.CategoryKey(pid, "")
		if parent.ID == 0 {
			if err := tx.Where("source_id = ? AND cat_key = ?", ref.SourceID, key).
				Order("id ASC").Limit(1).Find(&parent).Error; err != nil {
				return err
			}
			if parent.ID != 0 && strings.TrimSpace(parent.TypeID) != pid {
				if err := tx.Model(&model.Category{}).Where("id = ?", parent.ID).
					Update("type_id", pid).Error; err != nil {
					return err
				}
				parent.TypeID = pid
			}
		}
		if parent.ID == 0 {
			name := strings.TrimSpace(names[pid])
			if name == "" {
				name = pid // 无权威名称时以 type_id 占位，后续 typelist 会补全
			}
			parent = model.Category{
				SourceID: ref.SourceID, Key: key, TypeID: pid,
				Name: name, TypePID: "", ParentID: 0, Level: 1,
			}
			if err := tx.Create(&parent).Error; err != nil {
				return fmt.Errorf("补建一级分类 %s/%d: %w", pid, ref.SourceID, err)
			}
		}
		updates := map[string]interface{}{}
		if parent.Level != 1 {
			updates["level"] = 1
		}
		if parent.ParentID != 0 {
			updates["parent_id"] = 0
		}
		if parent.TypePID != "" {
			updates["type_pid"] = ""
		}
		if nm := strings.TrimSpace(names[pid]); nm != "" &&
			(parent.Name == "" || parent.Name == parent.TypeID || parent.Name == pid) && parent.Name != nm {
			updates["name"] = nm
		}
		if len(updates) > 0 {
			if err := tx.Model(&model.Category{}).Where("id = ?", parent.ID).Updates(updates).Error; err != nil {
				return err
			}
		}
	}
	// 第二遍：把带 type_pid 的二级分类按父级 type_id 挂接。
	// 父级刚在第一遍建好，因此老库里「未归类」的那批二级分类这次能归位。
	var children []model.Category
	if err := tx.Where(`(? = 0 OR source_id = ?)
		AND TRIM(COALESCE(type_pid, '')) NOT IN ('', '0')`, sourceID, sourceID).
		Find(&children).Error; err != nil {
		return err
	}
	for _, ch := range children {
		pid := strings.TrimSpace(ch.TypePID)
		if pid == "" || pid == "0" || pid == strings.TrimSpace(ch.TypeID) {
			continue
		}
		var parent model.Category
		if err := tx.Where("source_id = ? AND TRIM(COALESCE(type_id, '')) = ?", ch.SourceID, pid).
			Order("id ASC").Limit(1).Find(&parent).Error; err != nil {
			return err
		}
		if parent.ID == 0 || parent.ID == ch.ID {
			continue
		}
		if ch.ParentID != parent.ID || ch.Level != 2 {
			if err := tx.Model(&model.Category{}).Where("id = ?", ch.ID).
				Updates(map[string]interface{}{"parent_id": parent.ID, "level": 2}).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// backfillCategoryParents 是启动时的老库修复：按库内已有的 type_id_1 / type_pid
// 补建缺失的一级分类并重建父子关系。由 prepareDatabase 在版本升级时调用一次，
// 重复执行是空转。
func backfillCategoryParents(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		return rebuildCategoryTree(tx, 0, nil)
	})
}

// addCandidate 把候选分类并入收集表；键冲突时保留更有信息量的字段：
// 名称 / 父级 ID / 自身 type_id 均优先取非空值（父级兜底名不能覆盖真实名称）。
// 分类键相同但分别来自「影片自身 type_id」与「其它影片的 type_id_1」时，
// 非空字段的合并能保证父级容器被创建、且真实分类名不丢失。
func addCandidate(m map[string]model.Category, key string, c model.Category) {
	if old, ok := m[key]; ok {
		if old.Name == "" && c.Name != "" {
			old.Name = c.Name
		}
		if old.TypePID == "" && c.TypePID != "" {
			old.TypePID = c.TypePID
		}
		if old.TypeID == "" && c.TypeID != "" {
			old.TypeID = c.TypeID
		}
		m[key] = old
		return
	}
	m[key] = c
}

// List 返回分类，按站点与 ID 正序；sourceID 为 0 时返回全部站点的分类。
func (r *categoryRepository) List(sourceID uint, visible ...[]uint) ([]model.Category, error) {
	q := applyVisibility(r.db.Model(&model.Category{}), "source_id", firstVisible(visible...))
	if sourceID > 0 {
		q = q.Where("source_id = ?", sourceID)
	}
	var items []model.Category
	if err := q.Order("source_id ASC, id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// Get 按主键读取分类；分类所属站点不可见时按「不存在」处理。
func (r *categoryRepository) Get(id uint, visible ...[]uint) (*model.Category, error) {
	var cat model.Category
	q := applyVisibility(r.db.Model(&model.Category{}), "source_id", firstVisible(visible...))
	if err := q.First(&cat, id).Error; err != nil {
		return nil, notFound(err)
	}
	return &cat, nil
}
