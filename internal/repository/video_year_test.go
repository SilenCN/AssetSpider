package repository

import (
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
)

// 年份筛选是影片上的单值属性（不等同于标签 / 地域的多对多拆分）：
// 列表按 vod_year 等值过滤，下拉选项由 GROUP BY 聚合得到。
func TestVideoYearFilterAndFacet(t *testing.T) {
	r := newTestRepos(t)
	mk := func(sourceID uint, vodID, name, year string) model.Video {
		return model.Video{
			SourceID: sourceID, VodID: vodID, VodName: name, VodYear: year,
			Hash: "h-" + vodID, VodTime: "2026-11-09 10:00:00",
		}
	}
	if _, err := r.Videos.UpsertVideos([]model.Video{
		mk(1, "1", "旧片", "2024"),
		mk(1, "2", "新片甲", "2025"),
		mk(1, "3", "新片乙", "2025"),
		mk(2, "4", "别站新片", "2025"),
		mk(1, "5", "没年份", ""),
	}); err != nil {
		t.Fatal(err)
	}

	// 等值筛选：只命中该年份
	items, total, err := listVideosForTest(r.Videos, VideoQuery{Year: "2025", Page: 1, Size: 20})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(items) != 3 {
		t.Errorf("按年份筛选应命中 3 条，实际 total=%d len=%d", total, len(items))
	}
	for _, v := range items {
		if v.VodYear != "2025" {
			t.Errorf("筛选结果混入其它年份: %+v", v.VodYear)
		}
	}

	// 年份 + 站点组合筛选
	if _, total, err = listVideosForTest(r.Videos, VideoQuery{Year: "2025", SourceID: 1, Page: 1, Size: 20}); err != nil {
		t.Fatal(err)
	} else if total != 2 {
		t.Errorf("年份 + 站点组合应命中 2 条，实际 %d", total)
	}

	// 下拉选项：年份倒序、带影片数、空年份不出现
	years, err := r.Videos.ListVideoYears(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(years) != 2 {
		t.Fatalf("应只有 2 个年份（空年份不进下拉），实际 %+v", years)
	}
	if years[0].Year != "2025" || years[0].Count != 3 {
		t.Errorf("首个年份应为 2025（3 条），实际 %+v", years[0])
	}
	if years[1].Year != "2024" || years[1].Count != 1 {
		t.Errorf("第二个年份应为 2024（1 条），实际 %+v", years[1])
	}

	// 按站点统计：站点 1 只有 2 条 2025
	if years, err = r.Videos.ListVideoYears(1); err != nil {
		t.Fatal(err)
	} else if len(years) != 2 || years[0].Year != "2025" || years[0].Count != 2 {
		t.Errorf("站点 1 的年份统计异常: %+v", years)
	}

	// 可见性：只可见站点 1 时，2025 的影片数应排除站点 2 的那条
	if years, err = r.Videos.ListVideoYears(0, []uint{1}); err != nil {
		t.Fatal(err)
	} else if len(years) == 0 || years[0].Year != "2025" || years[0].Count != 2 {
		t.Errorf("可见性过滤后的年份统计异常: %+v", years)
	}

	// 可见站点为空切片表示「什么都看不到」，下拉也应为空
	if years, err = r.Videos.ListVideoYears(0, []uint{}); err != nil {
		t.Fatal(err)
	} else if len(years) != 0 {
		t.Errorf("无可见站点时应无年份选项，实际 %+v", years)
	}
}
