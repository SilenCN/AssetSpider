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

// EnsureForVideos 在单个事务里为本批次出现的全部分类做「有则复用、无则创建」。
// 分类键带 sourceID 限定，因此每个资源站拥有独立的一套分类，不会跨站串分类。
func (r *categoryRepository) EnsureForVideos(sourceID uint, videos []model.Video) (map[string]uint, error) {
	candidates := make(map[string]model.Category)
	for i := range videos {
		v := &videos[i]
		key := utils.CategoryKey(v.TypeID, v.TypeName)
		if key == "" {
			continue
		}
		if _, exists := candidates[key]; exists {
			continue
		}
		name := strings.TrimSpace(v.TypeName)
		if name == "" {
			name = strings.TrimSpace(v.TypeID) // 站点只给了 ID 时用 ID 兜底展示
		}
		candidates[key] = model.Category{
			SourceID: sourceID,
			Key:      key,
			TypeID:   strings.TrimSpace(v.TypeID),
			Name:     name,
		}
	}
	out := make(map[string]uint, len(candidates))
	if len(candidates) == 0 {
		return out, nil
	}

	err := r.db.Transaction(func(tx *gorm.DB) error {
		for key, cat := range candidates {
			var existing model.Category
			// 用 Find 而非 First，避免「不存在」时 GORM 打印 warn 日志。
			found := tx.Where("source_id = ? AND cat_key = ?", sourceID, key).
				Limit(1).Find(&existing)
			if found.Error != nil {
				return found.Error
			}
			if found.RowsAffected > 0 {
				// 同一 type_id 改名时同步分类名，但不新建记录。
				if cat.Name != "" && existing.Name != cat.Name {
					if err := tx.Model(&existing).Update("name", cat.Name).Error; err != nil {
						return err
					}
				}
				out[key] = existing.ID
				continue
			}
			if err := tx.Create(&cat).Error; err != nil {
				return fmt.Errorf("创建分类 %s/%s: %w", cat.TypeID, cat.Name, err)
			}
			out[key] = cat.ID
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
