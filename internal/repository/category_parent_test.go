package repository

import (
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
)

// categoryByTypeID 按 type_id 取站点下的分类，供分类树相关断言定位节点。
func categoryByTypeID(t *testing.T, r *Repositories, sourceID uint, typeID string) model.Category {
	t.Helper()
	var c model.Category
	if err := r.db.Where("source_id = ? AND type_id = ?", sourceID, typeID).
		Order("id ASC").Limit(1).Find(&c).Error; err != nil {
		t.Fatal(err)
	}
	if c.ID == 0 {
		t.Fatalf("站点 %d 下未找到 type_id=%s 的分类", sourceID, typeID)
	}
	return c
}

// TestApplyTypeTree 验证 videolist 不带 type_id_1 的站点，也能靠 typelist 的
// type_pid 建出带真实名称的一级分类，并把二级分类挂上去。
func TestApplyTypeTree(t *testing.T) {
	r := newTestRepos(t)
	cats := []model.Category{
		{SourceID: 2, Key: "id:6", TypeID: "6", Name: "动作片", Level: 1},
		{SourceID: 2, Key: "id:7", TypeID: "7", Name: "喜剧片", Level: 1},
		{SourceID: 2, Key: "id:26", TypeID: "26", Name: "国产剧", Level: 1},
	}
	for _, c := range cats {
		if err := r.db.Create(&c).Error; err != nil {
			t.Fatal(err)
		}
	}
	// 影片完全没有 type_id_1，层级只存在于 typelist。
	if err := r.db.Create(&model.Video{VodID: "t1", SourceID: 2, TypeID: "6", TypeName: "动作片"}).Error; err != nil {
		t.Fatal(err)
	}
	defs := []CategoryDef{
		{TypeID: "1", Name: "电影", ParentTypeID: "0"},
		{TypeID: "6", Name: "动作片", ParentTypeID: "1"},
		{TypeID: "7", Name: "喜剧片", ParentTypeID: "1"},
		{TypeID: "8", Name: "电视剧", ParentTypeID: "0"},
		{TypeID: "26", Name: "国产剧", ParentTypeID: "8"},
	}
	if err := r.Categories.ApplyTypeTree(2, defs); err != nil {
		t.Fatal(err)
	}
	movie := categoryByTypeID(t, r, 2, "1")
	tv := categoryByTypeID(t, r, 2, "8")
	if movie.Name != "电影" || tv.Name != "电视剧" {
		t.Fatalf("typelist 的权威名称未用于补建父级: %q / %q", movie.Name, tv.Name)
	}
	if movie.Level != 1 || tv.Level != 1 {
		t.Fatalf("补建的父级应为一级: %+v / %+v", movie, tv)
	}
	for _, tc := range []struct{ child, parent string }{{"6", "1"}, {"7", "1"}, {"26", "8"}} {
		ch := categoryByTypeID(t, r, 2, tc.child)
		p := categoryByTypeID(t, r, 2, tc.parent)
		if ch.ParentID != p.ID || ch.Level != 2 {
			t.Fatalf("分类 %s 未按 typelist 挂到 %s: %+v", tc.child, tc.parent, ch)
		}
	}
	// 幂等：defs 为空（接口不可用）时也只按库内数据重建，不会破坏已修好的树。
	if err := r.Categories.ApplyTypeTree(2, nil); err != nil {
		t.Fatal(err)
	}
	if ch := categoryByTypeID(t, r, 2, "26"); ch.ParentID != tv.ID {
		t.Fatalf("重复重建后父子关系被破坏: %+v", ch)
	}
}

// TestApplyTypeTreeCreatesUnreferencedTopLevel 覆盖「一级分类下既没有已采影片、
// 也没有已采到的子分类」的情形：站点 typelist 明确声明的一级分类必须直接建出来，
// 否则分类页只显示碰巧采到影片的那几个分类，用户会以为站点分类不全。
func TestApplyTypeTreeCreatesUnreferencedTopLevel(t *testing.T) {
	r := newTestRepos(t)
	videos := []model.Video{{VodID: "v1", SourceID: 5, TypeID: "29", TypeName: "欧美剧"}}
	if _, err := r.Categories.EnsureForVideos(5, videos); err != nil {
		t.Fatal(err)
	}
	defs := []CategoryDef{
		{TypeID: "7", Name: "电影", ParentTypeID: "0"},
		{TypeID: "8", Name: "连续剧", ParentTypeID: "0"},
		{TypeID: "29", Name: "欧美剧", ParentTypeID: "8"},
		{TypeID: "10", Name: "动作片", ParentTypeID: "7"}, // 还没有影片，不应建
	}
	if err := r.Categories.ApplyTypeTree(5, defs); err != nil {
		t.Fatal(err)
	}
	movie := categoryByTypeID(t, r, 5, "7")
	if movie.Name != "电影" || movie.Level != 1 || movie.ParentID != 0 {
		t.Fatalf("未被引用的一级分类没有建出来: %+v", movie)
	}
	var leaf model.Category
	if err := r.db.Where("source_id = ? AND type_id = ?", 5, "10").Limit(1).Find(&leaf).Error; err != nil {
		t.Fatal(err)
	}
	if leaf.ID != 0 {
		t.Fatalf("不应为没有影片的叶子分类建空记录: %+v", leaf)
	}
	ep := categoryByTypeID(t, r, 5, "29")
	if ep.Level != 2 || ep.ParentID != categoryByTypeID(t, r, 5, "8").ID {
		t.Fatalf("二级分类未挂到一级: %+v", ep)
	}
	// 幂等：重复执行不能产生重复的一级分类。
	if err := r.Categories.ApplyTypeTree(5, defs); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := r.db.Model(&model.Category{}).Where("source_id = ? AND level = 1", 5).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("一级分类数量 = %d，期望 2（电影 / 连续剧）", n)
	}
}
