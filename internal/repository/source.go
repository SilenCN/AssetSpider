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
	// ClearSourceData 清空站点名下的全部数据（影片、分类、标签、采集记录）并复位
	// 采集状态与影片数快照，站点配置本身保持不变。
	ClearSourceData(id uint) (ClearStats, error)
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
			if err := tx.Where("video_id IN ?", videoIDs).Delete(&model.VideoArea{}).Error; err != nil {
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
		if err := tx.Where("source_id = ?", id).Delete(&model.Area{}).Error; err != nil {
			return err
		}
		// 站点删除后同步撤掉所有用户对该站点的授权，避免 user_sources 悬挂。
		if err := tx.Where("source_id = ?", id).Delete(&model.UserSource{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Source{}, id).Error
	})
}

// ClearStats 是一次「清空站点数据」实际删除的各表条数，用于向前端回报清空规模。
type ClearStats struct {
	Videos     int64 `json:"videos"`
	Categories int64 `json:"categories"`
	Tags       int64 `json:"tags"`
	Areas      int64 `json:"areas"`
	Jobs       int64 `json:"jobs"`
}

// ClearSourceData 清空站点数据但保留站点配置。
//
// 同时复位采集状态（增量游标、上次采集结果、影片数快照）：影片都删了，
// 游标若还停在上次采到的最大 vod_time，下次增量采集会认为「没有更新」而
// 永远补不回来；复位后下一次增量会从零开始重新拉取。
func (r *sourceRepository) ClearSourceData(id uint) (ClearStats, error) {
	var stats ClearStats
	err := r.db.Transaction(func(tx *gorm.DB) error {
		var src model.Source
		if err := tx.First(&src, id).Error; err != nil {
			return notFound(err)
		}
		// 统计放在删除之前，回报的是「本次清掉了多少」。
		if err := tx.Model(&model.Video{}).Where("source_id = ?", id).Count(&stats.Videos).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Category{}).Where("source_id = ?", id).Count(&stats.Categories).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Tag{}).Where("source_id = ?", id).Count(&stats.Tags).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Area{}).Where("source_id = ?", id).Count(&stats.Areas).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.CrawlJob{}).Where("source_id = ?", id).Count(&stats.Jobs).Error; err != nil {
			return err
		}

		// video_tags / video_areas 没有 source_id，需先按本源影片主键清理连接表，
		// 且必须在删除影片之前完成。
		var videoIDs []uint
		if err := tx.Model(&model.Video{}).Where("source_id = ?", id).Pluck("id", &videoIDs).Error; err != nil {
			return err
		}
		if len(videoIDs) > 0 {
			if err := tx.Where("video_id IN ?", videoIDs).Delete(&model.VideoTag{}).Error; err != nil {
				return err
			}
			if err := tx.Where("video_id IN ?", videoIDs).Delete(&model.VideoArea{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("source_id = ?", id).Delete(&model.Video{}).Error; err != nil {
			return err
		}
		if err := tx.Where("source_id = ?", id).Delete(&model.Category{}).Error; err != nil {
			return err
		}
		if err := tx.Where("source_id = ?", id).Delete(&model.Tag{}).Error; err != nil {
			return err
		}
		if err := tx.Where("source_id = ?", id).Delete(&model.Area{}).Error; err != nil {
			return err
		}
		if err := tx.Where("source_id = ?", id).Delete(&model.CrawlJob{}).Error; err != nil {
			return err
		}

		// 复位运行状态：用 UpdateColumns 而不是 Updates，避免把配置的 updated_at 也刷新
		// （站点配置本身没有变化）。指向时间字段的 nil 会被写成 NULL。
		return tx.Model(&model.Source{}).Where("id = ?", id).UpdateColumns(map[string]interface{}{
			"last_mode":         "",
			"last_status":       "",
			"last_error":        "",
			"last_cursor":       "",
			"last_sync_at":      nil,
			"last_full_sync_at": nil,
			"video_count":       0,
		}).Error
	})
	if err != nil {
		return ClearStats{}, err
	}
	return stats, nil
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
