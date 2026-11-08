package repository

import (
	"fmt"
	"reflect"
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
	"gorm.io/gorm"
)

// UpsertStats 是一次影片批量写入的统计。
type UpsertStats struct {
	New       int `json:"new"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
}

// VideoQuery 是影片列表的查询条件，避免随筛选维度增加不断拉长参数列表。
type VideoQuery struct {
	SourceID   uint
	CategoryID uint
	Keyword    string
	Page       int
	Size       int
}

// NewVideoQuery 构造不带分类条件的查询，供不需要分类过滤的调用方使用。
func NewVideoQuery(sourceID uint, keyword string, page, size int) VideoQuery {
	return VideoQuery{SourceID: sourceID, Keyword: keyword, Page: page, Size: size}
}

// VideoRepository 是 Video 实体的数据访问契约。
type VideoRepository interface {
	UpsertVideos(items []model.Video) (UpsertStats, error)
	GetVideo(id uint) (*model.Video, error)
	ListVideos(query VideoQuery) ([]model.Video, int64, error)
	CountVideosBySource() (map[uint]int64, error)
	CountVideosByCategory() (map[uint]int64, error)
}

// videoRepository 是 Video 实体专属的仓储对象。
type videoRepository struct {
	db *gorm.DB
}

func NewVideoRepository(db *gorm.DB) VideoRepository {
	return &videoRepository{db: db}
}

var _ VideoRepository = (*videoRepository)(nil)

// UpsertVideos 按 (source_id, vod_id) 批量去重写入。
// 内容指纹未变化则跳过更新，仅刷新 LastSeenAt。
func (r *videoRepository) UpsertVideos(items []model.Video) (UpsertStats, error) {
	var stats UpsertStats
	err := r.db.Transaction(func(tx *gorm.DB) error {
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
				updates := map[string]interface{}{"last_seen_at": time.Now()}
				// 全字段未变时也要补齐分类关联，让改造前入库的历史数据在全量采集后自动归类。
				if existing.CategoryID != v.CategoryID {
					updates["category_id"] = v.CategoryID
				}
				if err := tx.Model(&existing).Updates(updates).Error; err != nil {
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
	m := make(map[string]interface{}, len(model.VodFields)+5)
	for _, f := range model.VodFields {
		fv := rv.FieldByName(f.Go)
		if fv.IsValid() && fv.Kind() == reflect.String {
			m[f.Column] = fv.String()
		}
	}
	m["hash"] = v.Hash
	m["raw"] = v.Raw
	m["extra"] = v.Extra
	m["category_id"] = v.CategoryID
	m["last_seen_at"] = time.Now()
	return m
}

// GetVideo 按主键读取单条影片的完整记录（含 Raw 原始 JSON 与 Extra 扩展字段）。
func (r *videoRepository) GetVideo(id uint) (*model.Video, error) {
	var v model.Video
	if err := r.db.First(&v, id).Error; err != nil {
		return nil, notFound(err)
	}
	return &v, nil
}

// ListVideos 分页查询影片，支持按站点、分类过滤与多字段模糊搜索。
func (r *videoRepository) ListVideos(query VideoQuery) ([]model.Video, int64, error) {
	if query.Page < 1 {
		query.Page = 1
	}
	if query.Size < 1 || query.Size > 200 {
		query.Size = 20
	}
	q := r.db.Model(&model.Video{})
	if query.SourceID > 0 {
		q = q.Where("source_id = ?", query.SourceID)
	}
	if query.CategoryID > 0 {
		q = q.Where("category_id = ?", query.CategoryID)
	}
	if query.Keyword != "" {
		kw := "%" + query.Keyword + "%"
		q = q.Where(
			"vod_name LIKE ? OR vod_en LIKE ? OR vod_actor LIKE ? OR vod_director LIKE ? OR vod_class LIKE ?",
			kw, kw, kw, kw, kw,
		)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var items []model.Video
	if err := q.Order("id DESC").Offset((query.Page - 1) * query.Size).Limit(query.Size).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// CountVideosBySource 返回每个站点的影片数量。
func (r *videoRepository) CountVideosBySource() (map[uint]int64, error) {
	type row struct {
		SourceID uint
		Cnt      int64
	}
	var rows []row
	if err := r.db.Model(&model.Video{}).
		Select("source_id, count(*) as cnt").Group("source_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uint]int64, len(rows))
	for _, r := range rows {
		out[r.SourceID] = r.Cnt
	}
	return out, nil
}

// CountVideosByCategory 返回每个分类下的影片数量（未归类的不计入）。
func (r *videoRepository) CountVideosByCategory() (map[uint]int64, error) {
	type row struct {
		CategoryID uint
		Cnt        int64
	}
	var rows []row
	if err := r.db.Model(&model.Video{}).
		Select("category_id, count(*) as cnt").
		Where("category_id > 0").Group("category_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uint]int64, len(rows))
	for _, row := range rows {
		out[row.CategoryID] = row.Cnt
	}
	return out, nil
}
