package service

import (
	"path/filepath"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

// recordingCollector 记录 StartTask 的调用，并满足 SourceCollector 接口。
type recordingCollector struct {
	calls    int
	lastMode string
	lastID   uint
}

func (c *recordingCollector) HasActiveTask(uint) bool { return false }

func (c *recordingCollector) StartTask(id uint, mode string, _ ...Principal) (*TaskView, error) {
	c.calls++
	c.lastID = id
	c.lastMode = mode
	return &TaskView{}, nil
}

func newSourceRepo(t *testing.T) *repository.Repositories {
	repos, err := repository.Open(filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repos.Close() })
	return repos
}

// TestSourceCreateAutoCollect 验证：新建启用的站点且要求 autostart 时，
// 站点服务会自动发起一次全量采集；未要求、未注入采集能力、或站点被禁用时都不触发。
func TestSourceCreateAutoCollect(t *testing.T) {
	repos := newSourceRepo(t)
	coll := &recordingCollector{}
	svc := NewSourceService(repos.Sources, coll)

	in := SourceInput{Name: "新站", APIBase: "https://a.example.com/api.php/provide/vod/", Enabled: boolPtr(true)}

	// 1. autostart=true 且启用 → 自动全量采集
	created, err := svc.Create(in, true)
	if err != nil {
		t.Fatalf("创建站点失败: %v", err)
	}
	if coll.calls != 1 {
		t.Fatalf("autostart 时应调用一次 StartTask，实际 %d 次", coll.calls)
	}
	if coll.lastMode != model.ModeFull {
		t.Fatalf("自动采集应为全量，实际 %q", coll.lastMode)
	}
	if coll.lastID != created.ID {
		t.Fatalf("自动采集的站点 ID 应为 %d，实际 %d", created.ID, coll.lastID)
	}

	// 2. autostart 未传（默认）→ 不自动采集
	if _, err := svc.Create(SourceInput{Name: "手动站", APIBase: "https://b.example.com/api.php/provide/vod/", Enabled: boolPtr(true)}); err != nil {
		t.Fatalf("创建站点失败: %v", err)
	}
	if coll.calls != 1 {
		t.Fatalf("未要求 autostart 时不应自动采集，StartTask 调用应为 1，实际 %d", coll.calls)
	}

	// 3. autostart=true 但站点被禁用 → 不自动采集
	if _, err := svc.Create(SourceInput{Name: "禁用站", APIBase: "https://c.example.com/api.php/provide/vod/", Enabled: boolPtr(false)}, true); err != nil {
		t.Fatalf("创建站点失败: %v", err)
	}
	if coll.calls != 1 {
		t.Fatalf("禁用站点不应自动采集，StartTask 调用应为 1，实际 %d", coll.calls)
	}
}

// TestSourceCreateNoCollectorSafe 验证：未注入采集能力时，新建站点不会因
// 缺少依赖而报错，自动采集自然被跳过。
func TestSourceCreateNoCollectorSafe(t *testing.T) {
	repos := newSourceRepo(t)
	svc := NewSourceService(repos.Sources) // 不注入 collector
	_, err := svc.Create(SourceInput{Name: "裸站", APIBase: "https://d.example.com/api.php/provide/vod/", Enabled: boolPtr(true)}, true)
	if err != nil {
		t.Fatalf("无采集依赖时新建站点不应报错: %v", err)
	}
}

func boolPtr(b bool) *bool { return &b }
