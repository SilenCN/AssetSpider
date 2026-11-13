package repository

import (
	"strings"

	"github.com/SilenCN/AssetSpider/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// TagRepository 是 Tag 实体的数据访问契约。
type TagRepository interface {
	// EnsureForVideos 解析一批影片的标签（取自 vod_tag，逗号等分隔），按源去重创建标签，
	// 返回 vod_id → tag 主键列表 的映射，供 service 在影片入库后回填 video_tags 关联。
	// 可选的 CrawlCache 是采集任务级类目缓存：传入后命中项不再查库、也不再写入。
	EnsureForVideos(sourceID uint, videos []model.Video, cache ...*CrawlCache) (map[string][]uint, error)
	// LinkVideos 按 (vod_id → 影片主键) 与 (vod_id → tag 主键) 替换本批次影片的标签关联。
	LinkVideos(idByVod map[string]uint, tagsByVod map[string][]uint) error
	// List 返回标签，按站点与名称排序；sourceID 为 0 时返回全部站点的标签。
	List(sourceID uint, visible ...[]uint) ([]model.Tag, error)
	// CountVideosByTag 返回每个标签下的影片数量。
	CountVideosByTag(visible ...[]uint) (map[uint]int64, error)
}

// tagRepository 是 Tag 实体专属的仓储对象。
type tagRepository struct {
	db *gorm.DB
}

func NewTagRepository(db *gorm.DB) TagRepository {
	return &tagRepository{db: db}
}

var _ TagRepository = (*tagRepository)(nil)

// ParseTags 把接口返回的标签串拆成标签名切片。
//
// 苹果CMS / 海洋CMS 的 vod_tag 通常用半角或全角逗号分隔，也有用斜杠、竖线、
// 顿号或空格分隔的情况；这里统一按这些分隔符拆分，逐条去空格、去空项。
func ParseTags(raw string) []string {
	return splitMultiValues(raw)
}

// splitMultiValues 是标签与地域共用的多值拆分规则。
//
// 先按常见分隔符切分，再对每一段按空白二次拆分，兼容「科幻 动作」「喜剧/爱情」
// 与「美国/英国」等写法；同一串内重复出现的名称只保留第一次的写法。
func splitMultiValues(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', '，', '/', '、', '|', ';', '；', '\t':
			return true
		default:
			return false
		}
	})
	seen := make(map[string]struct{})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		// 含空格的复合段（如「科幻 惊悚」）再拆一次，避免把整串当成一个标签。
		for _, part := range strings.Fields(f) {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			key := strings.ToLower(part)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, part)
		}
	}
	return out
}

// EnsureForVideos 在单个事务里为本批次出现的全部分签做「有则复用、无则创建」，
// 并按源去重（同名标签取首个出现的写法）。分类键带 sourceID 限定，因此每个资源站
// 拥有独立的一套标签，不会跨站串标签。
//
// 注意：本函数只负责「创建标签实体」，不触碰 video_tags 连接表——影片主键要等
// UpsertVideos 之后才可知，关联回填交由 LinkVideos 完成。
func (r *tagRepository) EnsureForVideos(sourceID uint, videos []model.Video, cache ...*CrawlCache) (map[string][]uint, error) {
	// 第一遍：收集每个影片需要的标签名（vod_id → 标签名切片），并去重全部标签名。
	need := make(map[string][]string, len(videos))
	allNames := make([]string, 0)
	seenName := make(map[string]struct{})
	for i := range videos {
		v := &videos[i]
		names := ParseTags(v.VodTag)
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

	// 第二遍：有任务级缓存时纯内存解析（只把真正新增的批量写回并同步缓存），
	// 无缓存时走原来的事务内批量 find-or-create。
	var nameToID map[string]uint
	if c := cacheMatch(cache, sourceID); c != nil {
		ids, err := r.ensureTagIDsCached(sourceID, allNames, c)
		if err != nil {
			return nil, err
		}
		nameToID = ids
	} else {
		err := r.db.Transaction(func(tx *gorm.DB) error {
			ids, err := ensureTagIDs(tx, sourceID, allNames)
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

	// 第三遍：把标签名映射成主键，组装 vod_id → tagIDs 返回。
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

// ensureTagIDs 在事务内把一批标签名解析为主键，缺失的批量创建。
// 批量实现：一次查询取回全部已存在标签，只把缺失项批量插入；返回键为小写标签名。
func ensureTagIDs(tx *gorm.DB, sourceID uint, names []string) (map[string]uint, error) {
	out := make(map[string]uint, len(names))
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

	var found []struct {
		ID   uint   `gorm:"column:id"`
		Name string `gorm:"column:name"`
	}
	if err := tx.Model(&model.Tag{}).
		Select("id, name").
		Where("source_id = ? AND lower(name) IN ?", sourceID, keys).
		Scan(&found).Error; err != nil {
		return nil, err
	}
	for _, row := range found {
		out[strings.ToLower(row.Name)] = row.ID
	}

	missing := make([]model.Tag, 0, len(keys))
	missingKeys := make([]string, 0, len(keys))
	for _, key := range keys {
		if _, ok := out[key]; ok {
			continue
		}
		missing = append(missing, model.Tag{SourceID: sourceID, Name: firstWrite[key]})
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

// ensureTagIDsCached 用任务级缓存把标签名解析为主键：命中即返回，只有缺失项才
// 批量插入并写回缓存。采集绝大多数页的标签都已存在，因此这一步通常是 0 条 SQL、
// 0 个事务；只有站点出现新标签时才写库。
func (r *tagRepository) ensureTagIDsCached(sourceID uint, names []string, c *CrawlCache) (map[string]uint, error) {
	first, keys := firstWriteNames(names)
	out := make(map[string]uint, len(keys))
	missing := make([]model.Tag, 0, len(keys))
	missingKeys := make([]string, 0, len(keys))
	for _, key := range keys {
		if id, ok := c.tags[key]; ok {
			out[key] = id
			continue
		}
		missing = append(missing, model.Tag{SourceID: sourceID, Name: first[key]})
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
		c.tags[missingKeys[i]] = row.ID
	}
	return out, nil
}

// sameIDSet 判断两个主键集合是否完全一致（nil 与空集合视为相同）。
// 标签 / 地域的关联差异写共用，避免把未变化的影片重写一遍连接表。
func sameIDSet(a, b map[uint]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

// LinkVideos 用本批次的 (vod_id → 影片主键) 与 (vod_id → tag 主键) 替换影片的标签关联。
//
// 采用「读现状 → 算差异 → 只写差异」：先一次查询取回本批影片已有的关联，与本次
// 解析结果比对；只有关联确实变化的影片才走「先删后插」。结果与过去无条件重写
// 完全一致（标签变化会更新、清空会清除历史关联、重复执行幂等），但绝大多数页
// 标签没变，因而不再产生任何 DELETE / INSERT，避免逐页无条件重写连接表。
//
// 调用方必须把「本批次全部影片」的 id 传入 idByVod（而不只是有标签的），否则无标签
// 影片的旧关联无法被清理。
func (r *tagRepository) LinkVideos(idByVod map[string]uint, tagsByVod map[string][]uint) error {
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
		set := make(map[uint]struct{}, len(tagsByVod[vodID]))
		for _, tid := range tagsByVod[vodID] {
			set[tid] = struct{}{}
		}
		want[videoID] = set
	}
	if len(vids) == 0 {
		return nil
	}

	// 一次查询取回本批影片现有关联（video_id 上有主键索引，是小批量点查）。
	var rows []model.VideoTag
	if err := r.db.Model(&model.VideoTag{}).
		Select("video_id, tag_id").
		Where("video_id IN ?", vids).Scan(&rows).Error; err != nil {
		return err
	}
	have := make(map[uint]map[uint]struct{}, len(rows))
	for _, row := range rows {
		if have[row.VideoID] == nil {
			have[row.VideoID] = make(map[uint]struct{})
		}
		have[row.VideoID][row.TagID] = struct{}{}
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
		if err := tx.Where("video_id IN ?", changed).Delete(&model.VideoTag{}).Error; err != nil {
			return err
		}
		insert := make([]model.VideoTag, 0)
		for _, videoID := range changed {
			for tid := range want[videoID] {
				insert = append(insert, model.VideoTag{VideoID: videoID, TagID: tid})
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

// List 返回标签，按站点与名称正序；sourceID 为 0 时返回全部站点的标签。
func (r *tagRepository) List(sourceID uint, visible ...[]uint) ([]model.Tag, error) {
	q := applyVisibility(r.db.Model(&model.Tag{}), "source_id", firstVisible(visible...))
	if sourceID > 0 {
		q = q.Where("source_id = ?", sourceID)
	}
	var items []model.Tag
	if err := q.Order("source_id ASC, name ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// CountVideosByTag 返回每个标签下的影片数量（按连接表聚合）。
func (r *tagRepository) CountVideosByTag(visible ...[]uint) (map[uint]int64, error) {
	type row struct {
		TagID uint
		Cnt   int64
	}
	var rows []row
	q := r.db.Model(&model.VideoTag{}).
		Select("tag_id, count(*) as cnt").
		Group("tag_id")
	// 受站点可见性约束：只统计可见站点的影片关联的标签。
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
		out[row.TagID] = row.Cnt
	}
	return out, nil
}
