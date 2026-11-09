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
	TagID      uint // 按标签过滤（经 video_tags 关联表）
	AreaID     uint // 按地域过滤（经 video_areas 关联表）
	Keyword    string
	Sort       string // 排序标识，见 videoOrder；空则按 id 倒序
	Page       int
	Size       int
	// VisibleSourceIDs 是可选的站点白名单：nil 表示不过滤（管理员），
	// 长度为 0 的非 nil 切片表示不返回任何影片。
	VisibleSourceIDs []uint
}

// NewVideoQuery 构造不带分类条件的查询，供不需要分类过滤的调用方使用。
func NewVideoQuery(sourceID uint, keyword string, page, size int) VideoQuery {
	return VideoQuery{SourceID: sourceID, Keyword: keyword, Page: page, Size: size}
}

// VideoRepository 是 Video 实体的数据访问契约。
type VideoRepository interface {
	UpsertVideos(items []model.Video) (UpsertStats, error)
	// 读取类方法都接受可选的站点白名单，语义见 visibility.go。
	GetVideo(id uint, visible ...[]uint) (*model.Video, error)
	ListVideos(query VideoQuery) ([]model.Video, int64, error)
	// MapVodIDs 把一批 vod_id 解析为库内影片主键，供标签关联回填等场景使用。
	MapVodIDs(sourceID uint, vodIDs []string) (map[string]uint, error)
	// CountVideos 返回某站点已入库的影片总数，供站点表的 video_count 快照回写。
	CountVideos(sourceID uint) (int64, error)
	CountVideosBySource(visible ...[]uint) (map[uint]int64, error)
	CountVideosByCategory(visible ...[]uint) (map[uint]int64, error)
}

// videoRepository 是 Video 实体专属的仓储对象。
type videoRepository struct {
	db *gorm.DB
	// search 表示全文索引（videos_fts）是否可用；不可用时关键词检索退回 LIKE。
	search bool
}

func NewVideoRepository(db *gorm.DB) VideoRepository {
	return &videoRepository{db: db, search: videoSearchTableExists(db)}
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
				// 解析后的播放组（vod_play_list）同理：对历史数据或上次未解析成功的记录，
				// 即使内容指纹未变，也要把本次解析得到的播放组回写，避免"解析了却没入库"。
				if existing.VodPlayList == "" && v.VodPlayList != "" {
					updates["vod_play_list"] = v.VodPlayList
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
	m := make(map[string]interface{}, len(model.VodFields)+4)
	for _, f := range model.VodFields {
		fv := rv.FieldByName(f.Go)
		if fv.IsValid() && fv.Kind() == reflect.String {
			m[f.Column] = fv.String()
		}
	}
	m["hash"] = v.Hash
	m["extra"] = v.Extra
	m["category_id"] = v.CategoryID
	m["last_seen_at"] = time.Now()
	return m
}

// GetVideo 按主键读取单条影片的完整记录（含 Extra 扩展字段）。
// 影片所属站点不在可见集合内时按「不存在」处理，避免越权读取。
func (r *videoRepository) GetVideo(id uint, visible ...[]uint) (*model.Video, error) {
	var v model.Video
	q := applyVisibility(r.db.Model(&model.Video{}), "source_id", firstVisible(visible...))
	if err := q.First(&v, id).Error; err != nil {
		return nil, notFound(err)
	}
	return &v, nil
}

// MapVodIDs 把一批 vod_id 解析为库内影片主键。
// 仅查指定站点，返回 vod_id → 主键 的映射（查不到的 vod_id 不出现在结果里）。
func (r *videoRepository) MapVodIDs(sourceID uint, vodIDs []string) (map[string]uint, error) {
	out := make(map[string]uint, len(vodIDs))
	if len(vodIDs) == 0 {
		return out, nil
	}
	type kv struct {
		VodID string
		ID    uint
	}
	var rows []kv
	if err := r.db.Model(&model.Video{}).
		Where("source_id = ? AND vod_id IN ?", sourceID, vodIDs).
		Select("vod_id, id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.VodID] = row.ID
	}
	return out, nil
}

// videoOrder 把排序标识映射为白名单内的 ORDER BY 片段。
// 只允许固定几组排序，避免把外部输入直接拼进 SQL。
func videoOrder(sort string) string {
	switch sort {
	case "time_desc":
		return "vod_time DESC, id DESC"
	case "time_asc":
		return "vod_time ASC, id ASC"
	case "score_desc":
		// vod_score 是字符串列，空值经 NULLIF 转 NULL 后排在末尾（SQLite 中 NULL 在 DESC 时最后）。
		return "CAST(NULLIF(TRIM(vod_score), '') AS REAL) DESC, vod_time DESC, id DESC"
	case "score_asc":
		return "CAST(NULLIF(TRIM(vod_score), '') AS REAL) ASC, vod_time DESC, id DESC"
	default:
		return "id DESC"
	}
}

// ListVideos 分页查询影片，支持按站点、分类、地域过滤、多字段模糊搜索与排序。
func (r *videoRepository) ListVideos(query VideoQuery) ([]model.Video, int64, error) {
	if query.Page < 1 {
		query.Page = 1
	}
	if query.Size < 1 || query.Size > 200 {
		query.Size = 20
	}
	q := applyVisibility(r.db.Model(&model.Video{}), "source_id", query.VisibleSourceIDs)
	if query.SourceID > 0 {
		q = q.Where("source_id = ?", query.SourceID)
	}
	if query.CategoryID > 0 {
		// 二级分类支持：按分类筛选时，父分类一并命中其下所有子分类的影片。
		var ids []uint
		if err := r.db.Model(&model.Category{}).
			Where("id = ? OR parent_id = ?", query.CategoryID, query.CategoryID).
			Pluck("id", &ids).Error; err != nil {
			return nil, 0, err
		}
		if len(ids) == 0 {
			ids = []uint{query.CategoryID}
		}
		q = q.Where("category_id IN ?", ids)
	}
	if query.TagID > 0 {
		// 标签与影片是多对多：命中该标签的影片即通过 video_tags 内连接筛选。
		q = q.Joins("JOIN video_tags ON video_tags.video_id = videos.id").
			Where("video_tags.tag_id = ?", query.TagID)
	}
	if query.AreaID > 0 {
		// 地域与影片同样是多对多：命中该地域的影片经 video_areas 内连接筛选。
		q = q.Joins("JOIN video_areas ON video_areas.video_id = videos.id").
			Where("video_areas.area_id = ?", query.AreaID)
	}
	if query.Keyword != "" {
		q = applyVideoKeyword(q, query.Keyword, r.search)
	}
	// 先取本页数据，再决定要不要算总数：
	// 第一页且没取满时，本页条数就是总数，可省掉一次全量扫描（关键词检索时尤其明显）。
	//
	// 分页查询必须在独立的 Session 上执行：GORM 的链式方法会复用同一个 Statement，
	// 直接在 q 上 .Order().Offset().Limit() 会把分页条件带到后面的 Count 上。
	var items []model.Video
	page := q.Session(&gorm.Session{})
	if err := page.Order(videoOrder(query.Sort)).
		Offset((query.Page - 1) * query.Size).Limit(query.Size).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	if query.Page == 1 && len(items) < query.Size {
		return items, int64(len(items)), nil
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// CountVideos 返回某站点已入库的影片总数。
//
// 站点表的 video_count 列就是它的快照：采集运行中定期、结束时各回写一次，
// 避免每次列出站点都要对 videos 表做一次聚合。
func (r *videoRepository) CountVideos(sourceID uint) (int64, error) {
	var n int64
	if err := r.db.Model(&model.Video{}).Where("source_id = ?", sourceID).Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// CountVideosBySource 返回每个站点的影片数量。
func (r *videoRepository) CountVideosBySource(visible ...[]uint) (map[uint]int64, error) {
	type row struct {
		SourceID uint
		Cnt      int64
	}
	var rows []row
	q := applyVisibility(r.db.Model(&model.Video{}), "source_id", firstVisible(visible...))
	if err := q.Select("source_id, count(*) as cnt").Group("source_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uint]int64, len(rows))
	for _, r := range rows {
		out[r.SourceID] = r.Cnt
	}
	return out, nil
}

// CountVideosByCategory 返回每个分类下的影片数量（未归类的不计入）。
func (r *videoRepository) CountVideosByCategory(visible ...[]uint) (map[uint]int64, error) {
	type row struct {
		CategoryID uint
		Cnt        int64
	}
	var rows []row
	q := applyVisibility(r.db.Model(&model.Video{}), "source_id", firstVisible(visible...))
	if err := q.Select("category_id, count(*) as cnt").
		Where("category_id > 0").Group("category_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uint]int64, len(rows))
	for _, row := range rows {
		out[row.CategoryID] = row.Cnt
	}
	return out, nil
}
