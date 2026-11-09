package repository

import (
	"errors"
	"strings"
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
	"gorm.io/gorm"
)

// SourceRepository 是 Source 实体的数据访问契约。
type SourceRepository interface {
	// visible 为可选站点白名单：不传 / nil 表示不过滤，空切片表示不返回任何站点。
	ListSources(visible ...[]uint) ([]model.Source, error)
	ListEnabledSources(visible ...[]uint) ([]model.Source, error)
	GetSource(id uint, visible ...[]uint) (*model.Source, error)
	// GetSourceByName 按名称（忽略大小写与首尾空白）查找站点，找不到返回 ErrNotFound。
	GetSourceByName(name string) (*model.Source, error)
	CreateSource(src *model.Source) error
	UpdateSource(id uint, in *model.Source) (*model.Source, error)
	DeleteSource(id uint) error
	UpdateSourceResult(id uint, mode, status, cursor, errMsg string, at time.Time) error
	// UpdateVideoCount 回写站点已入库的影片总数（采集运行中定期 + 任务结束时调用）。
	UpdateVideoCount(id uint, count int64) error
	// RefreshVideoCounts 按 videos 表重算全部站点的影片总数，用于启动时校准历史数据。
	RefreshVideoCounts() error
}

// sourceRepository 是 Source 实体专属的仓储对象。
type sourceRepository struct {
	db *gorm.DB
}

func NewSourceRepository(db *gorm.DB) SourceRepository {
	return &sourceRepository{db: db}
}

var _ SourceRepository = (*sourceRepository)(nil)

// ListSources 返回站点配置，按 ID 正序；可限定在可见站点集合内。
func (r *sourceRepository) ListSources(visible ...[]uint) ([]model.Source, error) {
	var items []model.Source
	q := applyVisibility(r.db.Model(&model.Source{}), "id", firstVisible(visible...))
	if err := q.Order("id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// ListEnabledSources 返回启用站点；可限定在可见站点集合内。
func (r *sourceRepository) ListEnabledSources(visible ...[]uint) ([]model.Source, error) {
	var items []model.Source
	q := applyVisibility(r.db.Model(&model.Source{}).Where("enabled = ?", true), "id", firstVisible(visible...))
	if err := q.Order("id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// GetSource 按 ID 读取站点配置；站点不在可见集合内时按「不存在」处理。
func (r *sourceRepository) GetSource(id uint, visible ...[]uint) (*model.Source, error) {
	var src model.Source
	q := applyVisibility(r.db.Model(&model.Source{}), "id", firstVisible(visible...))
	if err := q.First(&src, id).Error; err != nil {
		return nil, notFound(err)
	}
	return &src, nil
}

// CreateSource 新增站点配置。
func (r *sourceRepository) CreateSource(src *model.Source) error {
	return r.db.Create(src).Error
}

// GetSourceByName 按名称（忽略大小写与首尾空白）查找站点；找不到返回 ErrNotFound。
// 站点名称全系统唯一，因此这是去重与「重名即更新」的唯一判据来源。
func (r *sourceRepository) GetSourceByName(name string) (*model.Source, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return nil, ErrNotFound
	}
	var src model.Source
	if err := r.db.Model(&model.Source{}).Where("lower(name) = ?", key).First(&src).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &src, nil
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
	src.PicAPI = in.PicAPI
	src.PicDomain = in.PicDomain
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
		// video_tags 没有 source_id，需先按本源影片主键清理连接表，且必须在删除影片之前完成，
		// 否则影片删掉后就再也定位不到这些关联。
		var videoIDs []uint
		if err := tx.Model(&model.Video{}).Where("source_id = ?", id).
			Pluck("id", &videoIDs).Error; err != nil {
			return err
		}
		if len(videoIDs) > 0 {
			if err := tx.Where("video_id IN ?", videoIDs).Delete(&model.VideoTag{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("source_id = ?", id).Delete(&model.Video{}).Error; err != nil {
			return err
		}
		if err := tx.Where("source_id = ?", id).Delete(&model.CrawlJob{}).Error; err != nil {
			return err
		}
		if err := tx.Where("source_id = ?", id).Delete(&model.Category{}).Error; err != nil {
			return err
		}
		if err := tx.Where("source_id = ?", id).Delete(&model.Tag{}).Error; err != nil {
			return err
		}
		// 站点删除后同步撤掉所有用户对该站点的授权，避免 user_sources 悬挂。
		if err := tx.Where("source_id = ?", id).Delete(&model.UserSource{}).Error; err != nil {
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

// UpdateVideoCount 回写站点已入库的影片总数。
//
// 用 UpdateColumn 而不是 Updates：统计值是「采集运行期间每分钟都会变」的附属数据，
// 不应把站点的 updated_at 也一起刷新，否则列表上会误显示「刚修改过配置」。
func (r *sourceRepository) UpdateVideoCount(id uint, count int64) error {
	if count < 0 {
		count = 0
	}
	return r.db.Model(&model.Source{}).Where("id = ?", id).
		UpdateColumn("video_count", count).Error
}

// RefreshVideoCounts 按 videos 表重算全部站点的影片总数。
//
// 一条语句完成全量校准：带 video_count 列的版本首次启动时，历史站点该列是默认值 0，
// 需要与真实影片数对齐。videos 上有 (source_id, vod_id) 联合唯一索引，
// 每个站点的 COUNT(*) 都走索引，不随影片总量放大。
func (r *sourceRepository) RefreshVideoCounts() error {
	return r.db.Exec(`
		UPDATE sources
		SET video_count = (SELECT COUNT(*) FROM videos WHERE videos.source_id = sources.id)
	`).Error
}
