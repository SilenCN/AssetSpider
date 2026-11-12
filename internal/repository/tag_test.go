package repository

import (
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
)

// TestParseTags 覆盖常见分隔符、去重与空白拆分。
func TestParseTags(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{"动作,喜剧", []string{"动作", "喜剧"}},
		{"动作，喜剧,科幻", []string{"动作", "喜剧", "科幻"}},   // 全角逗号
		{"动作/喜剧 | 科幻", []string{"动作", "喜剧", "科幻"}}, // 斜杠与竖线
		{"动作、喜剧", []string{"动作", "喜剧"}},            // 顿号
		{"科幻 惊悚", []string{"科幻", "惊悚"}},            // 空格拆分
		{"动作,动作, 动作", []string{"动作"}},              // 去重
		{"  ", nil}, // 全空白
		{"", nil},   // 空串
	}
	for _, c := range cases {
		got := ParseTags(c.raw)
		if len(got) != len(c.want) {
			t.Fatalf("ParseTags(%q) = %#v, want %#v", c.raw, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("ParseTags(%q)[%d] = %q, want %q", c.raw, i, got[i], c.want[i])
			}
		}
	}
}

// TestTagEnsureAndLink 验证标签的「创建 → 关联 → 列表 / 计数」全链路
// （资源库标签筛选维度已移除，归档数据仍保留）。
func TestTagEnsureAndLink(t *testing.T) {
	r := newTestRepos(t)
	src := model.Source{Name: "站点A", APIBase: "http://a.example/api.php/provide/vod/"}
	if err := r.Sources.CreateSource(&src); err != nil {
		t.Fatal(err)
	}

	videos := []model.Video{
		{SourceID: src.ID, VodID: "a", VodName: "片A", VodTag: "动作,科幻"},
		{SourceID: src.ID, VodID: "b", VodName: "片B", VodTag: "科幻，喜剧"},
		{SourceID: src.ID, VodID: "c", VodName: "片C", VodTag: ""}, // 无标签
	}
	tagIDsByVod, err := r.Tags.EnsureForVideos(src.ID, videos)
	if err != nil {
		t.Fatal(err)
	}
	// a → {动作, 科幻}；b → {科幻, 喜剧}；c 无标签不出现在映射里。
	if len(tagIDsByVod["a"]) != 2 || len(tagIDsByVod["b"]) != 2 {
		t.Fatalf("标签映射异常: %+v", tagIDsByVod)
	}
	if _, ok := tagIDsByVod["c"]; ok {
		t.Fatalf("无标签影片不应有映射: %+v", tagIDsByVod)
	}

	if _, err := r.Videos.UpsertVideos(videos); err != nil {
		t.Fatal(err)
	}
	idByVod, err := r.Videos.MapVodIDs(src.ID, []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(idByVod) != 3 {
		t.Fatalf("应解析出 3 个影片主键: %+v", idByVod)
	}
	if err := r.Tags.LinkVideos(idByVod, tagIDsByVod); err != nil {
		t.Fatal(err)
	}

	// 列表：站点应有 3 个标签（动作、科幻、喜剧）。
	tags, err := r.Tags.List(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 3 {
		t.Fatalf("站点应有 3 个标签，实际 %d: %+v", len(tags), tags)
	}

	// 计数：科幻被 a、b 同时使用，应为 2；动作、喜剧各 1。
	counts, err := r.Tags.CountVideosByTag()
	if err != nil {
		t.Fatal(err)
	}
	var scifi model.Tag
	for _, tg := range tags {
		if tg.Name == "科幻" {
			scifi = tg
		}
	}
	if scifi.ID == 0 {
		t.Fatalf("未找到「科幻」标签: %+v", tags)
	}
	if counts[scifi.ID] != 2 {
		t.Fatalf("「科幻」影片数应为 2，实际 %d（%+v）", counts[scifi.ID], counts)
	}

	// 资源库已移除标签筛选维度：采集仍自动归档标签（列表/计数在上面验证），
	// 但列表不再提供 tag_id 过滤，这里只复核归档计数的一致性。
	if got := counts[scifi.ID]; got != 2 {
		t.Fatalf("「科幻」归档影片数应为 2，实际 %d", got)
	}

	// 幂等：重复 Ensure + Link 不重复建标签、不重复关联。
	again, err := r.Tags.EnsureForVideos(src.ID, videos)
	if err != nil {
		t.Fatal(err)
	}
	if again["a"][0] != tagIDsByVod["a"][0] || again["b"][0] != tagIDsByVod["b"][0] {
		t.Fatalf("重复 Ensure 应复用既有标签: %+v vs %+v", again, tagIDsByVod)
	}
	if err := r.Tags.LinkVideos(idByVod, again); err != nil {
		t.Fatal(err)
	}
	counts2, _ := r.Tags.CountVideosByTag()
	if counts2[scifi.ID] != 2 {
		t.Fatalf("重复关联后「科幻」影片数应仍为 2，实际 %d", counts2[scifi.ID])
	}
}

// TestTagPerSource 验证标签按站点隔离：不同站点同名标签互不干扰。
func TestTagPerSource(t *testing.T) {
	r := newTestRepos(t)
	a := model.Source{Name: "站点A", APIBase: "http://a.example/api.php/provide/vod/"}
	b := model.Source{Name: "站点B", APIBase: "http://b.example/api.php/provide/vod/"}
	if err := r.Sources.CreateSource(&a); err != nil {
		t.Fatal(err)
	}
	if err := r.Sources.CreateSource(&b); err != nil {
		t.Fatal(err)
	}

	va := []model.Video{{SourceID: a.ID, VodID: "1", VodTag: "热播"}}
	vb := []model.Video{{SourceID: b.ID, VodID: "1", VodTag: "热播"}}
	if _, err := r.Tags.EnsureForVideos(a.ID, va); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Tags.EnsureForVideos(b.ID, vb); err != nil {
		t.Fatal(err)
	}

	ta, _ := r.Tags.List(a.ID)
	tb, _ := r.Tags.List(b.ID)
	if len(ta) != 1 || len(tb) != 1 {
		t.Fatalf("每个站点应各有 1 个「热播」标签: A=%+v B=%+v", ta, tb)
	}
	if ta[0].ID == tb[0].ID {
		t.Fatalf("不同站点的同名标签不应是同一实体: A=%d B=%d", ta[0].ID, tb[0].ID)
	}
}

// TestTagClearedWhenChanged 验证影片标签变化后（含清空）关联被正确替换。
func TestTagClearedWhenChanged(t *testing.T) {
	r := newTestRepos(t)
	src := model.Source{Name: "站点A", APIBase: "http://a.example/api.php/provide/vod/"}
	if err := r.Sources.CreateSource(&src); err != nil {
		t.Fatal(err)
	}
	v := []model.Video{{SourceID: src.ID, VodID: "x", VodTag: "动作,科幻"}}
	ids, _ := r.Tags.EnsureForVideos(src.ID, v)
	_, _ = r.Videos.UpsertVideos(v)
	idByVod, _ := r.Videos.MapVodIDs(src.ID, []string{"x"})
	if err := r.Tags.LinkVideos(idByVod, ids); err != nil {
		t.Fatal(err)
	}

	// 重新采集：标签改为「喜剧」，旧关联应被替换。
	v2 := []model.Video{{SourceID: src.ID, VodID: "x", VodTag: "喜剧"}}
	ids2, _ := r.Tags.EnsureForVideos(src.ID, v2)
	if err := r.Tags.LinkVideos(idByVod, ids2); err != nil {
		t.Fatal(err)
	}
	var links []model.VideoTag
	if err := r.db.Where("video_id = ?", idByVod["x"]).Find(&links).Error; err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("标签变化后应只剩 1 条关联，实际 %d: %+v", len(links), links)
	}
	var comedy model.Tag
	r.db.Where("id = ?", links[0].TagID).First(&comedy)
	if comedy.Name != "喜剧" {
		t.Fatalf("剩余关联应为「喜剧」，实际 %q", comedy.Name)
	}

	// 再次采集：标签清空，关联应被清空。
	v3 := []model.Video{{SourceID: src.ID, VodID: "x", VodTag: ""}}
	ids3, _ := r.Tags.EnsureForVideos(src.ID, v3)
	if err := r.Tags.LinkVideos(idByVod, ids3); err != nil {
		t.Fatal(err)
	}
	var links3 []model.VideoTag
	r.db.Where("video_id = ?", idByVod["x"]).Find(&links3)
	if len(links3) != 0 {
		t.Fatalf("标签清空后关联应清空，实际 %d", len(links3))
	}
}
