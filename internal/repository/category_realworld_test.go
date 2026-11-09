package repository

import (
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
)

// TestCategoryRealWorldTwoLevel 复现真实 MacCMS 二级分类场景：
// 影片只挂在二级分类上（动作=type_id6/type_id_1=1，喜剧=type_id7/type_id_1=1），
// 一级分类「电影」(type_id=1) 自己没有任何影片，因此永远不会作为某条影片的
// 自身 type_id 出现，只能从子分类的 type_id_1 推断。
//
// 期望：即便「电影」没有直接影片，也应从 type_id_1 推断出父级并把它建出来，
// 二级分类被自动挂到它下面，而不是沦为孤儿。
func TestCategoryRealWorldTwoLevel(t *testing.T) {
	r := newTestRepos(t)
	// 注意：没有 type_id=1 的影片，模拟父分类是纯容器。
	ids, err := r.Categories.EnsureForVideos(1, []model.Video{
		{TypeID: "6", TypeName: "动作", TypeID1: "1"},
		{TypeID: "7", TypeName: "喜剧", TypeID1: "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 2 个二级分类 + 1 个从 type_id_1 推断出来的父级 = 3 个。
	if len(ids) != 3 {
		t.Fatalf("应创建 2 个二级分类 + 1 个推断父级，共 3 个: %+v", ids)
	}

	action, err := r.Categories.Get(ids["id:6"])
	if err != nil {
		t.Fatal(err)
	}
	if action.Level != 2 {
		t.Fatalf("二级分类层级错误: %+v", action)
	}
	if action.ParentID == 0 {
		t.Fatalf("二级分类未能挂到推断出的父级: %+v", action)
	}
	parent, err := r.Categories.Get(action.ParentID)
	if err != nil {
		t.Fatalf("父分类不存在，说明父级(type_id=1)未被从 type_id_1 推断创建: %v", err)
	}
	if parent.TypeID != "1" {
		t.Fatalf("父分类 type_id 错误: %+v", parent)
	}
	if parent.Level != 1 {
		t.Fatalf("推断出的父级应为一级分类: %+v", parent)
	}
}

// TestCategoryOrphanWhenParentMissing 验证：当二级分类的 type_pid 指向一个
// 本源内根本不存在的父级（例如旧数据残留）时，该分类保持为未挂接的孤儿
// （parent_id=0），交由前端「未归类」分组处理；待对应父级真正出现后，
// 兜底归树应把它挂上去。
func TestCategoryOrphanWhenParentMissing(t *testing.T) {
	r := newTestRepos(t)
	// 模拟旧数据残留：一条二级分类的 type_pid 指向本源内不存在的父级（type_id=99）。
	if err := r.db.Create(&model.Category{
		SourceID: 1, Key: "id:6", TypeID: "6", Name: "动作", TypePID: "99", ParentID: 0, Level: 2,
	}).Error; err != nil {
		t.Fatal(err)
	}
	// 一次常规采集（不含 type_id=99 的父级）后，该孤儿不应被错挂。
	if _, err := r.Categories.EnsureForVideos(1, []model.Video{
		{TypeID: "7", TypeName: "喜剧", TypeID1: "1"},
	}); err != nil {
		t.Fatal(err)
	}
	var orphan model.Category
	if err := r.db.First(&orphan, "source_id = ? AND type_id = ?", 1, "6").Error; err != nil {
		t.Fatal(err)
	}
	if orphan.ParentID != 0 {
		t.Fatalf("指向不存在父级的分类不应被挂接: %+v", orphan)
	}
	// 后续 type_id=99 的父级经某条影片的 type_id_1=99 出现，兜底归树应挂上孤儿。
	if _, err := r.Categories.EnsureForVideos(1, []model.Video{
		{TypeID: "6b", TypeName: "其它", TypeID1: "99"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.db.First(&orphan, "source_id = ? AND type_id = ?", 1, "6").Error; err != nil {
		t.Fatal(err)
	}
	if orphan.ParentID == 0 {
		t.Fatalf("父级出现后孤儿应被兜底挂接: %+v", orphan)
	}
}

// TestCategoryApplyTypeNames 验证：父级（纯容器）分类在 EnsureForVideos 里只能以
// type_id 兜底展示，经苹果CMS typelist 拿到权威名称后，ApplyTypeNames 能把它补全，
// 同时已带真实名称的二级分类不被 typelist 覆盖（保护既有数据）。
func TestCategoryApplyTypeNames(t *testing.T) {
	r := newTestRepos(t)
	// 父级 type_id=1 没有影片、也没有名称 → EnsureForVideos 只能建出 type_id 兜底的分类。
	if _, err := r.Categories.EnsureForVideos(1, []model.Video{
		{TypeID: "6", TypeName: "动作", TypeID1: "1"},
		{TypeID: "7", TypeName: "喜剧", TypeID1: "1"},
	}); err != nil {
		t.Fatal(err)
	}
	// 找到父级（type_pid 为空、level=1、name 为 type_id 兜底）。
	all, err := r.Categories.List(1)
	if err != nil {
		t.Fatal(err)
	}
	var parentID uint
	for _, c := range all {
		if c.TypeID == "1" {
			parentID = c.ID
		}
	}
	if parentID == 0 {
		t.Fatal("未找到从 type_id_1 推断出的父级分类")
	}
	parent, err := r.Categories.Get(parentID)
	if err != nil {
		t.Fatal(err)
	}
	if parent.Name != "1" {
		t.Fatalf("父级此前应为 type_id 兜底名称，实际 %q", parent.Name)
	}

	// typelist 给出权威名称：父级 1→电影，二级 6→动作、7→喜剧（与既有名称相同）。
	if err := r.Categories.ApplyTypeNames(1, map[string]string{
		"1": "电影",
		"6": "动作",
		"7": "喜剧",
	}); err != nil {
		t.Fatal(err)
	}

	parent, _ = r.Categories.Get(parentID)
	if parent.Name != "电影" {
		t.Fatalf("父级名称应被 typelist 补全为「电影」，实际 %q", parent.Name)
	}
	// 找到二级分类 6 确认其名称未被覆盖。
	var actionID uint
	all, _ = r.Categories.List(1)
	for _, c := range all {
		if c.TypeID == "6" {
			actionID = c.ID
		}
	}
	action, err := r.Categories.Get(actionID)
	if err != nil {
		t.Fatal(err)
	}
	if action.Name != "动作" {
		t.Fatalf("已带真实名称的二级分类不应被 typelist 覆盖，实际 %q", action.Name)
	}

	// 幂等：重复调用不报错、不重复创建，名称保持一致。
	if err := r.Categories.ApplyTypeNames(1, map[string]string{
		"1": "电影",
		"6": "动作",
		"7": "喜剧",
	}); err != nil {
		t.Fatal(err)
	}
	parent, _ = r.Categories.Get(parentID)
	if parent.Name != "电影" {
		t.Fatalf("幂等调用后父级名称应为「电影」，实际 %q", parent.Name)
	}

	// 空 map 直接返回，不报错。
	if err := r.Categories.ApplyTypeNames(1, nil); err != nil {
		t.Fatalf("空 map 不应报错: %v", err)
	}
}
