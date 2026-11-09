package repository

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 影片关键词检索有两条路径：3 个字符以上走 FTS5 全文索引，1~2 个字符回退 LIKE。
// 两条路径的结果语义必须一致（都是「六列里任意一列包含该子串」）。
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
		{"流浪", 2, "LIKE（2 字，trigram 无法命中）"},
		{"联盟", 1, "LIKE（2 字）"},
		{"小罗伯特", 1, "FTS（命中演员列）"},
		{"动作", 1, "LIKE（命中分类列）"},
		{"不存在的片名", 0, "FTS 无命中"},
	}
	for _, c := range cases {
		_, total, err := r.Videos.ListVideos(VideoQuery{Keyword: c.keyword, Page: 1, Size: 20})
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
		if _, _, err := r.Videos.ListVideos(VideoQuery{Keyword: kw, Page: 1, Size: 20}); err != nil {
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
		_, total, err := r.Videos.ListVideos(VideoQuery{Keyword: kw, Page: 1, Size: 20})
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
	if _, total, err := r.Videos.ListVideos(VideoQuery{Keyword: "被清空的纪录片", Page: 1, Size: 20}); err != nil || total != 1 {
		t.Fatalf("清空前应能检索到: total=%d err=%v", total, err)
	}
	if _, err := r.Sources.ClearSourceData(src.ID); err != nil {
		t.Fatal(err)
	}
	if _, total, err := r.Videos.ListVideos(VideoQuery{Keyword: "被清空的纪录片", Page: 1, Size: 20}); err != nil || total != 0 {
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
	_, total, err := upgraded.Videos.ListVideos(VideoQuery{Keyword: "老库里的经典影片", Page: 1, Size: 20})
	if err != nil || total != 1 {
		t.Fatalf("升级后应能检索到历史数据: total=%d err=%v", total, err)
	}
}

// 分页次数的优化：第一页没取满时直接返回本页条数，不再额外 Count 一次。
func TestListVideosShortFirstPageSkipsCount(t *testing.T) {
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
	list, total, err := r.Videos.ListVideos(VideoQuery{Page: 1, Size: 20})
	if err != nil || total != 3 || len(list) != 3 {
		t.Fatalf("首页不足一页时 total 应等于实际条数: total=%d len=%d err=%v", total, len(list), err)
	}
	// 取满一页时仍然要给出精确总数。
	list, total, err = r.Videos.ListVideos(VideoQuery{Page: 1, Size: 2})
	if err != nil || total != 3 || len(list) != 2 {
		t.Fatalf("取满一页时 total 应为 3: total=%d len=%d err=%v", total, len(list), err)
	}
	// 第二页即使取不满，total 也必须是全量条数而不是本页条数。
	list, total, err = r.Videos.ListVideos(VideoQuery{Page: 2, Size: 2})
	if err != nil || total != 3 || len(list) != 1 {
		t.Fatalf("第二页 total 应为 3: total=%d len=%d err=%v", total, len(list), err)
	}
}
