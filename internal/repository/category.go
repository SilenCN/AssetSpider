package repository

import (
	"fmt"
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
	List(sourceID uint) ([]model.Category, error)
	Get(id uint) (*model.Category, error)
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
		if c, ok := candidates[key]; ok {
			// 同批多次出现：若之前没拿到父级而现在有，补上父级信息。
			if c.TypePID == "" && pid != "" {
				c.TypePID = pid
				candidates[key] = c
			}
			continue
		}
		candidates[key] = model.Category{
			SourceID: sourceID,
			Key:      key,
			TypeID:   strings.TrimSpace(v.TypeID),
			Name:     name,
			TypePID:  pid,
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
		for key, cat := range candidates {
			var existing model.Category
			// 用 Find 而非 First，避免「不存在」时 GORM 打印 warn 日志。
			found := tx.Where("source_id = ? AND cat_key = ?", sourceID, key).
				Limit(1).Find(&existing)
			if found.Error != nil {
				return found.Error
			}
			if found.RowsAffected > 0 {
				// 同 type_id 改名时同步分类名；父级信息后续补到也允许回填。
				if cat.TypePID != "" && existing.TypePID == "" {
					if err := tx.Model(&existing).Update("type_pid", cat.TypePID).Error; err != nil {
						return err
					}
				}
				if cat.Name != "" && existing.Name != cat.Name {
					if err := tx.Model(&existing).Update("name", cat.Name).Error; err != nil {
						return err
					}
				}
				out[key] = existing.ID
				if existing.TypeID != "" {
					typeIDToID[existing.TypeID] = existing.ID
				}
				created = append(created, existing)
				continue
			}
			if err := tx.Create(&cat).Error; err != nil {
				return fmt.Errorf("创建分类 %s/%s: %w", cat.TypeID, cat.Name, err)
			}
			out[key] = cat.ID
			if cat.TypeID != "" {
				typeIDToID[cat.TypeID] = cat.ID
			}
			created = append(created, cat)
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
		// 兜底：本批之外、之前孤立（父级未到）的二级分类，若其父级 type_pid 已在
		// 本批到位，则一并挂接，保证「父级后到」也能正确归树。
		for pid, parentID := range typeIDToID {
			if err := tx.Model(&model.Category{}).
				Where("source_id = ? AND type_pid = ? AND parent_id = 0 AND id <> ?", sourceID, pid, parentID).
				Updates(map[string]interface{}{"parent_id": parentID, "level": 2}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// List 返回分类，按站点与 ID 正序；sourceID 为 0 时返回全部站点的分类。
func (r *categoryRepository) List(sourceID uint) ([]model.Category, error) {
	q := r.db.Model(&model.Category{})
	if sourceID > 0 {
		q = q.Where("source_id = ?", sourceID)
	}
	var items []model.Category
	if err := q.Order("source_id ASC, id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// Get 按主键读取分类。
func (r *categoryRepository) Get(id uint) (*model.Category, error) {
	var cat model.Category
	if err := r.db.First(&cat, id).Error; err != nil {
		return nil, notFound(err)
	}
	return &cat, nil
}
