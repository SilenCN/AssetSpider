package repository

import (
	"strings"

	"github.com/SilenCN/AssetSpider/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AreaRepository 是 Area 实体的数据访问契约。
//
// 与 TagRepository 完全同构：地域由采集过程按影片自带的 vod_area 拆分维护，
// 不提供手工增删改。
type AreaRepository interface {
	// EnsureForVideos 解析一批影片的地域（取自 vod_area），按源去重创建，
	// 返回 vod_id → area 主键列表 的映射，供 service 在影片入库后回填 video_areas。
	// 可选的 CrawlCache 是采集任务级类目缓存：传入后命中项不再查库、也不再写入。
	EnsureForVideos(sourceID uint, videos []model.Video, cache ...*CrawlCache) (map[string][]uint, error)
	// LinkVideos 按 (vod_id → 影片主键) 与 (vod_id → area 主键) 替换本批次影片的地域关联。
	LinkVideos(idByVod map[string]uint, areasByVod map[string][]uint) error
	// List 返回地域，按站点与名称排序；sourceID 为 0 时返回全部站点的地域。
	List(sourceID uint, visible ...[]uint) ([]model.Area, error)
	// CountVideosByArea 返回每个地域下的影片数量。
	CountVideosByArea(visible ...[]uint) (map[uint]int64, error)
}

// areaRepository 是 Area 实体专属的仓储对象。
type areaRepository struct {
	db *gorm.DB
}

func NewAreaRepository(db *gorm.DB) AreaRepository {
	return &areaRepository{db: db}
}

var _ AreaRepository = (*areaRepository)(nil)

// ParseAreas 把接口返回的地域串拆成地域名切片。
//
// 绝大多数站点只给一个地域（「美国」），但也有「美国/英国」这类多值写法，
// 因此与标签共用同一套拆分规则，保证两者行为一致。
func ParseAreas(raw string) []string {
	return splitMultiValues(raw)
}

// EnsureForVideos 在单个事务里为本批次出现的全部地域做「有则复用、无则创建」，
// 并按源去重。地域键带 sourceID 限定，因此每个资源站拥有独立的一套地域。
//
// 与标签同理：本函数只创建地域实体，关联回填交由 LinkVideos 完成
// （影片主键要等 UpsertVideos 之后才可知）。
func (r *areaRepository) EnsureForVideos(sourceID uint, videos []model.Video, cache ...*CrawlCache) (map[string][]uint, error) {
	need := make(map[string][]string, len(videos))
	allNames := make([]string, 0)
	seenName := make(map[string]struct{})
	for i := range videos {
		v := &videos[i]
		names := ParseAreas(v.VodArea)
		if len(names) == 0 {
			continue
		}
		need[v.VodID] = names
		for _, n := range names {
			key := strings.ToLower(n)
			if _, ok := seenName[key]; ok {
				continue
			}
			seenName[key] = struct{}{}
			allNames = append(allNames, n)
		}
	}
	out := make(map[string][]uint, len(need))
	if len(allNames) == 0 {
		return out, nil
	}

	// 有任务级缓存时纯内存解析（只把真正新增的批量写回并同步缓存），
	// 无缓存时走原来的事务内批量 find-or-create。
	var nameToID map[string]uint
	if c := cacheMatch(cache, sourceID); c != nil {
		ids, err := r.ensureAreaIDsCached(sourceID, allNames, c)
		if err != nil {
			return nil, err
		}
		nameToID = ids
	} else {
		err := r.db.Transaction(func(tx *gorm.DB) error {
			ids, err := ensureAreaIDs(tx, sourceID, allNames)
			if err != nil {
				return err
			}
			nameToID = ids
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	for vodID, names := range need {
		ids := make([]uint, 0, len(names))
		for _, n := range names {
			if id, ok := nameToID[strings.ToLower(n)]; ok {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			out[vodID] = ids
		}
	}
	return out, nil
}

// ensureAreaIDs 在给定事务内把一批地域名解析为主键，缺失的就地创建。
// 批量实现：一次查询取回全部已存在的地域，只把缺失项批量插入，避免逐条 find-or-create
// 的 O(N) 往返。返回 map 的键是小写地域名（与调用方的去重键一致）。
func ensureAreaIDs(tx *gorm.DB, sourceID uint, names []string) (map[string]uint, error) {
	out := make(map[string]uint, len(names))
	// 去重并保持首个写法；收集用于一次查询的小写名列表。
	firstWrite := make(map[string]string, len(names))
	keys := make([]string, 0, len(names))
	for _, name := range names {
		key := strings.ToLower(name)
		if _, ok := firstWrite[key]; ok {
			continue
		}
		firstWrite[key] = name
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return out, nil
	}

	// ① 一次批量查询所有已存在地域（只取 id/name）。
	var found []struct {
		ID   uint   `gorm:"column:id"`
		Name string `gorm:"column:name"`
	}
	if err := tx.Model(&model.Area{}).
		Select("id, name").
		Where("source_id = ? AND lower(name) IN ?", sourceID, keys).
		Scan(&found).Error; err != nil {
		return nil, err
	}
	for _, row := range found {
		out[strings.ToLower(row.Name)] = row.ID
	}

	// ② 只把缺失的批量插入；CreateInBatches 会回填自增 ID。
	missing := make([]model.Area, 0, len(keys))
	missingKeys := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, ok := out[key]; ok {
			continue
		}
		missing = append(missing, model.Area{SourceID: sourceID, Name: firstWrite[key]})
		missingKeys = append(missingKeys, key)
	}
	if len(missing) > 0 {
		if err := tx.CreateInBatches(&missing, 200).Error; err != nil {
			return nil, err
		}
		for i, row := range missing {
			out[missingKeys[i]] = row.ID
		}
	}
	return out, nil
}

// ensureAreaIDsCached 用任务级缓存把地域名解析为主键：命中即返回，只有缺失项才
// 批量插入并写回缓存。与标签同构，绝大多数页都是 0 条 SQL、0 个事务。
func (r *areaRepository) ensureAreaIDsCached(sourceID uint, names []string, c *CrawlCache) (map[string]uint, error) {
	first, keys := firstWriteNames(names)
	out := make(map[string]uint, len(keys))
	missing := make([]model.Area, 0, len(keys))
	missingKeys := make([]string, 0, len(keys))
	for _, key := range keys {
		if id, ok := c.areas[key]; ok {
			out[key] = id
			continue
		}
		missing = append(missing, model.Area{SourceID: sourceID, Name: first[key]})
		missingKeys = append(missingKeys, key)
	}
	if len(missing) == 0 {
		return out, nil
	}
	if err := r.db.Transaction(func(tx *gorm.DB) error {
		return tx.CreateInBatches(&missing, 200).Error
	}); err != nil {
		return nil, err
	}
	for i, row := range missing {
		out[missingKeys[i]] = row.ID
		c.areas[missingKeys[i]] = row.ID
	}
	return out, nil
}

// LinkVideos 用本批次的 (vod_id → 影片主键) 与 (vod_id → area 主键) 替换影片的地域关联。
//
// 与标签同构，采用「读现状 → 算差异 → 只写差异」：先一次查询取回本批影片已有的
// 关联，只有关联确实变化的影片才走「先删后插」。结果保持幂等与自愈（地域变化会
// 更新、清空会清除历史关联），但地域不变的页不再产生任何 DELETE / INSERT。
func (r *areaRepository) LinkVideos(idByVod map[string]uint, areasByVod map[string][]uint) error {
	if len(idByVod) == 0 {
		return nil
	}
	want := make(map[uint]map[uint]struct{}, len(idByVod))
	vids := make([]uint, 0, len(idByVod))
	for vodID, videoID := range idByVod {
		if videoID == 0 {
			continue
		}
		vids = append(vids, videoID)
		set := make(map[uint]struct{}, len(areasByVod[vodID]))
		for _, aid := range areasByVod[vodID] {
			set[aid] = struct{}{}
		}
		want[videoID] = set
	}
	if len(vids) == 0 {
		return nil
	}

	var rows []model.VideoArea
	if err := r.db.Model(&model.VideoArea{}).
		Select("video_id, area_id").
		Where("video_id IN ?", vids).Scan(&rows).Error; err != nil {
		return err
	}
	have := make(map[uint]map[uint]struct{}, len(rows))
	for _, row := range rows {
		if have[row.VideoID] == nil {
			have[row.VideoID] = make(map[uint]struct{})
		}
		have[row.VideoID][row.AreaID] = struct{}{}
	}

	changed := make([]uint, 0)
	for videoID, wantSet := range want {
		if !sameIDSet(have[videoID], wantSet) {
			changed = append(changed, videoID)
		}
	}
	if len(changed) == 0 {
		return nil // 关联未变：0 次写入
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("video_id IN ?", changed).Delete(&model.VideoArea{}).Error; err != nil {
			return err
		}
		insert := make([]model.VideoArea, 0)
		for _, videoID := range changed {
			for aid := range want[videoID] {
				insert = append(insert, model.VideoArea{VideoID: videoID, AreaID: aid})
			}
		}
		if len(insert) > 0 {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).
				CreateInBatches(&insert, 200).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// List 返回地域，按站点与名称正序；sourceID 为 0 时返回全部站点的地域。
func (r *areaRepository) List(sourceID uint, visible ...[]uint) ([]model.Area, error) {
	q := applyVisibility(r.db.Model(&model.Area{}), "source_id", firstVisible(visible...))
	if sourceID > 0 {
		q = q.Where("source_id = ?", sourceID)
	}
	var items []model.Area
	if err := q.Order("source_id ASC, name ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// CountVideosByArea 返回每个地域下的影片数量（按连接表聚合）。
func (r *areaRepository) CountVideosByArea(visible ...[]uint) (map[uint]int64, error) {
	type row struct {
		AreaID uint
		Cnt    int64
	}
	var rows []row
	q := r.db.Model(&model.VideoArea{}).
		Select("area_id, count(*) as cnt").
		Group("area_id")
	if v := firstVisible(visible...); v != nil {
		if err := q.Where("video_id IN (?)",
			r.db.Model(&model.Video{}).
				Select("id").
				Where("source_id IN ?", v)).
			Error; err != nil {
			return nil, err
		}
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[uint]int64, len(rows))
	for _, row := range rows {
		out[row.AreaID] = row.Cnt
	}
	return out, nil
}

// backfillAreas 把历史数据里 videos.vod_area 的文本补建成 areas + video_areas。
//
// 地域从「影片上的字符串列」改成「独立实体 + 连接表」后，老库里的影片没有关联行，
// 前端下拉会是空的，直到重新采集一遍——因此升级时按现有文本回灌一次。
// 只在 video_areas 为空时执行（新库、已回灌过的库都是空转）。
func backfillAreas(db *gorm.DB) error {
	var linked int64
	if err := db.Model(&model.VideoArea{}).Count(&linked).Error; err != nil {
		return err
	}
	if linked > 0 {
		return nil
	}
	var lastID uint
	for {
		var batch []model.Video
		if err := db.Select("id, source_id, vod_area").
			Where("id > ? AND vod_area IS NOT NULL AND vod_area <> ''", lastID).
			Order("id ASC").Limit(500).Find(&batch).Error; err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		lastID = batch[len(batch)-1].ID

		// 按站点分组，逐站点确保地域实体存在，再批量写关联。
		bySource := make(map[uint][]model.Video)
		for _, v := range batch {
			bySource[v.SourceID] = append(bySource[v.SourceID], v)
		}
		for sourceID, videos := range bySource {
			if err := db.Transaction(func(tx *gorm.DB) error {
				names := make([]string, 0)
				seen := make(map[string]struct{})
				for _, v := range videos {
					for _, n := range ParseAreas(v.VodArea) {
						key := strings.ToLower(n)
						if _, ok := seen[key]; ok {
							continue
						}
						seen[key] = struct{}{}
						names = append(names, n)
					}
				}
				idByName, err := ensureAreaIDs(tx, sourceID, names)
				if err != nil {
					return err
				}
				rows := make([]model.VideoArea, 0, len(videos))
				for _, v := range videos {
					for _, n := range ParseAreas(v.VodArea) {
						if id, ok := idByName[strings.ToLower(n)]; ok {
							rows = append(rows, model.VideoArea{VideoID: v.ID, AreaID: id})
						}
					}
				}
				if len(rows) == 0 {
					return nil
				}
				return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
			}); err != nil {
				return err
			}
		}
	}
}
