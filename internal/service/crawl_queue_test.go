package service

import (
	"testing"
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

// waitTaskStatus 等任务进入指定状态（任务已结束/不存在即失败）。
func waitTaskStatus(t *testing.T, svc *CrawlService, jobID uint, status string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if view := svc.LiveTask(jobID); view != nil && view.Status == status {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待任务 #%d 进入 %s 超时", jobID, status)
}

// TestSameSourceTasksQueueInsteadOfReject 验证同一资源站的任务不再互斥报错：
// 第二个任务以 queued 进入队列，前一个结束后自动补位，两者不会同时运行。
func TestSameSourceTasksQueueInsteadOfReject(t *testing.T) {
	items := []mockItem{
		{ID: "1", Name: "影片A", VodTime: "2026-11-06 08:00:00"},
		{ID: "2", Name: "影片B", VodTime: "2026-11-06 09:00:00"},
		{ID: "3", Name: "影片C", VodTime: "2026-11-06 10:00:00"},
		{ID: "4", Name: "影片D", VodTime: "2026-11-06 11:00:00"},
	}
	m := newMockSite(t, items)
	m.delay = 150 * time.Millisecond // 2 页约 300ms，留出排队的观察窗口
	st, svc := newCrawlService(t)
	id := addSource(t, st, m.server.URL+"/api.php/provide/vod/", true)

	first, err := svc.startTask(id, model.ModeFull, startOptions{})
	if err != nil {
		t.Fatal(err)
	}
	waitTaskStatus(t, svc, first.JobID, model.JobRunning)

	second, err := svc.startTask(id, model.ModeFull, startOptions{})
	if err != nil {
		t.Fatalf("同站点重复发起应排队而不是报错: %v", err)
	}
	if second.JobID == first.JobID || second.Status != model.JobQueued {
		t.Fatalf("第二个任务应以 queued 排队，实际 jobID=%d status=%s", second.JobID, second.Status)
	}

	// 任务列表里两条都在，且排队数把它算进去了。
	deadline := time.Now().Add(5 * time.Second)
	for svc.Capacity().Queued < 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := svc.Capacity().Queued; got < 1 {
		t.Fatalf("等本站点的任务应计入排队数，实际 %d", got)
	}

	// 采样整个过程：同站点任何时刻最多只有一个任务在运行；第二个一旦开跑，
	// 第一个必须已经从运行时任务表里消失（说明是串行，不是并发）。
	sawSecondRunning := false
	deadline = time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		firstView, secondView := svc.LiveTask(first.JobID), svc.LiveTask(second.JobID)
		if secondView != nil && secondView.Status == model.JobRunning {
			if firstView != nil {
				t.Fatalf("同站点出现两个任务同时运行：#%d 与 #%d", first.JobID, second.JobID)
			}
			sawSecondRunning = true
		}
		if firstView == nil && secondView == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawSecondRunning {
		t.Fatal("排队的任务在前一个结束后应自动开始运行")
	}
	if !svc.WaitForTasks(20 * time.Second) {
		t.Fatal("任务未在预期时间内结束")
	}

	for _, jobID := range []uint{first.JobID, second.JobID} {
		job, err := st.Jobs.GetJob(jobID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status != model.JobSuccess {
			t.Fatalf("任务 #%d 应成功，实际 %s（%s）", jobID, job.Status, job.Message)
		}
	}
	// 两次全量采集同一批影片，不应产生重复行。
	if _, total, err := st.Videos.ListVideos(repository.NewVideoQuery(id, "", 1, 20)); err != nil || total != 4 {
		t.Fatalf("重复采集不应产生重复影片: total=%d err=%v", total, err)
	}
}

// TestPauseQueuedTaskSettlesPaused 验证排队中的任务也能暂停：
// 它不会真的去抓取（页数为 0），直接以 paused 收尾，且不影响正在运行的那个。
func TestPauseQueuedTaskSettlesPaused(t *testing.T) {
	items := []mockItem{
		{ID: "1", Name: "影片A", VodTime: "2026-11-06 08:00:00"},
		{ID: "2", Name: "影片B", VodTime: "2026-11-06 09:00:00"},
		{ID: "3", Name: "影片C", VodTime: "2026-11-06 10:00:00"},
		{ID: "4", Name: "影片D", VodTime: "2026-11-06 11:00:00"},
	}
	m := newMockSite(t, items)
	m.delay = 250 * time.Millisecond
	st, svc := newCrawlService(t)
	id := addSource(t, st, m.server.URL+"/api.php/provide/vod/", true)

	first, err := svc.startTask(id, model.ModeFull, startOptions{})
	if err != nil {
		t.Fatal(err)
	}
	waitTaskStatus(t, svc, first.JobID, model.JobRunning)

	second, err := svc.startTask(id, model.ModeFull, startOptions{})
	if err != nil {
		t.Fatalf("同站点重复发起应排队而不是报错: %v", err)
	}
	if second.Status != model.JobQueued {
		t.Fatalf("第二个任务应排队，实际 %s", second.Status)
	}

	if _, err := svc.PauseTask(second.JobID); err != nil {
		t.Fatalf("排队中的任务应可暂停: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for svc.LiveTask(second.JobID) != nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if view := svc.LiveTask(second.JobID); view != nil {
		t.Fatalf("排队中被暂停的任务应结束，实际仍在 %s", view.Status)
	}

	job, err := st.Jobs.GetJob(second.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != model.JobPaused {
		t.Fatalf("排队中暂停的任务终态应为 paused，实际 %s", job.Status)
	}
	if job.Pages != 0 || job.Fetched != 0 {
		t.Fatalf("排队中暂停的任务不应抓取任何页面: %+v", job)
	}

	if !svc.WaitForTasks(20 * time.Second) {
		t.Fatal("任务未在预期时间内结束")
	}
	// 正在运行的那个不受影响，正常采完。
	if job, err = st.Jobs.GetJob(first.JobID); err != nil || job.Status != model.JobSuccess {
		t.Fatalf("先运行的任务应正常完成: %+v err=%v", job, err)
	}
}
