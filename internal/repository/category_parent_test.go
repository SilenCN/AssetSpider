package repository

import (
	"path/filepath"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
)

// legacyCategoryTree 造一份和真实老库一致的残缺数据：
// 一级分类「电影」(7)、「综艺」(9) 恰好有影片以自身 type_id 出现过，所以存在；
// 而「电视剧」(8)、「动漫」(40)、「体育」(41) 是纯容器，只出现在别的影片的
// type_id_1 里，老版本没有为它们建记录 → 大量二级分类挤进前端「未归类」。
func legacyCategoryTree(t *testing.T, r *Repositories) {
	t.Helper()
	cats := []model.Category{
		{SourceID: 1, Key: "id:7", TypeID: "7", Name: "电影", Level: 1},
		{SourceID: 1, Key: "id:9", TypeID: "9", Name: "综艺", Level: 1},
		{SourceID: 1, Key: "id:43", TypeID: "43", Name: "AI漫剧", Level: 1},
		{SourceID: 1, Key: "id:15", TypeID: "15", Name: "剧情片", TypePID: "7", Level: 2},
		{SourceID: 1, Key: "id:38", TypeID: "38", Name: "短剧", TypePID: "8", Level: 2},
		{SourceID: 1, Key: "id:1", TypeID: "1", Name: "国产动漫", TypePID: "40", Level: 2},
		{SourceID: 1, Key: "id:42", TypeID: "42", Name: "足球", TypePID: "41", Level: 2},
		{SourceID: 1, Key: "id:34", TypeID: "34", Name: "大陆综艺", TypePID: "9", Level: 2},
	}
	for _, c := range cats {
		if err := r.db.Create(&c).Error; err != nil {
			t.Fatal(err)
		}
	}
	videos := []model.Video{
		{VodID: "v1", SourceID: 1, TypeID: "15", TypeID1: "7", TypeName: "剧情片"},
		{VodID: "v2", SourceID: 1, TypeID: "38", TypeID1: "8", TypeName: "短剧"},
		{VodID: "v3", SourceID: 1, TypeID: "1", TypeID1: "40", TypeName: "国产动漫"},
		{VodID: "v4", SourceID: 1, TypeID: "42", TypeID1: "41", TypeName: "足球"},
		{VodID: "v5", SourceID: 1, TypeID: "43", TypeID1: "0", TypeName: "AI漫剧"},
	}
	if err := r.db.Create(&videos).Error; err != nil {
		t.Fatal(err)
	}
}

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

// TestBackfillCategoryParents 验证启动时回灌能补齐缺失的一级分类并重挂子树。
func TestBackfillCategoryParents(t *testing.T) {
	r := newTestRepos(t)
	legacyCategoryTree(t, r)

	if err := backfillCategoryParents(r.db); err != nil {
		t.Fatal(err)
	}

	for _, pid := range []string{"8", "40", "41"} {
		p := categoryByTypeID(t, r, 1, pid)
		if p.Level != 1 || p.ParentID != 0 || p.TypePID != "" {
			t.Fatalf("补建的父级 %s 应为一级分类且无父级: %+v", pid, p)
		}
		if p.Name != pid {
			t.Fatalf("无 typelist 时父级名称应以 type_id 占位，实际 %q", p.Name)
		}
	}
	// 原有的一级分类不能被误改成二级，也不能多建一份。
	for _, pid := range []string{"7", "9", "43"} {
		p := categoryByTypeID(t, r, 1, pid)
		if p.Level != 1 || p.ParentID != 0 {
			t.Fatalf("已有的一级分类 %s 被改坏: %+v", pid, p)
		}
	}
	for _, tc := range []struct{ child, parent string }{
		{"15", "7"}, {"38", "8"}, {"1", "40"}, {"42", "41"}, {"34", "9"},
	} {
		ch := categoryByTypeID(t, r, 1, tc.child)
		p := categoryByTypeID(t, r, 1, tc.parent)
		if ch.ParentID != p.ID || ch.Level != 2 {
			t.Fatalf("二级分类 %s 未挂到父级 %s: %+v", tc.child, tc.parent, ch)
		}
	}
	// 幂等：再跑一次不重复建分类、结果不变。
	var before int64
	if err := r.db.Model(&model.Category{}).Count(&before).Error; err != nil {
		t.Fatal(err)
	}
	if err := backfillCategoryParents(r.db); err != nil {
		t.Fatal(err)
	}
	var after int64
	if err := r.db.Model(&model.Category{}).Count(&after).Error; err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("重复回灌不应新增分类: %d → %d", before, after)
	}
}

// TestBackfillCategoryParentsOnOpenUpgrade 验证老库（user_version=4）启动即自动修复。
func TestBackfillCategoryParentsOnOpenUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	legacyCategoryTree(t, r)
	// 伪装成升级前的库；重开时应触发 v4 → v5 的回灌。
	if err := r.db.Exec("PRAGMA user_version = 4").Error; err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	for _, pid := range []string{"8", "40", "41"} {
		if p := categoryByTypeID(t, again, 1, pid); p.Level != 1 {
			t.Fatalf("升级后仍缺失一级分类 %s: %+v", pid, p)
		}
	}
	var version int
	if err := again.db.Raw("PRAGMA user_version").Scan(&version).Error; err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version 应升到 %d，实际 %d", schemaVersion, version)
	}
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
