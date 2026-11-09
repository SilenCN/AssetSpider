package service

import (
	"context"
	"testing"
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
)

// TestCrawlUpdatesSourceVideoCountOnFinish 验证站点会记录影片总数：
// 采集前为 0，采集结束后等于实际入库条数；再跑一次增量不会因为 upsert 重复累加。
func TestCrawlUpdatesSourceVideoCountOnFinish(t *testing.T) {
	items := []mockItem{
		{ID: "1", Name: "影片A", VodTime: "2026-11-06 08:00:00", PlayURL: "https://x/a.m3u8"},
		{ID: "2", Name: "影片B", VodTime: "2026-11-06 09:00:00", PlayURL: "https://x/b.m3u8"},
		{ID: "3", Name: "影片C", VodTime: "2026-11-07 10:00:00", PlayURL: "https://x/c.m3u8"},
	}
	m := newMockSite(t, items)
	st, svc := newCrawlService(t)
	id := addSource(t, st, m.server.URL+"/api.php/provide/vod/", true)

	src, err := st.Sources.GetSource(id)
	if err != nil {
		t.Fatal(err)
	}
	if src.VideoCount != 0 {
		t.Fatalf("采集前影片数应为 0，实际 %d", src.VideoCount)
	}

	if _, err := svc.RunOne(context.Background(), id, model.ModeFull); err != nil {
		t.Fatal(err)
	}
	src, err = st.Sources.GetSource(id)
	if err != nil {
		t.Fatal(err)
	}
	if src.VideoCount != 3 {
		t.Fatalf("采集结束后影片数应为 3，实际 %d", src.VideoCount)
	}

	// 增量再跑一次（站点没有新数据）：总数保持不变，证明是重算而不是累加。
	if _, err := svc.RunOne(context.Background(), id, model.ModeIncremental); err != nil {
		t.Fatal(err)
	}
	src, err = st.Sources.GetSource(id)
	if err != nil {
		t.Fatal(err)
	}
	if src.VideoCount != 3 {
		t.Fatalf("重复采集后影片数应仍为 3，实际 %d", src.VideoCount)
	}
}

// TestCrawlRefreshesSourceVideoCountWhileRunning 验证任务运行期间也会刷新影片总数：
// 把进度落库间隔调成 1 秒，让站点每页慢 400ms，在任务尚未结束时就应能看到
// 站点上的影片数已经涨上去了（对应「每跑一分钟更新一次」）。
func TestCrawlRefreshesSourceVideoCountWhileRunning(t *testing.T) {
	items := []mockItem{
		{ID: "1", Name: "影片A", VodTime: "2026-11-06 08:00:00"},
		{ID: "2", Name: "影片B", VodTime: "2026-11-06 09:00:00"},
		{ID: "3", Name: "影片C", VodTime: "2026-11-06 10:00:00"},
		{ID: "4", Name: "影片D", VodTime: "2026-11-06 11:00:00"},
		{ID: "5", Name: "影片E", VodTime: "2026-11-06 12:00:00"},
		{ID: "6", Name: "影片F", VodTime: "2026-11-06 13:00:00"},
	}
	m := newMockSite(t, items)
	m.delay = 400 * time.Millisecond // 3 页共约 1.2 秒，足以跨过一次 1 秒的刷新节拍
	st, svc := newCrawlService(t)
	svc.cfg.ProgressSaveSeconds = 1
	id := addSource(t, st, m.server.URL+"/api.php/provide/vod/", true)

	task, err := svc.startTask(id, model.ModeFull, startOptions{})
	if err != nil {
		t.Fatal(err)
	}

	var midRun int64
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		// 任务已结束就说明没能在运行中观察到刷新，直接退出由下面的断言报错。
		if svc.LiveTask(task.JobID) == nil {
			break
		}
		if src, err := st.Sources.GetSource(id); err == nil && src.VideoCount > 0 {
			midRun = src.VideoCount
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if midRun == 0 {
		t.Fatal("任务运行期间站点影片数没有被刷新（应随进度落库间隔定期更新）")
	}

	if !svc.WaitForTasks(20 * time.Second) {
		t.Fatal("采集任务未在预期时间内结束")
	}
	src, err := st.Sources.GetSource(id)
	if err != nil {
		t.Fatal(err)
	}
	if src.VideoCount != 6 {
		t.Fatalf("任务结束后影片数应为 6，实际 %d", src.VideoCount)
	}
}
