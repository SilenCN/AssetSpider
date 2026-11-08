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

func TestCategoryRepositoryTwoLevel(t *testing.T) {
	r := newTestRepos(t)
	// 一级分类 1（电影）与两个二级分类 6/7，均挂在 1 之下（type_id_1 = 1）。
	ids, err := r.Categories.EnsureForVideos(1, []model.Video{
		{TypeID: "1", TypeName: "电影", TypeID1: ""},
		{TypeID: "6", TypeName: "动作片", TypeID1: "1"},
		{TypeID: "7", TypeName: "喜剧片", TypeID1: "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 {
		t.Fatalf("应创建 3 个分类: %+v", ids)
	}

	parent, err := r.Categories.Get(ids["id:1"])
	if err != nil {
		t.Fatal(err)
	}
	if parent.Level != 1 || parent.ParentID != 0 || parent.TypePID != "" {
		t.Fatalf("一级分类属性异常: %+v", parent)
	}
	child, err := r.Categories.Get(ids["id:6"])
	if err != nil {
		t.Fatal(err)
	}
	if child.Level != 2 || child.ParentID != parent.ID || child.TypePID != "1" {
		t.Fatalf("二级分类未挂到父级: %+v (parent=%d)", child, parent.ID)
	}

	// 父级未在本批出现时：二级分类先记为 level=2、parent_id=0，父级 ID 暂存于 type_pid。
	ids2, err := r.Categories.EnsureForVideos(1, []model.Video{
		{TypeID: "8", TypeName: "悬疑", TypeID1: "2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	orphanID := ids2["id:8"]
	orphan, err := r.Categories.Get(orphanID)
	if err != nil || orphan.Level != 2 || orphan.ParentID != 0 || orphan.TypePID != "2" {
		t.Fatalf("父级缺失时应暂存为二级孤儿: %+v", orphan)
	}
	// 父级 2 出现后，孤儿应被自动挂接。
	if _, err := r.Categories.EnsureForVideos(1, []model.Video{
		{TypeID: "2", TypeName: "电视剧", TypeID1: ""},
	}); err != nil {
		t.Fatal(err)
	}
	orphan, err = r.Categories.Get(orphanID)
	if err != nil {
		t.Fatal(err)
	}
	parentOfOrphan, err := r.Categories.Get(orphan.ParentID)
	if err != nil || parentOfOrphan.TypeID != "2" {
		t.Fatalf("父级出现后孤儿未挂接: orphan=%+v parent=%+v", orphan, parentOfOrphan)
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
