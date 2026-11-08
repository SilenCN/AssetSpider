package store

import (
	"fmt"
	"reflect"
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
	"gorm.io/gorm"
)

// ---------- 站点配置 ----------

// ListSources 返回全部站点配置（管理员视角），按 ID 正序。
// 需要按用户权限过滤时改用 ListSourcesVisible。
func (s *Store) ListSources() ([]model.Source, error) {
	return s.ListSourcesVisible(nil)
}

// ListEnabledSources 返回所有启用站点。
func (s *Store) ListEnabledSources() ([]model.Source, error) {
	var items []model.Source
	if err := s.db.Where("enabled = ?", true).Order("id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// GetSource 按 ID 读取站点配置。
func (s *Store) GetSource(id uint) (*model.Source, error) {
	var src model.Source
	if err := s.db.First(&src, id).Error; err != nil {
		return nil, err
	}
	return &src, nil
}

// CreateSource 新增站点配置。
func (s *Store) CreateSource(src *model.Source) error {
	return s.db.Create(src).Error
}

// UpdateSource 按 ID 全量更新站点配置的可编辑字段。
// 采集状态字段（Last*）不由前端修改，予以保留。
func (s *Store) UpdateSource(id uint, in *model.Source) (*model.Source, error) {
	src, err := s.GetSource(id)
	if err != nil {
		return nil, err
	}
	src.Name = in.Name
	src.APIBase = in.APIBase
	src.Format = in.Format
	src.ExtraParams = in.ExtraParams
	src.Enabled = in.Enabled
	src.IncrementalHours = in.IncrementalHours
	src.MaxPages = in.MaxPages
	if err := s.db.Save(src).Error; err != nil {
		return nil, err
	}
	return src, nil
}

// DeleteSource 删除站点配置，并级联清理其影片与采集记录。
func (s *Store) DeleteSource(id uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("source_id = ?", id).Delete(&model.Video{}).Error; err != nil {
			return err
		}
		if err := tx.Where("source_id = ?", id).Delete(&model.CrawlJob{}).Error; err != nil {
			return err
		}
		// 站点删除后同步清理所有用户对该站点的可见性授权。
		if err := tx.Where("source_id = ?", id).Delete(&model.UserSource{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Source{}, id).Error
	})
}

// UpdateSourceResult 采集完成后回写站点状态与增量游标。
func (s *Store) UpdateSourceResult(id uint, mode, status, cursor, errMsg string, at time.Time) error {
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
	return s.db.Model(&model.Source{}).Where("id = ?", id).Updates(updates).Error
}

// ---------- 影片数据 ----------

// UpsertStats 是一次批量写入的统计。
type UpsertStats struct {
	New       int `json:"new"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
}

// UpsertVideos 按 (source_id, vod_id) 批量去重写入。
// 内容指纹未变化则跳过更新，仅刷新 LastSeenAt。
func (s *Store) UpsertVideos(items []model.Video) (UpsertStats, error) {
	var stats UpsertStats
	err := s.db.Transaction(func(tx *gorm.DB) error {
		for i := range items {
			v := &items[i]
			var existing model.Video
			// 用 Find 而非 First，避免 GORM 为"记录不存在"打印 warn 日志。
			found := tx.Where("source_id = ? AND vod_id = ?", v.SourceID, v.VodID).
				Limit(1).Find(&existing)
			if found.Error != nil {
				return found.Error
			}
			switch {
			case found.RowsAffected == 0:
				now := time.Now()
				v.FirstSeenAt, v.LastSeenAt = now, now
				if err := tx.Create(v).Error; err != nil {
					return fmt.Errorf("新增影片 %d/%s: %w", v.SourceID, v.VodID, err)
				}
				stats.New++
			case existing.Hash == v.Hash:
				stats.Unchanged++
				if err := tx.Model(&existing).Update("last_seen_at", time.Now()).Error; err != nil {
					return err
				}
			default:
				if err := tx.Model(&existing).Updates(videoUpdateMap(*v)).Error; err != nil {
					return err
				}
				stats.Updated++
			}
		}
		return nil
	})
	return stats, err
}

// videoUpdateMap 生成覆盖全部标准字段列的更新 map。
// 用 map 而非 struct，确保接口字段被清空（零值）时也能正确写回；
// 列名与取值都由 model.VodFields 驱动，避免手写几十列造成遗漏。
func videoUpdateMap(v model.Video) map[string]interface{} {
	rv := reflect.ValueOf(v)
	m := make(map[string]interface{}, len(model.VodFields)+4)
	for _, f := range model.VodFields {
		fv := rv.FieldByName(f.Go)
		if fv.IsValid() && fv.Kind() == reflect.String {
			m[f.Column] = fv.String()
		}
	}
	m["hash"] = v.Hash
	m["raw"] = v.Raw
	m["extra"] = v.Extra
	m["last_seen_at"] = time.Now()
	return m
}

// GetVideo 按主键读取单条影片的完整记录（含 Raw 原始 JSON 与 Extra 扩展字段）。
func (s *Store) GetVideo(id uint) (*model.Video, error) {
	var v model.Video
	if err := s.db.First(&v, id).Error; err != nil {
		return nil, err
	}
	return &v, nil
}

// ListVideos 分页查询影片（管理员视角），支持按站点过滤与多字段模糊搜索。
// 需要按用户权限过滤时改用 ListVideosVisible。
func (s *Store) ListVideos(sourceID uint, keyword string, page, size int) ([]model.Video, int64, error) {
	return s.ListVideosVisible(sourceID, keyword, page, size, nil)
}

// CountVideosBySource 返回每个站点的影片数量。
func (s *Store) CountVideosBySource() (map[uint]int64, error) {
	type row struct {
		SourceID uint
		Cnt      int64
	}
	var rows []row
	if err := s.db.Model(&model.Video{}).
		Select("source_id, count(*) as cnt").Group("source_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uint]int64, len(rows))
	for _, r := range rows {
		out[r.SourceID] = r.Cnt
	}
	return out, nil
}

// ---------- 采集任务 ----------

// CreateJob 创建一条 running 状态的任务记录。
func (s *Store) CreateJob(job *model.CrawlJob) error {
	return s.db.Create(job).Error
}

// FinishJob 更新任务终态。
func (s *Store) FinishJob(job *model.CrawlJob, status, message string) error {
	now := time.Now()
	return s.db.Model(job).Updates(map[string]interface{}{
		"status":      status,
		"message":     message,
		"fetched":     job.Fetched,
		"new":         job.New,
		"updated":     job.Updated,
		"unchanged":   job.Unchanged,
		"pages":       job.Pages,
		"total":       job.Total,
		"finished_at": now,
	}).Error
}

// ListJobs 返回最近的采集记录（管理员视角）。
// 需要按用户权限过滤时改用 ListJobsVisible。
func (s *Store) ListJobs(sourceID uint, limit int) ([]model.CrawlJob, error) {
	return s.ListJobsVisible(sourceID, limit, nil)
}
