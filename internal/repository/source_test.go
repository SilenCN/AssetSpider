package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
)

func TestSourceRepositoryCRUDAndCascade(t *testing.T) {
	r := newTestRepos(t)

	src := &model.Source{
		Name: "测试资源站", APIBase: "https://example.com/api.php/provide/vod/",
		Format: "seacms_json", Enabled: true, IncrementalHours: 12,
	}
	if err := r.Sources.CreateSource(src); err != nil {
		t.Fatal(err)
	}
	if src.ID == 0 {
		t.Fatal("CreateSource 未回填 ID")
	}

	srcs, err := r.Sources.ListSources()
	if err != nil || len(srcs) != 1 || srcs[0].Name != "测试资源站" {
		t.Fatalf("ListSources 异常: %v %+v", err, srcs)
	}
	enabled, err := r.Sources.ListEnabledSources()
	if err != nil || len(enabled) != 1 {
		t.Fatalf("ListEnabledSources 异常: %v %+v", err, enabled)
	}

	got, err := r.Sources.GetSource(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	got.Name = "改名后"
	updated, err := r.Sources.UpdateSource(src.ID, got)
	if err != nil || updated.Name != "改名后" {
		t.Fatalf("UpdateSource 异常: %v %+v", err, updated)
	}

	// 采集状态回写
	if err := r.Sources.UpdateSourceResult(src.ID, model.ModeFull, model.JobSuccess,
		"2026-11-07 10:00:00", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err = r.Sources.GetSource(src.ID)
	if err != nil || got.LastStatus != model.JobSuccess || got.LastCursor == "" {
		t.Fatalf("UpdateSourceResult 未回写状态/游标: %v %+v", err, got)
	}

	// 级联删除：先写入该站点的影片与采集记录
	if _, err := r.Videos.UpsertVideos([]model.Video{{
		SourceID: src.ID, VodID: "21", VodName: "测试片", Hash: "h1", VodTime: "2026-11-07 10:00:00",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Jobs.CreateJob(&model.CrawlJob{
		SourceID: src.ID, SourceName: got.Name, Mode: model.ModeFull,
		Status: model.JobRunning, StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Categories.EnsureForVideos(src.ID, []model.Video{
		{TypeID: "6", TypeName: "动作片"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Sources.DeleteSource(src.ID); err != nil {
		t.Fatal(err)
	}
	items, total, err := r.Videos.ListVideos(NewVideoQuery(0, "", 1, 20))
	if err != nil || total != 0 || len(items) != 0 {
		t.Fatalf("删除站点后影片未清理: %v total=%d", err, total)
	}
	jobs, err := r.Jobs.ListJobs(0, 50)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("删除站点后采集记录未清理: %v %+v", err, jobs)
	}
	cats, err := r.Categories.List(0)
	if err != nil || len(cats) != 0 {
		t.Fatalf("删除站点后该站分类未清理: %v %+v", err, cats)
	}
}

func TestSourceRepositoryNotFound(t *testing.T) {
	r := newTestRepos(t)
	if _, err := r.Sources.GetSource(999999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的站点应返回 ErrNotFound，实际 %v", err)
	}
	if _, err := r.Sources.UpdateSource(999999, &model.Source{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("更新不存在的站点应返回 ErrNotFound，实际 %v", err)
	}
}
