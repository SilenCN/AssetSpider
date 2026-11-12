package repository

import (
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
)

// TestParseAreas 覆盖地域的多值写法：多数站点只有一个地域，但要能处理「美国/英国」。
func TestParseAreas(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{"美国", []string{"美国"}},
		{"美国/英国", []string{"美国", "英国"}},
		{"大陆，香港", []string{"大陆", "香港"}},
		{"美国、美国", []string{"美国"}}, // 去重
		{"", nil},
		{"   ", nil},
	}
	for _, c := range cases {
		got := ParseAreas(c.raw)
		if len(got) != len(c.want) {
			t.Fatalf("ParseAreas(%q) = %#v, want %#v", c.raw, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("ParseAreas(%q)[%d] = %q, want %q", c.raw, i, got[i], c.want[i])
			}
		}
	}
}

// TestAreaEnsureAndLink 验证地域的「创建 → 关联 → 列表 / 计数」全链路
// （资源库地域筛选维度已移除，归档数据仍保留）。
func TestAreaEnsureAndLink(t *testing.T) {
	r := newTestRepos(t)
	src := model.Source{Name: "站点A", APIBase: "http://a.example/api.php/provide/vod/"}
	if err := r.Sources.CreateSource(&src); err != nil {
		t.Fatal(err)
	}

	videos := []model.Video{
		{SourceID: src.ID, VodID: "a", VodName: "片A", VodArea: "美国"},
		{SourceID: src.ID, VodID: "b", VodName: "片B", VodArea: "美国/英国"},
		{SourceID: src.ID, VodID: "c", VodName: "片C", VodArea: ""}, // 无地域
	}
	areaIDsByVod, err := r.Areas.EnsureForVideos(src.ID, videos)
	if err != nil {
		t.Fatal(err)
	}
	if len(areaIDsByVod["a"]) != 1 || len(areaIDsByVod["b"]) != 2 {
		t.Fatalf("地域映射异常: %+v", areaIDsByVod)
	}
	if _, ok := areaIDsByVod["c"]; ok {
		t.Fatalf("无地域影片不应有映射: %+v", areaIDsByVod)
	}

	if _, err := r.Videos.UpsertVideos(videos); err != nil {
		t.Fatal(err)
	}
	idByVod, err := r.Videos.MapVodIDs(src.ID, []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Areas.LinkVideos(idByVod, areaIDsByVod); err != nil {
		t.Fatal(err)
	}

	// 列表：站点应有 2 个地域（美国、英国），美国被 a、b 共用。
	areas, err := r.Areas.List(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(areas) != 2 {
		t.Fatalf("站点应有 2 个地域，实际 %d: %+v", len(areas), areas)
	}
	var usa model.Area
	for _, a := range areas {
		if a.Name == "美国" {
			usa = a
		}
	}
	if usa.ID == 0 {
		t.Fatalf("未找到「美国」地域: %+v", areas)
	}
	counts, err := r.Areas.CountVideosByArea()
	if err != nil {
		t.Fatal(err)
	}
	if counts[usa.ID] != 2 {
		t.Fatalf("「美国」影片数应为 2，实际 %d（%+v）", counts[usa.ID], counts)
	}

	// 资源库已移除地域筛选维度：归档数据（地域、关联、影片数）仍保留并可用，
	// 但列表不再提供 area_id 过滤，这里只复核归档计数的一致性。
	if got := counts[usa.ID]; got != 2 {
		t.Fatalf("「美国」归档影片数应为 2，实际 %d", got)
	}

	// 幂等：重复 Ensure + Link 不重复建地域、不重复关联。
	again, err := r.Areas.EnsureForVideos(src.ID, videos)
	if err != nil {
		t.Fatal(err)
	}
	if again["a"][0] != areaIDsByVod["a"][0] {
		t.Fatalf("重复 Ensure 应复用既有地域: %+v vs %+v", again, areaIDsByVod)
	}
	if err := r.Areas.LinkVideos(idByVod, again); err != nil {
		t.Fatal(err)
	}
	counts2, _ := r.Areas.CountVideosByArea()
	if counts2[usa.ID] != 2 {
		t.Fatalf("重复关联后「美国」影片数应仍为 2，实际 %d", counts2[usa.ID])
	}
}

// TestAreaPerSource 验证地域按站点隔离：不同站点同名地域互不干扰。
func TestAreaPerSource(t *testing.T) {
	r := newTestRepos(t)
	a := model.Source{Name: "站点A", APIBase: "http://a.example/api.php/provide/vod/"}
	b := model.Source{Name: "站点B", APIBase: "http://b.example/api.php/provide/vod/"}
	if err := r.Sources.CreateSource(&a); err != nil {
		t.Fatal(err)
	}
	if err := r.Sources.CreateSource(&b); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Areas.EnsureForVideos(a.ID, []model.Video{{SourceID: a.ID, VodID: "1", VodArea: "美国"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Areas.EnsureForVideos(b.ID, []model.Video{{SourceID: b.ID, VodID: "1", VodArea: "美国"}}); err != nil {
		t.Fatal(err)
	}

	la, _ := r.Areas.List(a.ID)
	lb, _ := r.Areas.List(b.ID)
	if len(la) != 1 || len(lb) != 1 {
		t.Fatalf("每个站点应各有 1 个「美国」地域: A=%+v B=%+v", la, lb)
	}
	if la[0].ID == lb[0].ID {
		t.Fatalf("不同站点的同名地域不应是同一实体: A=%d B=%d", la[0].ID, lb[0].ID)
	}
}

// TestBackfillAreas 验证老库升级：videos.vod_area 已有文本但 video_areas 为空时，
// 启动迁移能把地域与关联补建出来（否则前端下拉会是空的，直到重新采集）。
func TestBackfillAreas(t *testing.T) {
	r := newTestRepos(t)
	src := model.Source{Name: "老站点", APIBase: "http://old.example/api.php/provide/vod/"}
	if err := r.Sources.CreateSource(&src); err != nil {
		t.Fatal(err)
	}
	// 模拟改造前的数据：只有 vod_area 字符串，没有任何地域关联。
	if _, err := r.Videos.UpsertVideos([]model.Video{
		{SourceID: src.ID, VodID: "1", VodName: "片1", VodArea: "美国"},
		{SourceID: src.ID, VodID: "2", VodName: "片2", VodArea: "中国大陆"},
		{SourceID: src.ID, VodID: "3", VodName: "片3", VodArea: ""},
	}); err != nil {
		t.Fatal(err)
	}

	if err := backfillAreas(r.db); err != nil {
		t.Fatal(err)
	}

	areas, err := r.Areas.List(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(areas) != 2 {
		t.Fatalf("回灌后应有 2 个地域，实际 %d: %+v", len(areas), areas)
	}
	counts, err := r.Areas.CountVideosByArea()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range areas {
		if counts[a.ID] != 1 {
			t.Fatalf("地域 %s 的影片数应为 1，实际 %d", a.Name, counts[a.ID])
		}
	}

	// 重复执行不应重复回灌（函数内部以 video_areas 非空为跳过条件）。
	if err := backfillAreas(r.db); err != nil {
		t.Fatal(err)
	}
	var links int64
	if err := r.db.Model(&model.VideoArea{}).Count(&links).Error; err != nil {
		t.Fatal(err)
	}
	if links != 2 {
		t.Fatalf("重复回灌后关联数应仍为 2，实际 %d", links)
	}
}
