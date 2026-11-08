package repository

import (
	"testing"
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
)

func TestJobRepositoryLifecycle(t *testing.T) {
	r := newTestRepos(t)
	job := &model.CrawlJob{
		SourceID: 1, SourceName: "站点A", Mode: model.ModeIncremental,
		Status: model.JobRunning, StartedAt: time.Now(),
	}
	if err := r.Jobs.CreateJob(job); err != nil {
		t.Fatal(err)
	}
	job.Fetched, job.New, job.Pages = 10, 3, 2
	if err := r.Jobs.FinishJob(job, model.JobSuccess, "采集完成"); err != nil {
		t.Fatal(err)
	}
	jobs, err := r.Jobs.ListJobs(0, 50)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("ListJobs 异常: %v %+v", err, jobs)
	}
	j := jobs[0]
	if j.Status != model.JobSuccess || j.New != 3 || j.Pages != 2 || j.FinishedAt == nil {
		t.Fatalf("任务终态异常: %+v", j)
	}
	if filtered, _ := r.Jobs.ListJobs(999, 50); len(filtered) != 0 {
		t.Errorf("按站点过滤任务异常: %+v", filtered)
	}
}

// TestSaveProgressKeepsTerminalFields 验证定期落库只写进度：
// status / finished_at / message 不被覆盖，统计与续采页正常写入。
func TestSaveProgressKeepsTerminalFields(t *testing.T) {
	r := newTestRepos(t)
	job := &model.CrawlJob{
		SourceID: 2, SourceName: "站点B", Mode: model.ModeFull,
		Status: model.JobRunning, StartedAt: time.Now(), ResumePage: 1,
	}
	if err := r.Jobs.CreateJob(job); err != nil {
		t.Fatal(err)
	}
	if err := r.Jobs.StartJob(job); err != nil {
		t.Fatal(err)
	}

	progress := &model.CrawlJob{
		ID: job.ID, Pages: 7, Fetched: 35, New: 30, Updated: 3, Unchanged: 2,
		Total: 200, ResumePage: 8, Attempt: 2,
	}
	if err := r.Jobs.SaveProgress(progress); err != nil {
		t.Fatal(err)
	}
	got, err := r.Jobs.GetJob(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != model.JobRunning || got.FinishedAt != nil || got.Message != "" {
		t.Fatalf("定期落库不应改动终态字段: %+v", got)
	}
	if got.Pages != 7 || got.Fetched != 35 || got.New != 30 || got.Updated != 3 ||
		got.Unchanged != 2 || got.Total != 200 || got.ResumePage != 8 || got.Attempt != 2 {
		t.Fatalf("定期落库的进度不正确: %+v", got)
	}

	// 结束后再落库也不应把终态改回运行中（调度层会先停掉落库协程，这里是兜底断言）
	job.Pages, job.Fetched = 10, 50
	if err := r.Jobs.FinishJob(job, model.JobSuccess, "采集完成"); err != nil {
		t.Fatal(err)
	}
	if err := r.Jobs.SaveProgress(&model.CrawlJob{ID: job.ID, Pages: 11, Fetched: 55, ResumePage: 12}); err != nil {
		t.Fatal(err)
	}
	got, _ = r.Jobs.GetJob(job.ID)
	if got.Status != model.JobSuccess || got.Message != "采集完成" {
		t.Fatalf("落库不应覆盖终态: %+v", got)
	}
}
