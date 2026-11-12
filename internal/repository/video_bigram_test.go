package repository

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// waitVideoBigramReady 等待后台回灌完成。历史数据回灌改成启动后的异步任务，
// Open 返回不再等于回灌结束；完成前 2 字检索降级 LIKE，结果仍正确。
func waitVideoBigramReady(t *testing.T, db *gorm.DB) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if videoBigramReady(db) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("后台两字索引回灌在 15 秒内未完成")
}

// seedBigramVideos 让六列检索字段各有可验证的两字片段。
func seedBigramVideos(t *testing.T, r *Repositories) {
	t.Helper()
	if _, err := r.Videos.UpsertVideos([]model.Video{
		{SourceID: 1, VodID: "b1", VodName: "流浪地球", VodEn: "The Wandering Earth",
			VodActor: "吴京", VodDirector: "郭帆", VodClass: "科幻", VodArea: "大陆", Hash: "h1"},
		{SourceID: 1, VodID: "b2", VodName: "流浪猫日记", VodActor: "张三",
			VodClass: "剧情", VodArea: "欧美", Hash: "h2"},
		{SourceID: 1, VodID: "b3", VodName: "ABcd 影片", VodEn: "abcdef",
			VodClass: "喜剧", Hash: "h3"},
	}); err != nil {
		t.Fatal(err)
	}
}

func countBigramKeyword(t *testing.T, repo VideoRepository, kw string) int64 {
	t.Helper()
	_, total, err := listVideosForTest(repo, VideoQuery{Keyword: kw, Page: 1, Size: 20})
	if err != nil {
		t.Fatalf("检索 %q 报错: %v", kw, err)
	}
	return total
}

// 2 字关键词必须走 video_bigrams，且六列、大小写、1 字 / 3 字路径结果都正确。
func TestVideoBigramSearchTwoRunes(t *testing.T) {
	r := newTestRepos(t)
	seedBigramVideos(t, r)

	cases := []struct {
		kw   string
		want int64
		path string
	}{
		{"流浪", 2, "bigram（片名）"},
		{"科幻", 1, "bigram（分类列）"},
		{"吴京", 1, "bigram（演员列）"},
		{"欧美", 1, "bigram（地域列）"},
		{"郭帆", 1, "bigram（导演列）"},
		{"ab", 1, "bigram（英文列，小写查询）"},
		{"AB", 1, "bigram（英文列，大写查询走 LOWER）"},
		{"abcd", 1, "FTS（4 字）"},
		{"流浪地球", 1, "FTS（4 字）"},
		{"不存在", 0, "FTS 无命中"},
		{"流", 2, "LIKE（1 字回退）"},
		{"流%", 2, "LIKE（2 字含通配符，保持原语义）"},
	}
	for _, c := range cases {
		if got := countBigramKeyword(t, r.Videos, c.kw); got != c.want {
			t.Errorf("%s 检索 %q 命中 %d 条，期望 %d 条", c.path, c.kw, got, c.want)
		}
	}

	// 两字查询生成的 SQL 必须真的带上倒排表，而不是悄悄退回 LIKE。
	dry := applyVideoKeyword(r.db.Model(&model.Video{}), "流浪",
		videoSearchCaps{fts: true, bigram: true}).
		Session(&gorm.Session{DryRun: true}).Find(&[]model.Video{}).Statement
	if !strings.Contains(dry.SQL.String(), videoBigramTable) {
		t.Errorf("2 字检索未走 %s，实际 SQL: %s", videoBigramTable, dry.SQL.String())
	}
	// 倒排表按主键 gram 命中，不是全表扫描。
	var plan []struct {
		Detail string `gorm:"column:detail"`
	}
	if err := r.db.Raw("EXPLAIN QUERY PLAN SELECT video_id FROM "+videoBigramTable+" WHERE gram = LOWER(?)", "流浪").
		Scan(&plan).Error; err != nil {
		t.Fatalf("读取查询计划失败: %v", err)
	}
	joined := ""
	for _, row := range plan {
		joined += row.Detail + " "
	}
	if !strings.Contains(joined, "PRIMARY KEY") {
		t.Errorf("bigram 查询未命中主键索引: %s", joined)
	}
}

// 触发器必须随 upsert / 删除同步：改名后旧两字词失效、新词生效，删行后倒排表无残留。
func TestVideoBigramFollowsUpsertAndDelete(t *testing.T) {
	r := newTestRepos(t)
	up := func(name, hash string) {
		t.Helper()
		if _, err := r.Videos.UpsertVideos([]model.Video{{
			SourceID: 1, VodID: "x1", VodName: name, Hash: hash, VodTime: "2026-11-12 10:00:00",
		}}); err != nil {
			t.Fatal(err)
		}
	}
	up("银河护卫队", "h1")
	if got := countBigramKeyword(t, r.Videos, "银河"); got != 1 {
		t.Fatalf("新增后 2 字检索应命中，实际 %d", got)
	}
	up("星球大战", "h2")
	if got := countBigramKeyword(t, r.Videos, "银河"); got != 0 {
		t.Errorf("改名后旧词不应命中，实际 %d", got)
	}
	if got := countBigramKeyword(t, r.Videos, "星球"); got != 1 {
		t.Errorf("改名后新词应命中，实际 %d", got)
	}

	// 只刷 last_seen_at 不碰检索文本：不应把倒排表删空。
	var v model.Video
	if err := r.db.Where("vod_id = ?", "x1").First(&v).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.db.Model(&model.Video{}).Where("id = ?", v.ID).
		Update("last_seen_at", time.Now()).Error; err != nil {
		t.Fatal(err)
	}
	if got := countBigramKeyword(t, r.Videos, "星球"); got != 1 {
		t.Errorf("更新非检索列后 2 字检索不应失效，实际 %d", got)
	}
	// UPDATE 触发器限定在六列，避免每次刷新时间都重算 bigram。
	var triggerSQL string
	if err := r.db.Raw("SELECT sql FROM sqlite_master WHERE type='trigger' AND name=?",
		videoBigramTable+"_au").Scan(&triggerSQL).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(triggerSQL, "UPDATE OF") {
		t.Errorf("更新触发器应带 UPDATE OF 限定列，实际: %s", triggerSQL)
	}

	if err := r.db.Where("id = ?", v.ID).Delete(&model.Video{}).Error; err != nil {
		t.Fatal(err)
	}
	var left int64
	if err := r.db.Raw("SELECT count(*) FROM "+videoBigramTable+" WHERE video_id = ?", v.ID).
		Scan(&left).Error; err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("删除影片后倒排表残留 %d 行", left)
	}
	if got := countBigramKeyword(t, r.Videos, "星球"); got != 0 {
		t.Errorf("删除影片后 2 字检索不应命中，实际 %d", got)
	}
}

// 历史库升级：库里已有影片、但没有 bigram 表时，Open 必须重建并回灌历史数据。
func TestVideoBigramBackfillsExistingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy", "old.db")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Videos.UpsertVideos([]model.Video{{
		SourceID: 1, VodID: "1", VodName: "老库里的经典影片", VodClass: "科幻", Hash: "h1",
	}}); err != nil {
		t.Fatal(err)
	}
	r.Close()

	raw, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"DROP TRIGGER IF EXISTS " + videoBigramTable + "_ai",
		"DROP TRIGGER IF EXISTS " + videoBigramTable + "_ad",
		"DROP TRIGGER IF EXISTS " + videoBigramTable + "_au",
		"DROP TABLE IF EXISTS " + videoBigramTable,
		"DROP TABLE IF EXISTS " + videoBigramStateTable,
	} {
		if err := raw.Exec(s).Error; err != nil {
			t.Fatal(err)
		}
	}
	if sqlDB, err := raw.DB(); err == nil {
		sqlDB.Close()
	}

	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if upgraded.maintenance == nil {
		t.Fatal("有历史数据时应启动后台维护任务，而不是在 Open 里同步跑")
	}
	waitVideoBigramReady(t, upgraded.db)
	if got := countBigramKeyword(t, upgraded.Videos, "经典"); got != 1 {
		t.Errorf("升级后历史数据的 2 字检索应命中，实际 %d 条", got)
	}
	if got := countBigramKeyword(t, upgraded.Videos, "科幻"); got != 1 {
		t.Errorf("升级后历史分类列的 2 字检索应命中，实际 %d 条", got)
	}
}

// 回灌未完成 / 索引不可用时降级为 LIKE，结果必须仍然正确。
func TestVideoBigramFallsBackToLike(t *testing.T) {
	r := newTestRepos(t)
	seedBigramVideos(t, r)
	if err := r.db.Exec("DELETE FROM " + videoBigramStateTable + " WHERE k = 'done'").Error; err != nil {
		t.Fatal(err)
	}
	degraded := NewVideoRepository(r.db)
	if got := countBigramKeyword(t, degraded, "流浪"); got != 2 {
		t.Errorf("bigram 未就绪时降级检索应命中 2 条，实际 %d 条", got)
	}
	if got := countBigramKeyword(t, degraded, "流"); got != 2 {
		t.Errorf("1 字 LIKE 回退应命中 2 条，实际 %d 条", got)
	}
	// 连 FTS 都不可用时，退回主表六列 LIKE，SQL 里不应出现任何索引表。
	dry := applyVideoKeyword(r.db.Model(&model.Video{}), "流浪", videoSearchCaps{}).
		Session(&gorm.Session{DryRun: true}).Find(&[]model.Video{}).Statement
	sql := dry.SQL.String()
	if strings.Contains(sql, videoBigramTable) || strings.Contains(sql, videoSearchTable) {
		t.Errorf("无索引时不应引用索引表，实际 SQL: %s", sql)
	}
	if !strings.Contains(sql, "LIKE") {
		t.Errorf("无索引时应回退 LIKE，实际 SQL: %s", sql)
	}
}

// 废弃索引（两个闲置单列索引 + 两个评分排序表达式索引）在升级后的库里不应再存在，
// AutoMigrate 也不该把它们建回来。评分排序移除后 DROP 可省空间与写入维护成本。
func TestRetiredVideoIndexesAreDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.AutoMigrate(&model.Video{}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"CREATE INDEX IF NOT EXISTS idx_videos_vod_name ON videos(vod_name)",
		"CREATE INDEX IF NOT EXISTS idx_videos_hash ON videos(hash)",
		"CREATE INDEX IF NOT EXISTS idx_videos_score_desc ON videos(CAST(NULLIF(TRIM(vod_score), '') AS REAL) DESC)",
		"CREATE INDEX IF NOT EXISTS idx_videos_score_asc ON videos(CAST(NULLIF(TRIM(vod_score), '') AS REAL) ASC)",
	} {
		if err := raw.Exec(s).Error; err != nil {
			t.Fatal(err)
		}
	}
	if sqlDB, err := raw.DB(); err == nil {
		sqlDB.Close()
	}

	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, name := range []string{
		"idx_videos_vod_name", "idx_videos_hash",
		"idx_videos_score_desc", "idx_videos_score_asc",
	} {
		var n int64
		if err := r.db.Raw("SELECT count(*) FROM sqlite_master WHERE type='index' AND name = ?", name).
			Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("废弃索引 %s 仍存在", name)
		}
	}
}

// 后台回灌被中止时不标记完成，下次从进度续跑；完成后新建仓储立即走倒排表。
func TestVideoBigramBackfillStopsAndResumes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.AutoMigrate(&model.Video{}); err != nil {
		t.Fatal(err)
	}
	// 在建倒排表之前插入数据，模拟「没有 bigram 索引的历史库」。
	for i := 1; i <= 6; i++ {
		if err := raw.Exec("INSERT INTO videos(id, source_id, vod_id, vod_name) VALUES(?, 1, ?, ?)",
			i, fmt.Sprintf("v%d", i), fmt.Sprintf("经典影片%d", i)).Error; err != nil {
			t.Fatal(err)
		}
	}
	if sqlDB, err := raw.DB(); err == nil {
		sqlDB.Close()
	}

	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	}()
	if err := initVideoBigrams(db); err != nil {
		t.Fatal(err)
	}

	oldBatch := videoBigramBatch
	videoBigramBatch = 2
	t.Cleanup(func() { videoBigramBatch = oldBatch })

	calls := 0
	err = backfillVideoBigrams(db, func() bool { calls++; return calls > 1 })
	if !errors.Is(err, errBigramStopped) {
		t.Fatalf("中止应返回 errBigramStopped，实际 %v", err)
	}
	if videoBigramReady(db) {
		t.Fatal("中止后不应标记回灌完成，否则未回灌的历史数据会查不到")
	}
	if videoBigramState(db, "cursor") == 0 {
		t.Fatal("中止前应已提交至少一批进度")
	}

	if err := backfillVideoBigrams(db, nil); err != nil {
		t.Fatal(err)
	}
	if !videoBigramReady(db) {
		t.Fatal("续跑完成后应标记 done")
	}
	// 能力探测是动态的：完成后新建的仓储立刻切到倒排表，不需要重启。
	if got := countBigramKeyword(t, NewVideoRepository(db), "经典"); got != 6 {
		t.Errorf("续跑后 2 字检索应命中 6 条，实际 %d", got)
	}
}

// 回灌状态从 0 变 1 后，已构造的仓储也应立刻切换，不缓存旧的降级结论。
func TestVideoBigramReadinessIsDynamic(t *testing.T) {
	r := newTestRepos(t)
	seedBigramVideos(t, r)
	repo := NewVideoRepository(r.db)
	if err := r.db.Exec("DELETE FROM " + videoBigramStateTable + " WHERE k = 'done'").Error; err != nil {
		t.Fatal(err)
	}
	if got := countBigramKeyword(t, repo, "流浪"); got != 2 {
		t.Fatalf("done 被移除后应降级 LIKE 且结果不变，实际 %d", got)
	}
	if err := r.db.Exec("INSERT OR REPLACE INTO " + videoBigramStateTable + "(k, v) VALUES('done', 1)").Error; err != nil {
		t.Fatal(err)
	}
	if got := countBigramKeyword(t, repo, "流浪"); got != 2 {
		t.Fatalf("done 恢复后动态探测应切回索引，结果仍为 2 条，实际 %d", got)
	}
	dry := applyVideoKeyword(r.db.Model(&model.Video{}), "流浪",
		videoSearchCaps{fts: true, bigram: repo.(*videoRepository).bigram()}).
		Session(&gorm.Session{DryRun: true}).Find(&[]model.Video{}).Statement
	if !strings.Contains(dry.SQL.String(), videoBigramTable) {
		t.Errorf("状态恢复后未走倒排表: %s", dry.SQL.String())
	}
}
