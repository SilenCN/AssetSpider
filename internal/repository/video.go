package repository

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// UpsertStats 是一次影片批量写入的统计。
type UpsertStats struct {
	New       int `json:"new"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
}

// VideoYear 是一个年份及其影片数，供资源库的年份筛选下拉使用。
type VideoYear struct {
	Year  string
	Count int64
}

// VideoQuery 是影片列表的查询条件，避免随筛选维度增加不断拉长参数列表。
type VideoQuery struct {
	SourceID   uint
	CategoryID uint
	TagID      uint   // 按标签过滤（经 video_tags 关联表）
	AreaID     uint   // 按地域过滤（经 video_areas 关联表）
	Year       string // 按年份过滤（vod_year 等值匹配）
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
	// ListVideos 只返回本页影片，不计算总数、也不多取一条：
	// COUNT 需要一次全量扫描，前端按「本页条数 == size」判断是否继续翻页。
	ListVideos(query VideoQuery) ([]model.Video, error)
	// MapVodIDs 把一批 vod_id 解析为库内影片主键，供标签关联回填等场景使用。
	MapVodIDs(sourceID uint, vodIDs []string) (map[string]uint, error)
	// CountVideos 返回某站点已入库的影片总数，供站点表的 video_count 快照回写。
	CountVideos(sourceID uint) (int64, error)
	CountVideosBySource(visible ...[]uint) (map[uint]int64, error)
	CountVideosByCategory(visible ...[]uint) (map[uint]int64, error)
	// ListVideoYears 返回库中出现过的年份及各自的影片数，供年份筛选下拉使用。
	ListVideoYears(sourceID uint, visible ...[]uint) ([]VideoYear, error)
}

// videoRepository 是 Video 实体专属的仓储对象。
type videoRepository struct {
	db *gorm.DB
	// search 表示 trigram 全文索引（videos_fts）是否可用，启动时探测一次即可。
	// bigram 不缓存：历史回灌在后台跑，完成时写 video_bigram_state，查询时动态
	// 读一次主键点查（开销可忽略）就能从 LIKE 切到倒排表，无需重启或重建仓储。
	search bool
}

func NewVideoRepository(db *gorm.DB) VideoRepository {
	return &videoRepository{db: db, search: videoSearchTableExists(db)}
}

// bigram 返回两字倒排表是否已回灌完毕。后台任务完成后状态立即生效。
func (r *videoRepository) bigram() bool { return videoBigramReady(r.db) }

var _ VideoRepository = (*videoRepository)(nil)

// existingVideo 是批量预查询时需要用到的既有影片最小字段集。
// 只取这 6 列：判定新增 / 未变 / 已变、回填首见时间与主键、补齐历史数据都够用，
// 避免把 91 个标准字段全查回来白耗内存与 IO。
type existingVideo struct {
	ID          uint      `gorm:"column:id"`
	VodID       string    `gorm:"column:vod_id"`
	Hash        string    `gorm:"column:hash"`
	CategoryID  uint      `gorm:"column:category_id"`
	VodPlayList string    `gorm:"column:vod_play_list"`
	FirstSeenAt time.Time `gorm:"column:first_seen_at"`
}

// UpsertVideos 按 (source_id, vod_id) 批量去重写入。
// 内容指纹未变化则跳过更新，仅刷新 LastSeenAt。
//
// 性能：整批只发 1 次 SELECT 把已存在行捞进内存，而不再逐条 Find；新增用
// CreateInBatches 一次写多行的 SQL；已变用 ON CONFLICT 一次 upsert 多行；
// 未变按「是否需补列」分组批量 UPDATE。往返次数从 O(3N) 降到 O(N/batch+常数)。
// 整个过程仍在单个事务内，保证原子性；FTS5 的行级触发器对批量语句同样逐行生效。
func (r *videoRepository) UpsertVideos(items []model.Video) (UpsertStats, error) {
	var stats UpsertStats
	if len(items) == 0 {
		return stats, nil
	}

	// 批次内按 (source_id, vod_id) 去重并保留最后一条：与旧逻辑一致（同批重复时
	// 后写覆盖前写，且只有最后一条的统计生效）。唯一键必须带 source_id——不同站点的
	// vod_id 可能完全相同，不能互相覆盖。
	type videoKey struct {
		sourceID uint
		vodID    string
	}
	latest := make(map[videoKey]int, len(items)) // (source_id, vod_id) -> items 下标
	order := make([]videoKey, 0, len(items))
	for i := range items {
		k := videoKey{items[i].SourceID, items[i].VodID}
		if _, ok := latest[k]; !ok {
			order = append(order, k)
		}
		latest[k] = i
	}

	// 全批按站点分组：一次查询 per 站点，用 source_id + vod_id IN 取回全部已存在行。
	sources := make(map[uint][]string, 1)
	for _, k := range order {
		sources[k.sourceID] = append(sources[k.sourceID], k.vodID)
	}

	err := r.db.Transaction(func(tx *gorm.DB) error {
		// ① 一次批量 SELECT：捞出本批所有已存在行（只取必要列）。
		existing := make(map[uint]map[string]existingVideo, len(sources))
		for sid, ids := range sources {
			uniq := make([]string, 0, len(ids))
			seen := make(map[string]bool, len(ids))
			for _, id := range ids {
				if !seen[id] {
					seen[id] = true
					uniq = append(uniq, id)
				}
			}
			var rows []existingVideo
			if err := tx.Model(&model.Video{}).
				Select("id, vod_id, hash, category_id, vod_play_list, first_seen_at").
				Where("source_id = ? AND vod_id IN ?", sid, uniq).
				Scan(&rows).Error; err != nil {
				return err
			}
			m := make(map[string]existingVideo, len(rows))
			for _, row := range rows {
				m[row.VodID] = row
			}
			existing[sid] = m
		}

		now := time.Now()
		toCreate := make([]*model.Video, 0, len(items))
		toUpsert := make([]*model.Video, 0, len(items)) // hash 变：ON CONFLICT 全字段更新
		toTouch := make([]*model.Video, 0, len(items))  // hash 同：仅刷 last_seen_at / 补列
		touchIDs := make([]uint, 0, len(items))

		// ② 内存分组（只遍历去重后的最后一条，按首次出现顺序保证稳定）。
		for _, k := range order {
			v := &items[latest[k]]
			ex, found := existing[k.sourceID][k.vodID]
			switch {
			case !found:
				v.FirstSeenAt, v.LastSeenAt = now, now
				toCreate = append(toCreate, v)
				stats.New++
			case ex.Hash == v.Hash:
				stats.Unchanged++
				toTouch = append(toTouch, v)
				touchIDs = append(touchIDs, ex.ID)
			default:
				// 回填既有主键与首见时间：ON CONFLICT 更新时不覆盖这两列。
				v.ID = ex.ID
				v.FirstSeenAt, v.LastSeenAt = ex.FirstSeenAt, now
				toUpsert = append(toUpsert, v)
				stats.Updated++
			}
		}

		const batchSize = 200

		// ③ 批量新增：GORM CreateInBatches 会回填每行自增 ID 并维护 CreatedAt。
		if len(toCreate) > 0 {
			if err := tx.CreateInBatches(toCreate, batchSize).Error; err != nil {
				return fmt.Errorf("批量新增影片: %w", err)
			}
		}

		// ④ 批量更新（内容变化）：按唯一键冲突时更新业务列，不碰 id / created_at /
		// first_seen_at（first_seen_at 已回填成库内原值，也一并排除更稳妥）。
		if len(toUpsert) > 0 {
			cols := make([]string, 0, len(model.VodFields)+3)
			for _, f := range model.VodFields {
				cols = append(cols, f.Column)
			}
			cols = append(cols, "hash", "extra", "category_id", "last_seen_at")
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "source_id"}, {Name: "vod_id"}},
				DoUpdates: clause.AssignmentColumns(cols),
			}).CreateInBatches(toUpsert, batchSize).Error; err != nil {
				return fmt.Errorf("批量更新影片: %w", err)
			}
		}

		// ⑤ 未变批次：先按 id 批量刷 last_seen_at；再把需要补列（历史数据缺
		// category_id / vod_play_list）的行分组批量 UPDATE，避免逐条语句。
		if len(toTouch) > 0 {
			if err := tx.Model(&model.Video{}).Where("id IN ?", touchIDs).
				Update("last_seen_at", now).Error; err != nil {
				return err
			}
			// 需要补 category_id 的行。
			catByID := make(map[uint]uint)
			playByID := make(map[uint]string)
			for _, v := range toTouch {
				ex := existing[v.SourceID][v.VodID]
				if ex.CategoryID != v.CategoryID {
					catByID[ex.ID] = v.CategoryID
				}
				if ex.VodPlayList == "" && v.VodPlayList != "" {
					playByID[ex.ID] = v.VodPlayList
				}
			}
			for id, cat := range catByID {
				if err := tx.Model(&model.Video{}).Where("id = ?", id).
					Update("category_id", cat).Error; err != nil {
					return err
				}
			}
			for id, play := range playByID {
				if err := tx.Model(&model.Video{}).Where("id = ?", id).
					Update("vod_play_list", play).Error; err != nil {
					return err
				}
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
	case "id_desc":
		// 默认排序：按库内视频 id 倒序（即最新入库的排在前面）。
		return "id DESC"
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
//
// 不再计算总数：COUNT 需要对命中结果做一次全量扫描，在关键词检索 / 大表上往往比
// 取本页数据还贵。也不额外多取一条、不返回 hasMore——前端按「本页条数 >= size」
// 认为还有下一页，到底时最多多发一次拿到空页的请求。
func (r *videoRepository) ListVideos(query VideoQuery) ([]model.Video, error) {
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
			return nil, err
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
	if year := strings.TrimSpace(query.Year); year != "" {
		// 年份是影片上的单值列，直接等值匹配（走 idx_videos_vod_year）。
		q = q.Where("vod_year = ?", year)
	}
	if query.Keyword != "" {
		q = applyVideoKeyword(q, query.Keyword, videoSearchCaps{fts: r.search, bigram: r.bigram()})
	}
	// 一次查询拿本页数据即可，不做 COUNT、不多取一条。
	var items []model.Video
	if err := q.Order(videoOrder(query.Sort)).
		Offset((query.Page - 1) * query.Size).Limit(query.Size).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// ListVideoYears 返回库中出现过的年份及各自的影片数，按年份倒序。
//
// 年份不像标签 / 地域那样拆成独立实体：它是影片上的单值属性，既没有跨站共享的名称，
// 也不需要多对多关系，直接对 vod_year 做 GROUP BY 即可（走 idx_videos_vod_year）。
// 空年份（接口没给）不进入下拉，否则会出现一个没有意义的空白选项。
// sourceID 为 0 时统计全部可见站点。
func (r *videoRepository) ListVideoYears(sourceID uint, visible ...[]uint) ([]VideoYear, error) {
	type row struct {
		Year  string
		Count int64
	}
	var rows []row
	q := applyVisibility(r.db.Model(&model.Video{}), "source_id", firstVisible(visible...))
	if sourceID > 0 {
		q = q.Where("source_id = ?", sourceID)
	}
	if err := q.Select("vod_year AS year, count(*) AS count").
		Where("TRIM(vod_year) <> ''").
		Group("vod_year").
		Order("vod_year DESC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]VideoYear, 0, len(rows))
	for _, row := range rows {
		out = append(out, VideoYear{Year: row.Year, Count: row.Count})
	}
	return out, nil
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
