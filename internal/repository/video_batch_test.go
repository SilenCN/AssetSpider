package repository

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// countingLogger 统计 GORM 实际执行的 SQL 语句数（每条 Trace 即一次语句）。
type countingLogger struct {
	gormlogger.Interface
	n *int64
}

func (l countingLogger) Trace(ctx context.Context, begin time.Time,
	fc func() (string, int64), err error) {
	atomic.AddInt64(l.n, 1)
	if l.Interface != nil {
		l.Interface.Trace(ctx, begin, fc, err)
	}
}
func (l countingLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	clone := l
	clone.Interface = l.Interface.LogMode(level)
	return clone
}

// TestUpsertVideosBatchesAndCounts 验证批量入库：
//  1. 混合批次（新增 / 未变 / 已变 / 批内重复）的统计与最终数据正确；
//  2. 纯新增 N 条只发「常数级」SQL（1 次 SELECT + 少量 INSERT 批次），
//     而不是旧实现每条 2 句（SELECT+INSERT）的 O(N)。
func TestUpsertVideosBatchesAndCounts(t *testing.T) {
	r := newTestRepos(t)
	mk := func(i int, hash string) model.Video {
		return model.Video{
			SourceID: 1, VodID: fmt.Sprintf("vod-%03d", i),
			VodName: fmt.Sprintf("影片%d", i), Hash: hash, VodTime: "2026-11-11 10:00:00",
			VodPlayURL: "https://x/1.m3u8",
		}
	}

	// 先入库 30 条作为「已存在」。
	seed := make([]model.Video, 30)
	for i := range seed {
		seed[i] = mk(i, "h-orig")
	}
	if st, err := r.Videos.UpsertVideos(seed); err != nil || st.New != 30 {
		t.Fatalf("种子数据写入异常: %+v %v", st, err)
	}

	// 混合批次：10 未变 + 10 已变 + 10 新增 + 2 条批内重复（vod-000 再出现）。
	mixed := make([]model.Video, 0, 32)
	for i := 0; i < 10; i++ {
		mixed = append(mixed, mk(i, "h-orig")) // 未变
	}
	for i := 10; i < 20; i++ {
		mixed = append(mixed, mk(i, "h-changed")) // 已变
	}
	for i := 30; i < 40; i++ {
		mixed = append(mixed, mk(i, "h-new")) // 新增
	}
	mixed = append(mixed, mk(0, "h-changed-again")) // 批内重复：覆盖上面 vod-000，改判已变
	mixed = append(mixed, mk(5, "h-orig"))          // 批内重复：与未变一致，仍算未变

	st, err := r.Videos.UpsertVideos(mixed)
	if err != nil {
		t.Fatal(err)
	}
	// 去重后：vod-000 由「未变」变「已变」，vod-005 仍是未变。
	// 未变 9（1..9）+ 已变 11（0,10..19）+ 新增 10（30..39）。
	if st.New != 10 || st.Updated != 11 || st.Unchanged != 9 {
		t.Fatalf("混合批次统计异常: %+v（期望 new=10 updated=11 unchanged=9）", st)
	}
	var total int64
	r.db.Model(&model.Video{}).Count(&total)
	if total != 40 {
		t.Fatalf("最终影片总数应为 40，实际 %d", total)
	}
	// 已变条目的 hash 应更新，未变条目的 hash 保持。
	var v0, v1, v30 model.Video
	r.db.Where("source_id=1 AND vod_id='vod-000'").First(&v0)
	r.db.Where("source_id=1 AND vod_id='vod-001'").First(&v1)
	r.db.Where("source_id=1 AND vod_id='vod-030'").First(&v30)
	if v0.Hash != "h-changed-again" {
		t.Errorf("批内重复最后一条未生效: hash=%s", v0.Hash)
	}
	if v1.Hash != "h-orig" {
		t.Errorf("未变条目 hash 被误改: hash=%s", v1.Hash)
	}
	if v30.VodName != "影片30" || v30.FirstSeenAt.IsZero() {
		t.Errorf("新增条目异常: %+v", v30)
	}

	// 纯新增 50 条：统计 SQL 条数，验证批量而非逐条。
	var n int64
	batch := make([]model.Video, 50)
	for i := range batch {
		batch[i] = mk(100+i, "h-batch")
	}
	counted := r.Videos.(*videoRepository).db.Session(&gorm.Session{
		Logger: countingLogger{Interface: gormlogger.Default.LogMode(gormlogger.Silent), n: &n},
	})
	stats2, err := (&videoRepository{db: counted, search: true}).UpsertVideos(batch)
	if err != nil || stats2.New != 50 {
		t.Fatalf("批量新增异常: %+v %v", stats2, err)
	}
	// 旧实现：50 × (1 SELECT + 1 INSERT) = 100 句。批量后：1 SELECT + 1 INSERT(多值) = 2 句。
	// 放宽到 < 20，允许事务 BEGIN/COMMIT、SavePoint、FTS 触发器等额外语句。
	if n >= 20 {
		t.Errorf("批量新增 50 条发了 %d 条 SQL，疑似仍在逐条写入（应远小于 100）", n)
	}
	t.Logf("批量新增 50 条实际 SQL 语句数: %d", atomic.LoadInt64(&n))

	// 纯「未变」50 条：同样应批量刷 last_seen_at，而非 50 条 UPDATE。
	n = 0
	counted2 := r.Videos.(*videoRepository).db.Session(&gorm.Session{
		Logger: countingLogger{Interface: gormlogger.Default.LogMode(gormlogger.Silent), n: &n},
	})
	stats3, err := (&videoRepository{db: counted2, search: true}).UpsertVideos(batch)
	if err != nil || stats3.Unchanged != 50 {
		t.Fatalf("批量未变异常: %+v %v", stats3, err)
	}
	if n >= 20 {
		t.Errorf("批量未变 50 条发了 %d 条 SQL，疑似仍在逐条 UPDATE", n)
	}
	t.Logf("批量未变 50 条实际 SQL 语句数: %d", atomic.LoadInt64(&n))
}

// TestUpsertVideosMultiSource 验证不同站点的同 vod_id 互不覆盖（唯一键含 source_id）。
func TestUpsertVideosMultiSource(t *testing.T) {
	r := newTestRepos(t)
	batch := []model.Video{
		{SourceID: 1, VodID: "same", VodName: "站点1的片", Hash: "a", VodTime: "2026-11-11"},
		{SourceID: 2, VodID: "same", VodName: "站点2的片", Hash: "b", VodTime: "2026-11-11"},
	}
	st, err := r.Videos.UpsertVideos(batch)
	if err != nil || st.New != 2 {
		t.Fatalf("跨站点同 vod_id 应各自新增: %+v %v", st, err)
	}
	var v1, v2 model.Video
	r.db.Where("source_id=1 AND vod_id='same'").First(&v1)
	r.db.Where("source_id=2 AND vod_id='same'").First(&v2)
	if v1.ID == 0 || v2.ID == 0 || v1.ID == v2.ID || v1.VodName != "站点1的片" || v2.VodName != "站点2的片" {
		t.Fatalf("跨站点数据串了: v1=%+v v2=%+v", v1, v2)
	}
	// 更新站点2，不应影响站点1。
	st, err = r.Videos.UpsertVideos([]model.Video{
		{SourceID: 2, VodID: "same", VodName: "站点2改名", Hash: "c", VodTime: "2026-11-11"},
	})
	if err != nil || st.Updated != 1 {
		t.Fatalf("跨站点更新异常: %+v %v", st, err)
	}
	r.db.Where("source_id=1 AND vod_id='same'").First(&v1)
	if v1.VodName != "站点1的片" {
		t.Errorf("站点2更新污染了站点1: %+v", v1)
	}
}

// TestUpsertVideosBatchUpdateFieldAlignment 验证批量 ON CONFLICT 更新时，
// 每条记录各写各的字段值，不会因多值插入而串位（批量特有的风险）。
func TestUpsertVideosBatchUpdateFieldAlignment(t *testing.T) {
	r := newTestRepos(t)
	seed := make([]model.Video, 25)
	for i := range seed {
		seed[i] = model.Video{
			SourceID: 1, VodID: fmt.Sprintf("align-%02d", i),
			VodName: fmt.Sprintf("原名%02d", i), VodActor: "原演员",
			Hash: "old", VodTime: "2026-11-11",
		}
	}
	if _, err := r.Videos.UpsertVideos(seed); err != nil {
		t.Fatal(err)
	}

	// 批量更新：每条的 name / actor / hash 都不同。
	updates := make([]model.Video, 25)
	for i := range updates {
		updates[i] = model.Video{
			SourceID: 1, VodID: fmt.Sprintf("align-%02d", i),
			VodName: fmt.Sprintf("新名%02d", i), VodActor: fmt.Sprintf("演员%02d", i),
			Hash: fmt.Sprintf("new-%02d", i), VodTime: "2026-11-12",
		}
	}
	st, err := r.Videos.UpsertVideos(updates)
	if err != nil || st.Updated != 25 {
		t.Fatalf("批量更新统计异常: %+v %v", st, err)
	}
	for i := 0; i < 25; i++ {
		var v model.Video
		r.db.Where("source_id=1 AND vod_id=?", fmt.Sprintf("align-%02d", i)).First(&v)
		wantName := fmt.Sprintf("新名%02d", i)
		wantActor := fmt.Sprintf("演员%02d", i)
		wantHash := fmt.Sprintf("new-%02d", i)
		if v.VodName != wantName || v.VodActor != wantActor || v.Hash != wantHash {
			t.Errorf("第 %d 条批量更新字段串位: name=%s actor=%s hash=%s", i, v.VodName, v.VodActor, v.Hash)
		}
		if v.FirstSeenAt.IsZero() {
			t.Errorf("第 %d 条更新后首见时间被清空", i)
		}
	}
}

// TestEnsureForVideosBatched 验证标签 / 地域 / 分类的 EnsureForVideos 已批量化：
// 一次给 40 个全新名称，SQL 语句数应远低于逐条 find-or-create 的 O(N)。
func TestEnsureForVideosBatched(t *testing.T) {
	r := newTestRepos(t)
	videos := make([]model.Video, 40)
	for i := range videos {
		videos[i] = model.Video{
			SourceID: 1, VodID: fmt.Sprintf("v%d", i),
			VodTag:   fmt.Sprintf("标签%d", i),
			VodArea:  fmt.Sprintf("地域%d", i),
			TypeID:   fmt.Sprintf("%d", 100+i),
			TypeName: fmt.Sprintf("分类%d", i),
		}
	}

	countSQL := func() int64 {
		var n int64
		db := r.db.Session(&gorm.Session{
			Logger: countingLogger{Interface: gormlogger.Default.LogMode(gormlogger.Silent), n: &n},
		})
		repos := &Repositories{
			Tags:       NewTagRepository(db),
			Areas:      NewAreaRepository(db),
			Categories: NewCategoryRepository(db),
		}
		if _, err := repos.Tags.EnsureForVideos(1, videos); err != nil {
			t.Error(err)
		}
		if _, err := repos.Areas.EnsureForVideos(1, videos); err != nil {
			t.Error(err)
		}
		if _, err := repos.Categories.EnsureForVideos(1, videos); err != nil {
			t.Error(err)
		}
		return atomic.LoadInt64(&n)
	}

	// 40 个新标签 + 40 个新地域 + 40 个新分类。旧实现每种约 40×2=80 句，合计 ~240 句。
	n := countSQL()
	t.Logf("40 条影片首次归档（标签/地域/分类各 40 个新值）SQL 语句数: %d", n)
	if n >= 60 {
		t.Errorf("首次归档发了 %d 条 SQL，疑似仍在逐条 find-or-create（应远小于 240）", n)
	}

	// 再次归档同一批：全部命中已存在，应只有 3 次批量查询 + 少量归类 Update。
	n = countSQL()
	t.Logf("40 条影片重复归档（全部已存在）SQL 语句数: %d", n)
	if n >= 60 {
		t.Errorf("重复归档发了 %d 条 SQL，疑似仍在逐条查询", n)
	}

	// 校验数据确实建全了（40 个标签 / 40 个地域 / 40 个分类）。
	var tags, areas, cats int64
	r.db.Model(&model.Tag{}).Where("source_id=1").Count(&tags)
	r.db.Model(&model.Area{}).Where("source_id=1").Count(&areas)
	r.db.Model(&model.Category{}).Where("source_id=1").Count(&cats)
	if tags != 40 || areas != 40 || cats != 40 {
		t.Errorf("归档实体数异常: tags=%d areas=%d cats=%d（期望各 40）", tags, areas, cats)
	}
}
