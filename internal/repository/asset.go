package repository

import (
	"github.com/SilenCN/AssetSpider/internal/model"
	"gorm.io/gorm"
)

// AssetRepository 是 Asset 实体的数据访问契约，只暴露该实体的方法。
type AssetRepository interface {
	List() ([]model.Asset, error)
	Create(a *model.Asset) error
	Delete(id uint) error
}

// assetRepository 是 Asset 实体专属的仓储对象，仅持有共享连接。
type assetRepository struct {
	db *gorm.DB
}

func NewAssetRepository(db *gorm.DB) AssetRepository {
	return &assetRepository{db: db}
}

var _ AssetRepository = (*assetRepository)(nil)

// List 按 ID 倒序返回全部资产。
func (r *assetRepository) List() ([]model.Asset, error) {
	var items []model.Asset
	if err := r.db.Order("id DESC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// Create 新增一条资产并回填主键。
func (r *assetRepository) Create(a *model.Asset) error {
	return r.db.Create(a).Error
}

// Delete 按 ID 删除资产。
func (r *assetRepository) Delete(id uint) error {
	return r.db.Delete(&model.Asset{}, id).Error
}
