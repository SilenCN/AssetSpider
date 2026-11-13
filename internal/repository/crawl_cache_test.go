package repository

import (
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/utils"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// cacheTestPages 覆盖采集里最容易出分歧的几类变化：一级 / 二级分类、父级分类
// 延后作为影片自身出现、同 type_id 改名、新标签 / 地域、标签与地域被清空、重复页。
func cacheTestPages() [][]model.Video {
	return [][]model.Video{
		{
			{SourceID: 1, VodID: "a", TypeID: "6", TypeID1: "1", TypeName: "动作片", VodTag: "科幻,动作", VodArea: "美国", Hash: "h-a1", VodTime: "2026-11-01"},
			{SourceID: 1, VodID: "b", TypeID: "7", TypeID1: "1", TypeName: "喜剧片", VodTag: "喜剧", VodArea: "大陆", Hash: "h-b1", VodTime: "2026-11-01"},
		},
		{
			{SourceID: 1, VodID: "c", TypeID: "8", TypeID1: "2", TypeName: "悬疑", VodTag: "科幻/悬疑", VodArea: "英国", Hash: "h-c1", VodTime: "2026-11-02"},
			{SourceID: 1, VodID: "a", TypeID: "6", TypeID1: "1", TypeName: "动作片2", VodTag: "科幻,动作,2025", VodArea: "美国", Hash: "h-a2", VodTime: "2026-11-02"},
		},
		{
			{SourceID: 1, VodID: "d", TypeID: "1", TypeID1: "", TypeName: "电影", VodTag: "文艺", VodArea: "法国", Hash: "h-d1", VodTime: "2026-11-03"},
			{SourceID: 1, VodID: "b", TypeID: "7", TypeID1: "1", TypeName: "喜剧片", VodTag: "", VodArea: "", Hash: "h-b2", VodTime: "2026-11-03"},
		},
		{
			{SourceID: 1, VodID: "a", TypeID: "6", TypeID1: "1", TypeName: "动作片2", VodTag: "科幻,动作,2025", VodArea: "美国", Hash: "h-a2", VodTime: "2026-11-02"},
			{SourceID: 1, VodID: "c", TypeID: "8", TypeID1: "2", TypeName: "悬疑", VodTag: "科幻/悬疑", VodArea: "英国", Hash: "h-c1", VodTime: "2026-11-02"},
		},
	}
}

func cloneVideos(src []model.Video) []model.Video {
	out := make([]model.Video, len(src))
	copy(out, src)
	return out
}

// runCacheTestPage 复刻 crawl.go 的一页归档链路：分类 → 标签 / 地域 → 影片入库 →
// 主键解析 → 关联回填。cache 为 nil 时走原来的逐页查库路径。
func runCacheTestPage(t *testing.T, r *Repositories, c *CrawlCache, videos []model.Video) {
	t.Helper()
	ids, err := r.Categories.EnsureForVideos(1, videos, c)
	if err != nil {
		t.Fatal(err)
	}
	for i := range videos {
		videos[i].CategoryID = ids[utils.CategoryKey(videos[i].TypeID, videos[i].TypeName)]
	}
	tagIDs, err := r.Tags.EnsureForVideos(1, videos, c)
	if err != nil {
		t.Fatal(err)
	}
	areaIDs, err := r.Areas.EnsureForVideos(1, videos, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Videos.UpsertVideos(videos, c); err != nil {
		t.Fatal(err)
	}
	vodIDs := make([]string, 0, len(videos))
	for i := range videos {
		vodIDs = append(vodIDs, videos[i].VodID)
	}
	idByVod, err := r.Videos.MapVodIDs(1, vodIDs, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Tags.LinkVideos(idByVod, tagIDs); err != nil {
		t.Fatal(err)
	}
	if err := r.Areas.LinkVideos(idByVod, areaIDs); err != nil {
		t.Fatal(err)
	}
}

// crawlSnapshot 把库内归档结果归一成可比较的文本：只比较业务键（type_id / 名称 /
// vod_id），不比较自增主键与时间戳，避免两个独立库的 ID 分配差异造成假失败。
func crawlSnapshot(t *testing.T, r *Repositories) string {
	t.Helper()
	var b strings.Builder
	var cats []model.Category
	if err := r.db.Order("id ASC").Find(&cats).Error; err != nil {
		t.Fatal(err)
	}
	catType := make(map[uint]string, len(cats))
	for _, c := range cats {
		catType[c.ID] = c.TypeID
	}
	catLines := make([]string, 0, len(cats))
	for _, c := range cats {
		parent := ""
		if c.ParentID != 0 {
			parent = catType[c.ParentID]
		}
		catLines = append(catLines, fmt.Sprintf("cat|%s|%s|%s|%d|%s", c.TypeID, c.Name, c.TypePID, c.Level, parent))
	}
	sort.Strings(catLines)
	for _, l := range catLines {
		b.WriteString(l + "\n")
	}

	var tags []model.Tag
	if err := r.db.Order("name ASC").Find(&tags).Error; err != nil {
		t.Fatal(err)
	}
	tagName := make(map[uint]string, len(tags))
	for _, x := range tags {
		tagName[x.ID] = x.Name
		b.WriteString("tag|" + x.Name + "\n")
	}
	var areas []model.Area
	if err := r.db.Order("name ASC").Find(&areas).Error; err != nil {
		t.Fatal(err)
	}
	areaName := make(map[uint]string, len(areas))
	for _, x := range areas {
		areaName[x.ID] = x.Name
		b.WriteString("area|" + x.Name + "\n")
	}

	var videos []model.Video
	if err := r.db.Order("vod_id ASC").Find(&videos).Error; err != nil {
		t.Fatal(err)
	}
	vodByID := make(map[uint]string, len(videos))
	for _, v := range videos {
		vodByID[v.ID] = v.VodID
		b.WriteString(fmt.Sprintf("video|%s|%s|%s\n", v.VodID, v.Hash, catType[v.CategoryID]))
	}

	var vt []model.VideoTag
	if err := r.db.Find(&vt).Error; err != nil {
		t.Fatal(err)
	}
	vtagLines := make([]string, 0, len(vt))
	for _, x := range vt {
		vtagLines = append(vtagLines, fmt.Sprintf("vtag|%s|%s", vodByID[x.VideoID], tagName[x.TagID]))
	}
	sort.Strings(vtagLines)
	for _, l := range vtagLines {
		b.WriteString(l + "\n")
	}
	var va []model.VideoArea
	if err := r.db.Find(&va).Error; err != nil {
		t.Fatal(err)
	}
	vareaLines := make([]string, 0, len(va))
	for _, x := range va {
		vareaLines = append(vareaLines, fmt.Sprintf("varea|%s|%s", vodByID[x.VideoID], areaName[x.AreaID]))
	}
	sort.Strings(vareaLines)
	for _, l := range vareaLines {
		b.WriteString(l + "\n")
	}
	return b.String()
}

// TestCrawlCacheMatchesDBPath 是缓存路径的正确性基线：同样的数据在两个独立库上
// 分别走「逐页查库」与「任务级缓存」，最终归档结果必须逐字一致。
func TestCrawlCacheMatchesDBPath(t *testing.T) {
	pages := cacheTestPages()
	plain := newTestRepos(t)
	cached := newTestRepos(t)

	cache, err := NewCrawlCache(1, cached.Categories, cached.Tags, cached.Areas)
	if err != nil {
		t.Fatal(err)
	}
	for i, page := range pages {
		runCacheTestPage(t, plain, nil, cloneVideos(page))
		runCacheTestPage(t, cached, cache, cloneVideos(page))
		// 每页结束都对齐一次，尽早定位是哪一页出现分歧。
		want, got := crawlSnapshot(t, plain), crawlSnapshot(t, cached)
		if want != got {
			t.Fatalf("第 %d 页后缓存路径与查库路径结果不一致\n--- 查库 ---\n%s\n--- 缓存 ---\n%s", i+1, want, got)
		}
	}
}

// TestCrawlCacheCreatesNewCategoriesIncrementally 验证缓存建立在空库上后，
// 后续页面新出现的分类 / 标签 / 地域仍会被增量写回，父子关系也能补挂。
func TestCrawlCacheCreatesNewCategoriesIncrementally(t *testing.T) {
	r := newTestRepos(t)
	cache, err := NewCrawlCache(1, r.Categories, r.Tags, r.Areas)
	if err != nil {
		t.Fatal(err)
	}
	if len(cache.cats) != 0 || len(cache.tags) != 0 || len(cache.areas) != 0 {
		t.Fatalf("空库预读缓存应为空: %+v", cache)
	}
	for _, page := range cacheTestPages()[:3] {
		runCacheTestPage(t, r, cache, cloneVideos(page))
	}

	var cats, tags, areas int64
	r.db.Model(&model.Category{}).Where("source_id=1").Count(&cats)
	r.db.Model(&model.Tag{}).Where("source_id=1").Count(&tags)
	r.db.Model(&model.Area{}).Where("source_id=1").Count(&areas)
	if cats < 5 || tags < 5 || areas < 3 {
		t.Fatalf("缓存路径未增量创建类目: cats=%d tags=%d areas=%d", cats, tags, areas)
	}
	// 一级分类「电影」是第三页才作为影片自身出现的，二级分类应已挂到它下面。
	var movie model.Category
	if err := r.db.Where("source_id=1 AND type_id='1'").First(&movie).Error; err != nil {
		t.Fatal(err)
	}
	var children int64
	r.db.Model(&model.Category{}).Where("source_id=1 AND parent_id=?", movie.ID).Count(&children)
	if children < 2 {
		t.Fatalf("父级延后出现后应补挂至少 2 个二级分类，实际 %d", children)
	}
}

// countedRepos 把仓储换成带 SQL 计数的连接，缓存对象仍可复用（纯内存）。
// 构造 NewVideoRepository 时会探测一次 FTS 表，归零把它排除，只统计页面链路。
func countedRepos(r *Repositories, n *int64) *Repositories {
	db := r.db.Session(&gorm.Session{
		Logger: countingLogger{Interface: gormlogger.Default.LogMode(gormlogger.Silent), n: n},
	})
	repos := &Repositories{
		Videos: NewVideoRepository(db), Categories: NewCategoryRepository(db),
		Tags: NewTagRepository(db), Areas: NewAreaRepository(db), db: db,
	}
	atomic.StoreInt64(n, 0)
	return repos
}

// countCachePageSQL 统计一页完整归档链路实际发出的 SQL 条数。
func countCachePageSQL(t *testing.T, r *Repositories, c *CrawlCache, videos []model.Video) int64 {
	t.Helper()
	var n int64
	runCacheTestPage(t, countedRepos(r, &n), c, videos)
	return atomic.LoadInt64(&n)
}

// countCatalogSQL 只统计一页里三个类目归档调用发出的 SQL 条数。
func countCatalogSQL(t *testing.T, r *Repositories, c *CrawlCache, videos []model.Video) int64 {
	t.Helper()
	var n int64
	repos := countedRepos(r, &n)
	if _, err := repos.Categories.EnsureForVideos(1, videos, c); err != nil {
		t.Fatal(err)
	}
	if _, err := repos.Tags.EnsureForVideos(1, videos, c); err != nil {
		t.Fatal(err)
	}
	if _, err := repos.Areas.EnsureForVideos(1, videos, c); err != nil {
		t.Fatal(err)
	}
	return atomic.LoadInt64(&n)
}

// TestCrawlCacheEliminatesPerPageQueries 量化优化效果：类目全部命中的重复页，
// 带缓存时三次类目归档应为 0 条 SQL；整页链路的往返也应明显少于逐页查库。
func TestCrawlCacheEliminatesPerPageQueries(t *testing.T) {
	r := newTestRepos(t)
	page := cacheTestPages()[0]
	// 先用带缓存路径把第一页建起来，构造「下一页什么都没变」的场景。
	cache, err := NewCrawlCache(1, r.Categories, r.Tags, r.Areas)
	if err != nil {
		t.Fatal(err)
	}
	runCacheTestPage(t, r, cache, cloneVideos(page))

	catalogCached := countCatalogSQL(t, r, cache, cloneVideos(page))
	catalogPlain := countCatalogSQL(t, r, nil, cloneVideos(page))
	t.Logf("重复页类目归档 SQL：带缓存 %d / 逐页查库 %d", catalogCached, catalogPlain)
	if catalogCached != 0 {
		t.Errorf("类目全部命中时不应再查库/写库，实际发了 %d 条 SQL", catalogCached)
	}
	if catalogPlain == 0 {
		t.Error("逐页查库路径本应发出类目查询，计数口径可能有问题")
	}

	cachedSQL := countCachePageSQL(t, r, cache, cloneVideos(page))
	plainSQL := countCachePageSQL(t, r, nil, cloneVideos(page))
	t.Logf("重复页整页 SQL 条数：带缓存 %d / 逐页查库 %d", cachedSQL, plainSQL)
	if cachedSQL >= plainSQL {
		t.Errorf("带缓存路径未减少数据库往返：带缓存 %d / 逐页查库 %d", cachedSQL, plainSQL)
	}
}

// TestCrawlCachePreloadedFromExistingDB 覆盖最贴近生产的核心场景：对一个已经
// 采集过、库里已有全部分类 / 标签 / 地域与关联的老库再跑一次全量采集。此时缓存
// 由「预读数据库」而非「缓存路径自己插入」填充，两者字段映射必须一致。
func TestCrawlCachePreloadedFromExistingDB(t *testing.T) {
	pages := cacheTestPages()
	r := newTestRepos(t)
	// 先用无缓存路径把库采满（模拟历史数据），记下归档结果。
	for _, page := range pages {
		runCacheTestPage(t, r, nil, cloneVideos(page))
	}
	before := crawlSnapshot(t, r)

	cache, err := NewCrawlCache(1, r.Categories, r.Tags, r.Areas)
	if err != nil {
		t.Fatal(err)
	}
	if len(cache.cats) == 0 || len(cache.tags) == 0 || len(cache.areas) == 0 {
		t.Fatalf("预读老库后缓存应已有类目: cats=%d tags=%d areas=%d",
			len(cache.cats), len(cache.tags), len(cache.areas))
	}
	// 重采与当前库状态一致的那一页：类目 / 层级 / 关联都应全部命中，0 条归档 SQL。
	last := pages[len(pages)-1]
	if n := countCatalogSQL(t, r, cache, cloneVideos(last)); n != 0 {
		t.Errorf("预读老库后类目应全部命中，实际发了 %d 条归档 SQL", n)
	}
	runCacheTestPage(t, r, cache, cloneVideos(last))
	if after := crawlSnapshot(t, r); before != after {
		t.Fatalf("老库重采后归档结果发生漂移\n--- 之前 ---\n%s\n--- 之后 ---\n%s", before, after)
	}
}

// TestLinkVideosOnlyWritesChangedRows 验证关联差异写：未变不写、变化更新、清空清除。
func TestLinkVideosOnlyWritesChangedRows(t *testing.T) {
	r := newTestRepos(t)
	videos := []model.Video{
		{SourceID: 1, VodID: "x", VodTag: "科幻,动作", VodArea: "美国", Hash: "h1", VodTime: "2026-11-01"},
	}
	ids, err := r.Tags.EnsureForVideos(1, videos)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Videos.UpsertVideos(videos); err != nil {
		t.Fatal(err)
	}
	idByVod, err := r.Videos.MapVodIDs(1, []string{"x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Tags.LinkVideos(idByVod, ids); err != nil {
		t.Fatal(err)
	}

	// 第二次写入完全相同：只应有一次现状 SELECT，不应有 DELETE / INSERT。
	var n int64
	counted := countedRepos(r, &n)
	if err := counted.Tags.LinkVideos(idByVod, ids); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt64(&n); got > 1 {
		t.Errorf("关联未变却发了 %d 条 SQL（预期仅 1 次现状查询）", got)
	}
	var cnt int64
	r.db.Model(&model.VideoTag{}).Count(&cnt)
	if cnt != 2 {
		t.Fatalf("未变写入后关联数应仍为 2，实际 %d", cnt)
	}

	// 清空标签：历史关联应被清除。
	empty, err := r.Tags.EnsureForVideos(1, []model.Video{{SourceID: 1, VodID: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Tags.LinkVideos(idByVod, empty); err != nil {
		t.Fatal(err)
	}
	r.db.Model(&model.VideoTag{}).Count(&cnt)
	if cnt != 0 {
		t.Errorf("标签被清空后应无残留关联，实际 %d", cnt)
	}
}
