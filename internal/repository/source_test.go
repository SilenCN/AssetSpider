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
	items, total, err := listVideosForTest(r.Videos, NewVideoQuery(0, "", 1, 20))
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

// TestSourceClearData 验证「清空站点数据、站点配置不变」：
// 影片 / 分类 / 标签 / 采集记录（含 video_tags 关联）一并清空，采集状态与
// 影片数快照复位，而站点自身的配置字段与其它站点的数据都不受影响。
func TestSourceClearData(t *testing.T) {
	r := newTestRepos(t)

	a := &model.Source{
		Name: "待清空站", APIBase: "https://a.example.com/api.php/provide/vod/",
		Format: model.FormatMacCMSJSON, Enabled: true,
		IncrementalHours: 6, MaxPages: 5,
		ParseAPI: "https://jx.example.com/?url=", PlayDomain: "https://cdn.example.com",
	}
	b := &model.Source{Name: "保留站", APIBase: "https://b.example.com/api.php/provide/vod/", Enabled: true}
	for _, src := range []*model.Source{a, b} {
		if err := r.Sources.CreateSource(src); err != nil {
			t.Fatal(err)
		}
	}

	// 两个站点各写入影片、分类、标签、采集记录，用于同时验证清空范围与站点隔离。
	for _, src := range []*model.Source{a, b} {
		if _, err := r.Videos.UpsertVideos([]model.Video{
			{SourceID: src.ID, VodID: "1", VodName: "片1", Hash: "h-" + src.Name + "-1", VodTag: "科幻,动作"},
			{SourceID: src.ID, VodID: "2", VodName: "片2", Hash: "h-" + src.Name + "-2", VodTag: "动作"},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Categories.EnsureForVideos(src.ID, []model.Video{
			{TypeID: "6", TypeName: "动作"},
			{TypeID: "7", TypeName: "喜剧"},
		}); err != nil {
			t.Fatal(err)
		}
		tagsByVod, err := r.Tags.EnsureForVideos(src.ID, []model.Video{
			{SourceID: src.ID, VodID: "1", VodTag: "科幻,动作"},
			{SourceID: src.ID, VodID: "2", VodTag: "动作"},
		})
		if err != nil {
			t.Fatal(err)
		}
		idByVod, err := r.Videos.MapVodIDs(src.ID, []string{"1", "2"})
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Tags.LinkVideos(idByVod, tagsByVod); err != nil {
			t.Fatal(err)
		}
		if err := r.Jobs.CreateJob(&model.CrawlJob{
			SourceID: src.ID, SourceName: src.Name, Mode: model.ModeFull,
			Status: model.JobSuccess, StartedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// 站 A 的采集状态与影片数快照先写成「已采集」。
	now := time.Now()
	if err := r.Sources.UpdateSourceResult(a.ID, model.ModeFull, model.JobSuccess,
		"2026-11-08 10:00:00", "", now); err != nil {
		t.Fatal(err)
	}
	if err := r.Sources.UpdateVideoCount(a.ID, 2); err != nil {
		t.Fatal(err)
	}
	aVideoIDs, err := r.Videos.MapVodIDs(a.ID, []string{"1", "2"})
	if err != nil {
		t.Fatal(err)
	}

	stats, err := r.Sources.ClearSourceData(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats != (ClearStats{Videos: 2, Categories: 2, Tags: 2, Jobs: 1}) {
		t.Fatalf("清空回报条数异常: %+v", stats)
	}

	// 1) 数据被清空。
	if _, total, err := listVideosForTest(r.Videos, NewVideoQuery(a.ID, "", 1, 20)); err != nil || total != 0 {
		t.Fatalf("站 A 影片未清空: total=%d err=%v", total, err)
	}
	if cats, err := r.Categories.List(a.ID); err != nil || len(cats) != 0 {
		t.Fatalf("站 A 分类未清空: %+v err=%v", cats, err)
	}
	if tags, err := r.Tags.List(a.ID); err != nil || len(tags) != 0 {
		t.Fatalf("站 A 标签未清空: %+v err=%v", tags, err)
	}
	if jobs, err := r.Jobs.ListJobs(a.ID, 50); err != nil || len(jobs) != 0 {
		t.Fatalf("站 A 采集记录未清空: %+v err=%v", jobs, err)
	}
	var links int64
	for _, id := range aVideoIDs {
		var n int64
		if err := r.db.Model(&model.VideoTag{}).Where("video_id = ?", id).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		links += n
	}
	if links != 0 {
		t.Fatalf("站 A 的标签关联未清理，仍有 %d 条", links)
	}

	// 2) 站点配置不变，采集状态与影片数复位。
	got, err := r.Sources.GetSource(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != a.Name || got.APIBase != a.APIBase || got.Format != a.Format ||
		got.Enabled != a.Enabled || got.IncrementalHours != a.IncrementalHours ||
		got.MaxPages != a.MaxPages || got.ParseAPI != a.ParseAPI || got.PlayDomain != a.PlayDomain {
		t.Fatalf("站点配置不应被清空改动: %+v", got)
	}
	if got.LastStatus != "" || got.LastMode != "" || got.LastCursor != "" ||
		got.LastSyncAt != nil || got.LastFullSyncAt != nil || got.VideoCount != 0 {
		t.Fatalf("采集状态与影片数应复位: %+v", got)
	}

	// 3) 其它站点不受影响。
	if _, total, err := listVideosForTest(r.Videos, NewVideoQuery(b.ID, "", 1, 20)); err != nil || total != 2 {
		t.Fatalf("站 B 影片不应受影响: total=%d err=%v", total, err)
	}
	if cats, err := r.Categories.List(b.ID); err != nil || len(cats) != 2 {
		t.Fatalf("站 B 分类不应受影响: %+v err=%v", cats, err)
	}
	if tags, err := r.Tags.List(b.ID); err != nil || len(tags) != 2 {
		t.Fatalf("站 B 标签不应受影响: %+v err=%v", tags, err)
	}
	if jobs, err := r.Jobs.ListJobs(b.ID, 50); err != nil || len(jobs) != 1 {
		t.Fatalf("站 B 采集记录不应受影响: %+v err=%v", jobs, err)
	}

	// 4) 不存在的站点按 ErrNotFound 处理。
	if _, err := r.Sources.ClearSourceData(999999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("清空不存在的站点应返回 ErrNotFound，实际 %v", err)
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

// TestSourceVideoCountSnapshot 验证站点表上的影片总数快照：
// UpdateVideoCount 回写单站点，RefreshVideoCounts 能按 videos 表整体校准。
func TestSourceVideoCountSnapshot(t *testing.T) {
	r := newTestRepos(t)
	a := &model.Source{Name: "站A", APIBase: "https://a.example.com/api.php/provide/vod/", Enabled: true}
	b := &model.Source{Name: "站B", APIBase: "https://b.example.com/api.php/provide/vod/", Enabled: true}
	for _, src := range []*model.Source{a, b} {
		if err := r.Sources.CreateSource(src); err != nil {
			t.Fatal(err)
		}
	}
	if a.VideoCount != 0 {
		t.Fatalf("新建站点影片数应为 0，实际 %d", a.VideoCount)
	}

	// 单站点回写。
	if err := r.Sources.UpdateVideoCount(a.ID, 7); err != nil {
		t.Fatal(err)
	}
	got, err := r.Sources.GetSource(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.VideoCount != 7 {
		t.Fatalf("UpdateVideoCount 未回写，实际 %d", got.VideoCount)
	}
	// 负数按 0 处理，避免脏值把列表显示成负片数。
	if err := r.Sources.UpdateVideoCount(a.ID, -3); err != nil {
		t.Fatal(err)
	}
	if got, _ = r.Sources.GetSource(a.ID); got.VideoCount != 0 {
		t.Fatalf("负数应归零，实际 %d", got.VideoCount)
	}

	// 两个站点各 2 / 1 条影片，但快照先被写脏再整体校准。
	if _, err := r.Videos.UpsertVideos([]model.Video{
		{SourceID: a.ID, VodID: "1", VodName: "A1", Hash: "h1"},
		{SourceID: a.ID, VodID: "2", VodName: "A2", Hash: "h2"},
		{SourceID: b.ID, VodID: "1", VodName: "B1", Hash: "h3"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.Sources.UpdateVideoCount(a.ID, 99); err != nil {
		t.Fatal(err)
	}
	if err := r.Sources.RefreshVideoCounts(); err != nil {
		t.Fatal(err)
	}
	if got, _ = r.Sources.GetSource(a.ID); got.VideoCount != 2 {
		t.Fatalf("站A 影片数应为 2，实际 %d", got.VideoCount)
	}
	if got, _ = r.Sources.GetSource(b.ID); got.VideoCount != 1 {
		t.Fatalf("站B 影片数应为 1，实际 %d", got.VideoCount)
	}
}
