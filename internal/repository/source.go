package repository

import (
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
	"gorm.io/gorm"
)

// SourceRepository 是 Source 实体的数据访问契约。
type SourceRepository interface {
	ListSources() ([]model.Source, error)
	ListEnabledSources() ([]model.Source, error)
	GetSource(id uint) (*model.Source, error)
	CreateSource(src *model.Source) error
	UpdateSource(id uint, in *model.Source) (*model.Source, error)
	DeleteSource(id uint) error
	UpdateSourceResult(id uint, mode, status, cursor, errMsg string, at time.Time) error
}

// sourceRepository 是 Source 实体专属的仓储对象。
type sourceRepository struct {
	db *gorm.DB
}

func NewSourceRepository(db *gorm.DB) SourceRepository {
	return &sourceRepository{db: db}
}

var _ SourceRepository = (*sourceRepository)(nil)

// ListSources 返回全部站点配置，按 ID 正序。
func (r *sourceRepository) ListSources() ([]model.Source, error) {
	var items []model.Source
	if err := r.db.Order("id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// ListEnabledSources 返回所有启用站点。
func (r *sourceRepository) ListEnabledSources() ([]model.Source, error) {
	var items []model.Source
	if err := r.db.Where("enabled = ?", true).Order("id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// GetSource 按 ID 读取站点配置。
func (r *sourceRepository) GetSource(id uint) (*model.Source, error) {
	var src model.Source
	if err := r.db.First(&src, id).Error; err != nil {
		return nil, notFound(err)
	}
	return &src, nil
}

// CreateSource 新增站点配置。
func (r *sourceRepository) CreateSource(src *model.Source) error {
	return r.db.Create(src).Error
}

// UpdateSource 按 ID 全量更新站点配置的可编辑字段。
// 采集状态字段（Last*）不由前端修改，予以保留。
func (r *sourceRepository) UpdateSource(id uint, in *model.Source) (*model.Source, error) {
	src, err := r.GetSource(id)
	if err != nil {
		return nil, err
	}
	src.Name = in.Name
	src.APIBase = in.APIBase
	src.Format = in.Format
	src.ExtraParams = in.ExtraParams
	src.ParseAPI = in.ParseAPI
	src.PlayDomain = in.PlayDomain
	src.Enabled = in.Enabled
	src.IncrementalHours = in.IncrementalHours
	src.MaxPages = in.MaxPages
	if err := r.db.Save(src).Error; err != nil {
		return nil, err
	}
	return src, nil
}

// DeleteSource 删除站点配置，并在同一事务里清理其影片与采集记录。
// 跨表只是引用完整性约束，仍归属站点仓储的删除语义。
func (r *sourceRepository) DeleteSource(id uint) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("source_id = ?", id).Delete(&model.Video{}).Error; err != nil {
			return err
		}
		if err := tx.Where("source_id = ?", id).Delete(&model.CrawlJob{}).Error; err != nil {
			return err
		}
		if err := tx.Where("source_id = ?", id).Delete(&model.Category{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Source{}, id).Error
	})
}

// UpdateSourceResult 采集完成后回写站点状态与增量游标。
func (r *sourceRepository) UpdateSourceResult(id uint, mode, status, cursor, errMsg string, at time.Time) error {
	updates := map[string]interface{}{
		"last_mode":    mode,
		"last_status":  status,
		"last_error":   errMsg,
		"last_sync_at": at,
	}
	if cursor != "" {
		updates["last_cursor"] = cursor
	}
	if mode == model.ModeFull && status == model.JobSuccess {
		updates["last_full_sync_at"] = at
	}
	return r.db.Model(&model.Source{}).Where("id = ?", id).Updates(updates).Error
}
