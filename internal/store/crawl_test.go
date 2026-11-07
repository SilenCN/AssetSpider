package store

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "db", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSourceCRUDAndCascade(t *testing.T) {
	s := newTestStore(t)

	src := &model.Source{
		Name: "测试资源站", APIBase: "https://example.com/api.php/provide/vod/",
		Format: "seacms_json", Enabled: true, IncrementalHours: 12,
	}
	if err := s.CreateSource(src); err != nil {
		t.Fatal(err)
	}
	if src.ID == 0 {
		t.Fatal("CreateSource 未回填 ID")
	}

	srcs, err := s.ListSources()
	if err != nil || len(srcs) != 1 || srcs[0].Name != "测试资源站" {
		t.Fatalf("ListSources 异常: %v %+v", err, srcs)
	}
	enabled, err := s.ListEnabledSources()
	if err != nil || len(enabled) != 1 {
		t.Fatalf("ListEnabledSources 异常: %v %+v", err, enabled)
	}

	got, err := s.GetSource(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	got.Name = "改名后"
	updated, err := s.UpdateSource(src.ID, got)
	if err != nil || updated.Name != "改名后" {
		t.Fatalf("UpdateSource 异常: %v %+v", err, updated)
	}

	// 级联删除：先写入该站点的影片与采集记录
	if _, err := s.UpsertVideos([]model.Video{{
		SourceID: src.ID, VodID: "21", VodName: "测试片", Hash: "h1", VodTime: "2026-11-07 10:00:00",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateJob(&model.CrawlJob{
		SourceID: src.ID, SourceName: got.Name, Mode: model.ModeFull,
		Status: model.JobRunning, StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSource(src.ID); err != nil {
		t.Fatal(err)
	}
	items, total, err := s.ListVideos(0, "", 1, 20)
	if err != nil || total != 0 || len(items) != 0 {
		t.Fatalf("删除站点后影片未清理: %v total=%d", err, total)
	}
	jobs, err := s.ListJobs(0, 50)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("删除站点后采集记录未清理: %v %+v", err, jobs)
	}
}

func TestUpsertVideosDedup(t *testing.T) {
	s := newTestStore(t)
	mk := func(hash, name string) model.Video {
		return model.Video{
			SourceID: 1, VodID: "21", VodName: name, TypeName: "动作片",
			Hash: hash, VodTime: "2026-11-07 10:00:00", VodPlayURL: "https://x/1.m3u8",
		}
	}
	stats, err := s.UpsertVideos([]model.Video{mk("h1", "测试片")})
	if err != nil || stats.New != 1 {
		t.Fatalf("首次写入异常: %+v %v", stats, err)
	}
	stats, err = s.UpsertVideos([]model.Video{mk("h1", "测试片")})
	if err != nil || stats.Unchanged != 1 || stats.New != 0 {
		t.Fatalf("重复写入应判定未变化: %+v %v", stats, err)
	}
	stats, err = s.UpsertVideos([]model.Video{mk("h2", "测试片 更新版")})
	if err != nil || stats.Updated != 1 {
		t.Fatalf("内容变化应被更新: %+v %v", stats, err)
	}

	items, total, err := s.ListVideos(0, "", 1, 20)
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("去重后应只有 1 条: %v total=%d", err, total)
	}
	if items[0].VodName != "测试片 更新版" || items[0].FirstSeenAt.IsZero() {
		t.Fatalf("更新后内容异常: %+v", items[0])
	}
	if _, total, _ = s.ListVideos(0, "不存在", 1, 20); total != 0 {
		t.Errorf("关键词过滤异常: total=%d", total)
	}
	if _, total, _ = s.ListVideos(2, "", 1, 20); total != 0 {
		t.Errorf("站点过滤异常: total=%d", total)
	}
	counts, err := s.CountVideosBySource()
	if err != nil || counts[1] != 1 {
		t.Fatalf("CountVideosBySource 异常: %v %+v", err, counts)
	}
}

// mkFullVideo 把所有标准字段填成带标记的值，用于验证全字段写入与更新。
func mkFullVideo(mark, hash string) model.Video {
	v := model.Video{
		SourceID: 1, VodID: "21", Hash: hash,
		VodTime: "2026-11-07 10:00:00",
		Raw:     `{"mark":"` + mark + `"}`,
		Extra:   `{"custom":"` + mark + `"}`,
	}
	rv := reflect.ValueOf(&v).Elem()
	for _, f := range model.VodFields {
		fv := rv.FieldByName(f.Go)
		if fv.IsValid() && fv.CanSet() && fv.Kind() == reflect.String && fv.String() == "" {
			fv.SetString(mark + "_" + f.Key)
		}
	}
	return v
}

func TestVideoUpdateMapCoversAllFields(t *testing.T) {
	v := mkFullVideo("a", "h1")
	m := videoUpdateMap(v)
	for _, f := range model.VodFields {
		got, ok := m[f.Column]
		if !ok {
			t.Errorf("更新 map 缺少列 %s", f.Column)
			continue
		}
		if got != "a_"+f.Key && f.Key != "vod_id" && f.Key != "vod_time" {
			t.Errorf("列 %s 取值异常: %v", f.Column, got)
		}
	}
	for _, key := range []string{"hash", "raw", "extra", "last_seen_at"} {
		if _, ok := m[key]; !ok {
			t.Errorf("更新 map 缺少 %s", key)
		}
	}
	if len(m) != len(model.VodFields)+4 {
		t.Errorf("更新列数 %d != %d", len(m), len(model.VodFields)+4)
	}
}

func TestUpsertVideosUpdatesAllFields(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.UpsertVideos([]model.Video{mkFullVideo("first", "h1")}); err != nil {
		t.Fatal(err)
	}
	stats, err := s.UpsertVideos([]model.Video{mkFullVideo("second", "h2")})
	if err != nil || stats.Updated != 1 {
		t.Fatalf("全字段内容变化应触发更新: %+v %v", stats, err)
	}

	items, total, err := s.ListVideos(0, "", 1, 20)
	if err != nil || total != 1 {
		t.Fatalf("总数异常: %v total=%d", err, total)
	}
	v := items[0]
	rv := reflect.ValueOf(v)
	skipped := map[string]bool{"vod_id": true, "vod_time": true}
	for _, f := range model.VodFields {
		if skipped[f.Key] {
			continue
		}
		if got := rv.FieldByName(f.Go).String(); got != "second_"+f.Key {
			t.Errorf("更新后字段 %s = %q，期望 second_%s", f.Column, got, f.Key)
		}
	}
	if v.Hash != "h2" || v.Raw != `{"mark":"second"}` || v.Extra != `{"custom":"second"}` {
		t.Errorf("元数据字段未随更新写回: hash=%s raw=%s extra=%s", v.Hash, v.Raw, v.Extra)
	}
	got, err := s.GetVideo(v.ID)
	if err != nil || got.VodName != "second_vod_name" {
		t.Fatalf("GetVideo 异常: %v %+v", err, got)
	}
	if _, err := s.GetVideo(999999); err == nil {
		t.Error("不存在的影片应返回错误")
	}
	// 多字段搜索：演员、导演、分类也应可检索
	if _, total, _ := s.ListVideos(0, "second_vod_actor", 1, 20); total != 1 {
		t.Error("按演员字段搜索未命中")
	}
}

func TestJobLifecycle(t *testing.T) {
	s := newTestStore(t)
	job := &model.CrawlJob{
		SourceID: 1, SourceName: "站点A", Mode: model.ModeIncremental,
		Status: model.JobRunning, StartedAt: time.Now(),
	}
	if err := s.CreateJob(job); err != nil {
		t.Fatal(err)
	}
	job.Fetched, job.New, job.Pages = 10, 3, 2
	if err := s.FinishJob(job, model.JobSuccess, "采集完成"); err != nil {
		t.Fatal(err)
	}
	jobs, err := s.ListJobs(0, 50)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("ListJobs 异常: %v %+v", err, jobs)
	}
	j := jobs[0]
	if j.Status != model.JobSuccess || j.New != 3 || j.Pages != 2 || j.FinishedAt == nil {
		t.Fatalf("任务终态异常: %+v", j)
	}
	if filtered, _ := s.ListJobs(999, 50); len(filtered) != 0 {
		t.Errorf("按站点过滤任务异常: %+v", filtered)
	}
}
