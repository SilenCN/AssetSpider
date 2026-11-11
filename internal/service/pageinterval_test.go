package service

import (
	"context"
	"testing"
	"time"

	"github.com/SilenCN/AssetSpider/internal/config"
)

// TestWaitPageInterval 验证每页间隔的行为：
//   - 0（含未配置）不限速、立即返回；
//   - >0 时确实等待约配置时长；
//   - 等待期间 ctx 被取消（暂停 / 停机）能立即醒来，不傻等整个间隔；
//   - 每轮读最新配置——设置页在线改间隔后，下一次调用即用新值。
func TestWaitPageInterval(t *testing.T) {
	base := config.Default()
	base.Collector.PageIntervalMs = 0
	svc := &CrawlService{cfg: base.Collector}

	// 0 = 不限速：应立即返回。
	start := time.Now()
	if err := svc.waitPageInterval(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatalf("page_interval_ms=0 应立即返回，实际耗时 %v", time.Since(start))
	}

	// >0：等待约配置时长（允许调度误差）。
	svc.cfg.PageIntervalMs = 120
	start = time.Now()
	if err := svc.waitPageInterval(context.Background()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("page_interval_ms=120 应至少等待约 120ms，实际仅 %v", elapsed)
	}

	// 等待期间取消 ctx：应立即返回 ctx.Err()，不傻等 5 秒。
	svc.cfg.PageIntervalMs = 5000
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	start = time.Now()
	err := svc.waitPageInterval(ctx)
	if err == nil {
		t.Fatal("ctx 取消时 waitPageInterval 应返回错误")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("ctx 取消后应立即醒来，实际傻等了 %v", elapsed)
	}

	// 热更新：注入 store 后每轮读最新值，改配置立即生效。
	store := config.NewStore(config.Default())
	svc.store = store
	cc := store.Collector()
	cc.PageIntervalMs = 0
	if _, err := store.UpdateCollector(cc); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	if err := svc.waitPageInterval(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatalf("store 里 page_interval_ms=0 应立即返回")
	}
	// 在线改成 120ms，下一次调用应读到新值并等待。
	cc = store.Collector()
	cc.PageIntervalMs = 120
	if _, err := store.UpdateCollector(cc); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	if err := svc.waitPageInterval(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 100*time.Millisecond {
		t.Fatalf("在线改间隔后应读到新值并等待，实际仅 %v", time.Since(start))
	}
}
