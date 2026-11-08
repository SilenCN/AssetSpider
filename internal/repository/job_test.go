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
