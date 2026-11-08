package repository

import (
	"errors"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
)

func TestCategoryRepositoryEnsurePerSource(t *testing.T) {
	r := newTestRepos(t)
	videos := []model.Video{
		{SourceID: 1, VodID: "a", TypeID: "6", TypeName: "动作片"},
		{SourceID: 1, VodID: "b", TypeID: "6", TypeName: "动作片"}, // 同批重复分类应合并
		{SourceID: 1, VodID: "c", TypeID: "7", TypeName: "喜剧片"},
		{SourceID: 1, VodID: "d", TypeID: "", TypeName: "无ID分类"}, // 回退用名称
		{SourceID: 1, VodID: "e", TypeID: "", TypeName: ""},      // 无分类信息，跳过
	}
	ids, err := r.Categories.EnsureForVideos(1, videos)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids["id:6"] == 0 || ids["id:7"] == 0 || ids["name:无ID分类"] == 0 {
		t.Fatalf("分类键映射异常: %+v", ids)
	}

	// 幂等：同一批再来一次不新增记录
	again, err := r.Categories.EnsureForVideos(1, videos)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 3 || again["id:6"] != ids["id:6"] {
		t.Fatalf("分类创建不是幂等的: %+v", again)
	}
	cats, err := r.Categories.List(1)
	if err != nil || len(cats) != 3 {
		t.Fatalf("站点 1 应有 3 个分类: %v %+v", err, cats)
	}

	// 每个资源站一套：另一站点用相同 type_id 也必须独立
	other, err := r.Categories.EnsureForVideos(2, []model.Video{
		{SourceID: 2, VodID: "x", TypeID: "6", TypeName: "动作片"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if other["id:6"] == 0 || other["id:6"] == ids["id:6"] {
		t.Fatalf("不同站点的同名分类被合并了: %+v vs %+v", other, ids)
	}
	all, err := r.Categories.List(0)
	if err != nil || len(all) != 4 {
		t.Fatalf("全部分类应为 4 个: %v %d", err, len(all))
	}
	only2, err := r.Categories.List(2)
	if err != nil || len(only2) != 1 || only2[0].SourceID != 2 {
		t.Fatalf("按站点过滤分类异常: %v %+v", err, only2)
	}

	// 同一 type_id 改名时更新名称，不新建记录
	if _, err := r.Categories.EnsureForVideos(1, []model.Video{
		{TypeID: "6", TypeName: "动作片(改名)"},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := r.Categories.Get(ids["id:6"])
	if err != nil || got.Name != "动作片(改名)" {
		t.Fatalf("分类名未同步: %v %+v", err, got)
	}
	all, _ = r.Categories.List(0)
	if len(all) != 4 {
		t.Fatalf("改名不应新增分类，实际 %d 个", len(all))
	}
	if _, err := r.Categories.Get(999999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的分类应返回 ErrNotFound，实际 %v", err)
	}
}

func TestCategoryVideoCountAndFilter(t *testing.T) {
	r := newTestRepos(t)
	ids, err := r.Categories.EnsureForVideos(1, []model.Video{
		{TypeID: "6", TypeName: "动作片"},
		{TypeID: "7", TypeName: "喜剧片"},
	})
	if err != nil {
		t.Fatal(err)
	}
	action, comedy := ids["id:6"], ids["id:7"]
	_, err = r.Videos.UpsertVideos([]model.Video{
		{SourceID: 1, VodID: "a", Hash: "h1", CategoryID: action},
		{SourceID: 1, VodID: "b", Hash: "h2", CategoryID: action},
		{SourceID: 1, VodID: "c", Hash: "h3", CategoryID: comedy},
		{SourceID: 1, VodID: "d", Hash: "h4"}, // 未归类
	})
	if err != nil {
		t.Fatal(err)
	}

	counts, err := r.Videos.CountVideosByCategory()
	if err != nil || counts[action] != 2 || counts[comedy] != 1 {
		t.Fatalf("分类影片数统计异常: %v %+v", err, counts)
	}
	items, total, err := r.Videos.ListVideos(VideoQuery{CategoryID: action, Page: 1, Size: 20})
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatalf("按分类筛选影片异常: %v total=%d items=%d", err, total, len(items))
	}
	// 站点 + 分类组合过滤
	_, total, err = r.Videos.ListVideos(VideoQuery{SourceID: 2, CategoryID: action, Page: 1, Size: 20})
	if err != nil || total != 0 {
		t.Fatalf("组合过滤异常: %v total=%d", err, total)
	}
}
