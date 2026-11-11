package service

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

// stubActiveTasks 是固定的「该站点是否有在跑任务」答案，用于验证清空前的互斥检查。
type stubActiveTasks struct {
	active bool
}

func (s stubActiveTasks) HasActiveTask(uint) bool { return s.active }

// 满足 SourceCollector 接口：测试里只用到 HasActiveTask，StartTask 留空即可。
func (s stubActiveTasks) StartTask(uint, string, ...Principal) (*TaskView, error) {
	return nil, nil
}

// TestSourceClearRefusesWhileCrawling 验证：站点还有排队/运行中的采集任务时，
// 清空数据返回冲突错误且不删除任何数据。
func TestSourceClearRefusesWhileCrawling(t *testing.T) {
	repos, err := repository.Open(filepath.Join(t.TempDir(), "clear.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repos.Close() })

	src := &model.Source{Name: "采集中的站", APIBase: "https://a.example.com/api.php/provide/vod/", Enabled: true}
	if err := repos.Sources.CreateSource(src); err != nil {
		t.Fatal(err)
	}
	if _, err := repos.Videos.UpsertVideos([]model.Video{
		{SourceID: src.ID, VodID: "1", VodName: "片1", Hash: "h1"},
	}); err != nil {
		t.Fatal(err)
	}

	svc := NewSourceService(repos.Sources, stubActiveTasks{active: true})
	if _, err := svc.Clear(src.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("站点正在采集时应返回 ErrConflict，实际 %v", err)
	}
	if _, total, err := listVideosForTest(repos.Videos, repository.NewVideoQuery(src.ID, "", 1, 20)); err != nil || total != 1 {
		t.Fatalf("冲突时不应删除影片: total=%d err=%v", total, err)
	}

	// 任务结束后（没有在跑的任务）即可清空。
	svc = NewSourceService(repos.Sources, stubActiveTasks{active: false})
	res, err := svc.Clear(src.ID)
	if err != nil {
		t.Fatalf("无任务时清空应成功: %v", err)
	}
	if res.Videos != 1 {
		t.Fatalf("清空回报的影片数应为 1，实际 %d", res.Videos)
	}
	if _, total, err := listVideosForTest(repos.Videos, repository.NewVideoQuery(src.ID, "", 1, 20)); err != nil || total != 0 {
		t.Fatalf("清空后影片应为 0: total=%d err=%v", total, err)
	}
	// 站点配置仍在。
	if got, err := repos.Sources.GetSource(src.ID); err != nil || got.Name != src.Name {
		t.Fatalf("站点配置应保留: %+v err=%v", got, err)
	}
}
