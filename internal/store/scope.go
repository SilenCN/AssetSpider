package store

import (
	"github.com/SilenCN/AssetSpider/internal/model"
	"gorm.io/gorm"
)

// applyVisibility 把「可见站点 ID 集合」应用到查询上。
//
// visibleIDs == nil 表示管理员：不过滤，可见全部站点；
// visibleIDs != nil（包括长度为 0 的空切片）表示普通用户：只允许集合内的站点，
// 空集合等价于不允许任何数据（不会退化成"查全部"）。
func applyVisibility(q *gorm.DB, column string, visibleIDs []uint) *gorm.DB {
	if visibleIDs == nil {
		return q
	}
	if len(visibleIDs) == 0 {
		return q.Where("1 = 0")
	}
	return q.Where(column+" IN ?", visibleIDs)
}

// ListSourcesVisible 返回当前用户可见的站点配置。
func (s *Store) ListSourcesVisible(visibleIDs []uint) ([]model.Source, error) {
	var items []model.Source
	q := applyVisibility(s.db.Model(&model.Source{}), "id", visibleIDs)
	if err := q.Order("id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// ListVideosVisible 分页查询当前用户可见的影片。
// 指定 sourceID 时，若该站点不在可见集合内，查询自然返回空。
func (s *Store) ListVideosVisible(sourceID uint, keyword string, page, size int, visibleIDs []uint) ([]model.Video, int64, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 20
	}
	q := applyVisibility(s.db.Model(&model.Video{}), "source_id", visibleIDs)
	if sourceID > 0 {
		q = q.Where("source_id = ?", sourceID)
	}
	if keyword != "" {
		kw := "%" + keyword + "%"
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
	if err := q.Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// ListJobsVisible 返回当前用户可见站点的采集记录。
func (s *Store) ListJobsVisible(sourceID uint, limit int, visibleIDs []uint) ([]model.CrawlJob, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	q := applyVisibility(s.db.Model(&model.CrawlJob{}), "source_id", visibleIDs)
	if sourceID > 0 {
		q = q.Where("source_id = ?", sourceID)
	}
	var items []model.CrawlJob
	if err := q.Order("id DESC").Limit(limit).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// InVisibleSources 判断某站点是否在可见集合内（管理员恒为 true）。
func InVisibleSources(visibleIDs []uint, sourceID uint) bool {
	if visibleIDs == nil {
		return true
	}
	for _, id := range visibleIDs {
		if id == sourceID {
			return true
		}
	}
	return false
}
