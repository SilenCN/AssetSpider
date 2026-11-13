package repository

import (
	"strings"

	"github.com/SilenCN/AssetSpider/internal/model"
)

// cachedCategory 是采集会话缓存里的一条分类快照。
// 保留完整字段是为了在内存里就能判断「名称 / 父级 / 层级是否需要写回」，
// 避免每页把没变化的分类再 UPDATE 一遍、也避免每页全表扫描孤儿分类。
type cachedCategory struct {
	ID       uint
	Key      string
	TypeID   string
	Name     string
	TypePID  string
	ParentID uint
	Level    int
}

// CrawlCache 是一次采集任务的「字典类目」内存缓存。
//
// 采集逐页进行，每页都要为影片解析分类 / 标签 / 地域并回填关联，而这些字典在
// 整次任务里高度重复：一个站点通常只有几十个分类、几百个标签 / 地域，采集却可能
// 有上千页。若每页都查一次库、开几个事务、把没变化的分类再 UPDATE 一遍，数据库
// 往返与事务提交（SQLite 单写者）就会成为采集的主要开销。
//
// 因此在任务开始时一次性把本源已有的类目读进内存，之后每页：
//   - 命中缓存的条目直接返回主键，不查库、不写库；
//   - 只有真正新增或字段变化的条目才写库，并同步回缓存；
//   - 影片主键由 UpsertVideos 直接写进缓存，MapVodIDs 不再额外查库。
//
// 缓存生命周期 = 一次 crawl（含断点续采的每一段），任务结束即丢弃；下次任务重新
// 读库，天然与数据库一致。缓存按 sourceID 独立、不跨任务共享，因此不受不同站点
// 并行采集影响，也无需加锁。所有仓储方法都把它作为**可选变参**接收：不传缓存时
// 走原来的查库路径，现有调用方与测试完全不受影响。
type CrawlCache struct {
	sourceID  uint
	cats      map[string]*cachedCategory // cat_key → 分类快照
	catTypeID map[string]uint            // type_id → 分类主键（父级挂接用，取最小 id）
	tags      map[string]uint            // lower(name) → 标签主键
	areas     map[string]uint            // lower(name) → 地域主键
	videoIDs  map[string]uint            // vod_id → 影片主键（本任务已入库的）
}

// NewCrawlCache 为一次采集任务构建字典缓存：一次性读取本源的分类 / 标签 / 地域。
// 三个 List 都是按站点过滤的轻量查询，只在任务开始时各执行一次；之后上千页的
// 归档解析都落在内存里。任一读取失败都返回错误，由调用方决定降级（不传缓存即可）。
func NewCrawlCache(
	sourceID uint,
	categories CategoryRepository,
	tags TagRepository,
	areas AreaRepository,
) (*CrawlCache, error) {
	c := &CrawlCache{
		sourceID:  sourceID,
		cats:      make(map[string]*cachedCategory),
		catTypeID: make(map[string]uint),
		tags:      make(map[string]uint),
		areas:     make(map[string]uint),
		videoIDs:  make(map[string]uint),
	}
	if categories != nil {
		rows, err := categories.List(sourceID)
		if err != nil {
			return nil, err
		}
		for i := range rows {
			row := &rows[i]
			c.cats[row.Key] = &cachedCategory{
				ID: row.ID, Key: row.Key, TypeID: row.TypeID, Name: row.Name,
				TypePID: row.TypePID, ParentID: row.ParentID, Level: row.Level,
			}
			// 同一 type_id 理论上只对应一个分类；老库万一有重复，取 id 最小的
			// 那条，与原来 orphan 归树里 LIMIT 1 的取法保持一致。
			if row.TypeID != "" {
				if _, exists := c.catTypeID[row.TypeID]; !exists {
					c.catTypeID[row.TypeID] = row.ID
				}
			}
		}
	}
	if tags != nil {
		rows, err := tags.List(sourceID)
		if err != nil {
			return nil, err
		}
		for i := range rows {
			c.tags[strings.ToLower(rows[i].Name)] = rows[i].ID
		}
	}
	if areas != nil {
		rows, err := areas.List(sourceID)
		if err != nil {
			return nil, err
		}
		for i := range rows {
			c.areas[strings.ToLower(rows[i].Name)] = rows[i].ID
		}
	}
	return c, nil
}

// cacheMatch 从可选变参里取出与 sourceID 匹配的缓存。
// 没有缓存（普通调用方 / 既有测试）返回 nil，仓储方法照旧走查库路径。
func cacheMatch(cache []*CrawlCache, sourceID uint) *CrawlCache {
	for _, c := range cache {
		if c != nil && c.sourceID == sourceID {
			return c
		}
	}
	return nil
}

// firstWriteNames 把一批名称去重成「小写键 → 首个写法」，返回稳定的键顺序。
// 标签 / 地域的缓存路径与查库路径共用这一规则，保证两者结果一致。
func firstWriteNames(names []string) (map[string]string, []string) {
	first := make(map[string]string, len(names))
	keys := make([]string, 0, len(names))
	for _, name := range names {
		key := strings.ToLower(name)
		if _, ok := first[key]; ok {
			continue
		}
		first[key] = name
		keys = append(keys, key)
	}
	return first, keys
}

// recordVideoID 把 UpsertVideos 刚解析出的影片主键登记进会话缓存。
// 采集主循环随后调 MapVodIDs 时能直接命中，不必「刚入库又查一次库」。
func recordVideoID(cache []*CrawlCache, sourceID uint, vodID string, id uint) {
	if c := cacheMatch(cache, sourceID); c != nil && id != 0 {
		c.videoIDs[vodID] = id
	}
}

// newCategorySnapshot 把一条新建分类登记进缓存。
func (c *CrawlCache) newCategorySnapshot(cat model.Category) *cachedCategory {
	snap := &cachedCategory{
		ID: cat.ID, Key: cat.Key, TypeID: cat.TypeID, Name: cat.Name,
		TypePID: cat.TypePID, ParentID: cat.ParentID, Level: cat.Level,
	}
	c.cats[cat.Key] = snap
	if cat.TypeID != "" {
		if _, exists := c.catTypeID[cat.TypeID]; !exists {
			c.catTypeID[cat.TypeID] = cat.ID
		}
	}
	return snap
}
