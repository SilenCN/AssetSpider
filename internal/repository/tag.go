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
	EnsureForVideos(sourceID uint, videos []model.Video) (map[string][]uint, error)
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
func (r *tagRepository) EnsureForVideos(sourceID uint, videos []model.Video) (map[string][]uint, error) {
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

	// 第二遍：事务内批量 find-or-create。一次查询取回全部已存在标签，只把缺失的
	// 批量插入，避免按名逐条 find-or-create 的 O(N) 往返。
	nameToID := make(map[string]uint, len(allNames))
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

// LinkVideos 用本批次的 (vod_id → 影片主键) 与 (vod_id → tag 主键) 替换影片的标签关联。
//
// 采用「先删后插」：把本批次所有影片（idByVod 的全部条目，无论有无标签）既有的
// video_tags 行清空，再写入本次解析出的标签。因此：
//   - 影片标签变化（如后端修正了 vod_tag）时联动更新；
//   - 影片标签被清空时，历史关联一并清除，不留残影；
//   - 同一影片一个批次只出现一次，重复执行幂等。
//
// 调用方必须把「本批次全部影片」的 id 传入 idByVod（而不只是有标签的），否则无标签
// 影片的旧关联无法被清理。
func (r *tagRepository) LinkVideos(idByVod map[string]uint, tagsByVod map[string][]uint) error {
	if len(idByVod) == 0 {
		return nil
	}
	vids := make([]uint, 0, len(idByVod))
	for _, vid := range idByVod {
		if vid != 0 {
			vids = append(vids, vid)
		}
	}
	if len(vids) == 0 {
		return nil
	}

	return r.db.Transaction(func(tx *gorm.DB) error {
		// 清理本批全部影片的旧关联（含无标签影片，从而清除被移除的历史标签）。
		if err := tx.Where("video_id IN ?", vids).Delete(&model.VideoTag{}).Error; err != nil {
			return err
		}
		// 批量写入新关联（去重 tagID，避免同一标签重复插入导致主键冲突）。
		rows := make([]model.VideoTag, 0)
		for vodID, videoID := range idByVod {
			if videoID == 0 {
				continue
			}
			seen := make(map[uint]struct{})
			for _, tid := range tagsByVod[vodID] {
				if _, ok := seen[tid]; ok {
					continue
				}
				seen[tid] = struct{}{}
				rows = append(rows, model.VideoTag{VideoID: videoID, TagID: tid})
			}
		}
		if len(rows) > 0 {
			// 用 OnConflict 兜底：极端并发下若已有相同关联则忽略，不报错。
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error; err != nil {
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
