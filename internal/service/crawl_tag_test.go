package service

import (
	"context"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

// TestCrawlSavesTagsAndCounts 端到端验证：采集时按 vod_tag 自动创建标签、
// 建立影片关联并统计影片数（资源库标签筛选维度已移除，归档数据仍保留）。
func TestCrawlSavesTagsAndCounts(t *testing.T) {
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

	// 资源库已移除标签筛选维度：采集仍自动归档标签并统计影片数（上面已验证），
	// 但列表不再提供 tag_id 过滤；无标签影片照常出现在全量列表里。
	if _, total, err := listVideosForTest(st.Videos, repository.NewVideoQuery(id, "", 1, 20)); err != nil || total != 3 {
		t.Fatalf("全量列表应有 3 条，实际 %d（err=%v）", total, err)
	}
}
