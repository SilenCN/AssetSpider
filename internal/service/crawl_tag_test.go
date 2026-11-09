package service

import (
	"context"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

// TestCrawlSavesTagsAndFilters 端到端验证：采集时按 vod_tag 自动创建标签、
// 建立影片关联，并能按标签筛选影片。
func TestCrawlSavesTagsAndFilters(t *testing.T) {
	items := []mockItem{
		{ID: "1", Name: "影片A", VodTime: "2026-11-06 08:00:00", PlayURL: "https://x/a.m3u8", Tag: "动作,科幻"},
		{ID: "2", Name: "影片B", VodTime: "2026-11-06 09:00:00", PlayURL: "https://x/b.m3u8", Tag: "科幻，喜剧"},
		{ID: "3", Name: "影片C", VodTime: "2026-11-07 10:00:00", PlayURL: "https://x/c.m3u8"}, // 无标签
	}
	m := newMockSite(t, items)
	st, svc := newCrawlService(t)
	id := addSource(t, st, m.server.URL+"/api.php/provide/vod/", true)

	if _, err := svc.RunOne(context.Background(), id, model.ModeFull); err != nil {
		t.Fatal(err)
	}

	// 应自动创建 动作 / 科幻 / 喜剧 三个标签，且都归属该站点。
	tags, err := st.Tags.List(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 3 {
		t.Fatalf("应创建 3 个标签，实际 %d: %+v", len(tags), tags)
	}
	byName := make(map[string]model.Tag, len(tags))
	for _, tg := range tags {
		byName[tg.Name] = tg
		if tg.SourceID != id {
			t.Fatalf("标签应归属站点 %d，实际 %+v", id, tg)
		}
	}
	for _, name := range []string{"动作", "科幻", "喜剧"} {
		if byName[name].ID == 0 {
			t.Fatalf("缺少标签 %q: %+v", name, tags)
		}
	}

	// 计数：科幻被 A、B 共用应为 2；动作、喜剧各 1。
	counts, err := st.Tags.CountVideosByTag()
	if err != nil {
		t.Fatal(err)
	}
	if counts[byName["科幻"].ID] != 2 {
		t.Fatalf("「科幻」影片数应为 2，实际 %d（%+v）", counts[byName["科幻"].ID], counts)
	}
	if counts[byName["动作"].ID] != 1 || counts[byName["喜剧"].ID] != 1 {
		t.Fatalf("动作/喜剧 影片数应各为 1，实际 %+v", counts)
	}

	// 按标签筛选：科幻命中 2 条，动作命中 1 条（影片A）。
	_, total, err := st.Videos.ListVideos(repository.VideoQuery{SourceID: id, TagID: byName["科幻"].ID, Page: 1, Size: 20})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("按「科幻」筛选应命中 2 条，实际 %d", total)
	}
	only, _, err := st.Videos.ListVideos(repository.VideoQuery{SourceID: id, TagID: byName["动作"].ID, Page: 1, Size: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 || only[0].VodName != "影片A" {
		t.Fatalf("按「动作」筛选应只命中影片A，实际 %+v", only)
	}

	// 无标签影片不应被任何标签筛选命中，但仍在全量列表里。
	if _, total, _ := st.Videos.ListVideos(repository.NewVideoQuery(id, "", 1, 20)); total != 3 {
		t.Fatalf("全量列表应有 3 条，实际 %d", total)
	}
}
