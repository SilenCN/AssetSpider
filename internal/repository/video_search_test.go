package repository

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 影片关键词检索有三条路径：3 字以上走 FTS5 trigram、2 字走 video_bigrams、
// 1 字回退 LIKE。三条路径的结果语义必须一致（都是「六列里任意一列包含该子串」）。
func TestVideoKeywordSearchBothPaths(t *testing.T) {
	r := newTestRepos(t)
	mk := func(vodID, name, actor, class string) model.Video {
		return model.Video{
			SourceID: 1, VodID: vodID, VodName: name, VodActor: actor, VodClass: class,
			Hash: "h-" + vodID, VodTime: "2026-11-09 10:00:00",
		}
	}
	if _, err := r.Videos.UpsertVideos([]model.Video{
		mk("1", "流浪地球", "吴京", "科幻"),
		mk("2", "流浪猫日记", "张三", "剧情"),
		mk("3", "复仇者联盟", "小罗伯特", "动作"),
	}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		keyword string
		want    int64
		path    string
	}{
		{"流浪地球", 1, "FTS（4 字）"},
		{"流浪", 2, "bigram（2 字，trigram 无法命中）"},
		{"联盟", 1, "bigram（2 字）"},
		{"小罗伯特", 1, "FTS（命中演员列）"},
		{"动作", 1, "bigram（命中分类列）"},
		{"不存在的片名", 0, "FTS 无命中"},
	}
	for _, c := range cases {
		_, total, err := listVideosForTest(r.Videos, VideoQuery{Keyword: c.keyword, Page: 1, Size: 20})
		if err != nil {
			t.Fatalf("%s 检索 %q 报错: %v", c.path, c.keyword, err)
		}
		if total != c.want {
			t.Errorf("%s 检索 %q 命中 %d 条，期望 %d 条", c.path, c.keyword, total, c.want)
		}
	}
}

// 关键词里带 FTS5 的语法字符（引号、AND、OR、减号、星号）不能把 MATCH 语句拼坏。
func TestVideoKeywordSearchEscapesFTSSyntax(t *testing.T) {
	r := newTestRepos(t)
	if _, err := r.Videos.UpsertVideos([]model.Video{
		{SourceID: 1, VodID: "1", VodName: `复仇者联盟 "终局"`, Hash: "h1"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, kw := range []string{`复仇者 "终局`, `复仇 AND 联盟`, `联盟 OR 复仇`, `复仇 -联盟`, `联盟*`, `"复仇"`} {
		if _, _, err := listVideosForTest(r.Videos, VideoQuery{Keyword: kw, Page: 1, Size: 20}); err != nil {
			t.Errorf("关键词 %q 不应报错: %v", kw, err)
		}
	}
}

// 全文索引必须随 upsert 同步：改名后旧词应查不到、新词应能查到。
func TestVideoSearchFollowsUpsert(t *testing.T) {
	r := newTestRepos(t)
	up := func(name, hash string) {
		if _, err := r.Videos.UpsertVideos([]model.Video{{
			SourceID: 1, VodID: "9", VodName: name, Hash: hash, VodTime: "2026-11-09 10:00:00",
		}}); err != nil {
			t.Fatal(err)
		}
	}
	search := func(kw string) int64 {
		_, total, err := listVideosForTest(r.Videos, VideoQuery{Keyword: kw, Page: 1, Size: 20})
		if err != nil {
			t.Fatalf("检索 %q 报错: %v", kw, err)
		}
		return total
	}

	up("银河护卫队", "h1")
	if got := search("银河护卫队"); got != 1 {
		t.Fatalf("新增后应能检索到，实际 %d 条", got)
	}
	up("银河护卫队 第二季", "h2")
	if got := search("第二季"); got != 1 {
		t.Fatalf("改名后新词应能检索到，实际 %d 条", got)
	}
	up("星球大战", "h3")
	if got := search("银河护卫队"); got != 0 {
		t.Errorf("改名后旧词不应再命中，实际 %d 条", got)
	}
	if got := search("星球大战"); got != 1 {
		t.Errorf("改名后新词应命中，实际 %d 条", got)
	}
}

// 清空站点数据会删除影片行，全文索引里的对应条目也必须一并消失。
func TestVideoSearchFollowsSourceClear(t *testing.T) {
	r := newTestRepos(t)
	src := &model.Source{Name: "检索站", APIBase: "https://example.com/api.php/provide/vod/", Format: model.FormatMacCMSJSON, Enabled: true}
	if err := r.Sources.CreateSource(src); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Videos.UpsertVideos([]model.Video{{
		SourceID: src.ID, VodID: "1", VodName: "被清空的纪录片", Hash: "h1",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, total, err := listVideosForTest(r.Videos, VideoQuery{Keyword: "被清空的纪录片", Page: 1, Size: 20}); err != nil || total != 1 {
		t.Fatalf("清空前应能检索到: total=%d err=%v", total, err)
	}
	if _, err := r.Sources.ClearSourceData(src.ID); err != nil {
		t.Fatal(err)
	}
	if _, total, err := listVideosForTest(r.Videos, VideoQuery{Keyword: "被清空的纪录片", Page: 1, Size: 20}); err != nil || total != 0 {
		t.Fatalf("清空后不应再有命中: total=%d err=%v", total, err)
	}
}

// 老库升级：库里已有影片、但还没有全文索引时，Open 必须建表并回灌已有数据。
func TestVideoSearchRebuildsExistingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy", "old.db")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Videos.UpsertVideos([]model.Video{{
		SourceID: 1, VodID: "1", VodName: "老库里的经典影片", Hash: "h1",
	}}); err != nil {
		t.Fatal(err)
	}
	r.Close()

	// 模拟升级前的库：索引表与触发器都不存在，数据照旧。
	raw, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"DROP TRIGGER IF EXISTS " + videoSearchTable + "_ai",
		"DROP TRIGGER IF EXISTS " + videoSearchTable + "_ad",
		"DROP TRIGGER IF EXISTS " + videoSearchTable + "_au",
		"DROP TABLE IF EXISTS " + videoSearchTable,
	} {
		if err := raw.Exec(s).Error; err != nil {
			t.Fatal(err)
		}
	}
	if sqlDB, err := raw.DB(); err == nil {
		sqlDB.Close()
	}

	// 重新打开：应建索引并从既有影片回灌，检索立刻可用。
	upgraded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	_, total, err := listVideosForTest(upgraded.Videos, VideoQuery{Keyword: "老库里的经典影片", Page: 1, Size: 20})
	if err != nil || total != 1 {
		t.Fatalf("升级后应能检索到历史数据: total=%d err=%v", total, err)
	}
}

// 列表接口不再返回总数 / hasMore：每页只返回本页数据，前端按「本页条数 >= size」
// 判断还有下一页。这里验证分页边界（满页 → 还有、不满 → 到底、再翻一页为空）。
func TestListVideosPaginationWithoutTotal(t *testing.T) {
	r := newTestRepos(t)
	items := make([]model.Video, 0, 3)
	for i := 1; i <= 3; i++ {
		items = append(items, model.Video{
			SourceID: 1, VodID: fmt.Sprintf("%d", i), VodName: fmt.Sprintf("影片 %d", i),
			Hash: fmt.Sprintf("h%d", i), VodTime: "2026-11-09 10:00:00",
		})
	}
	if _, err := r.Videos.UpsertVideos(items); err != nil {
		t.Fatal(err)
	}
	// 一页就能装下：本页 3 条 < size 20，前端据此判定到底。
	all, err := r.Videos.ListVideos(VideoQuery{Page: 1, Size: 20})
	if err != nil || len(all) != 3 {
		t.Fatalf("首页应返回 3 条: len=%d err=%v", len(all), err)
	}
	// 每页 2 条：第 1 页取满 → 继续翻页。
	first, err := r.Videos.ListVideos(VideoQuery{Page: 1, Size: 2})
	if err != nil || len(first) != 2 {
		t.Fatalf("第 1 页应取满 2 条: len=%d err=%v", len(first), err)
	}
	// 第 2 页不满 → 到底。
	second, err := r.Videos.ListVideos(VideoQuery{Page: 2, Size: 2})
	if err != nil || len(second) != 1 {
		t.Fatalf("第 2 页应剩 1 条: len=%d err=%v", len(second), err)
	}
	// 数据刚好整页时，前端会多发一次请求，拿到空页即停。
	four := model.Video{SourceID: 1, VodID: "4", VodName: "影片 4", Hash: "h4", VodTime: "2026-11-09 10:00:00"}
	if _, err := r.Videos.UpsertVideos([]model.Video{four}); err != nil {
		t.Fatal(err)
	}
	third, err := r.Videos.ListVideos(VideoQuery{Page: 3, Size: 2})
	if err != nil || len(third) != 0 {
		t.Fatalf("超出末页应返回空: len=%d err=%v", len(third), err)
	}
}

// 1 字 / 含通配符的关键词即使 FTS 可用也直接扫主表六列 LIKE，
// 不再绕道 FTS5 外部内容表（实测更慢）。
func TestShortKeywordFallsBackToMainTable(t *testing.T) {
	r := newTestRepos(t)
	for _, kw := range []string{"流", "流%"} {
		dry := applyVideoKeyword(r.db.Model(&model.Video{}), kw,
			videoSearchCaps{fts: true, bigram: true}).
			Session(&gorm.Session{DryRun: true}).Find(&[]model.Video{}).Statement
		sql := dry.SQL.String()
		if strings.Contains(sql, videoSearchTable) || strings.Contains(sql, videoBigramTable) {
			t.Errorf("短词 %q 回退不应引用索引表: %s", kw, sql)
		}
		if !strings.Contains(sql, "LIKE") {
			t.Errorf("短词 %q 应回退 LIKE: %s", kw, sql)
		}
	}
}
