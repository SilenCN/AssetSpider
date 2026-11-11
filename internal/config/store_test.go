package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleConfig = `# 顶部注释要保留
server:
  addr: ":8080"
database:
  path: "data/assetspider.db"
web:
  dir: "web/dist"
collector:
  # 请求超时注释
  timeout_seconds: 20
  max_pages: 1000          # 行尾注释
  page_size: 0
  user_agent: "AssetSpider/1.0"
  default_incremental_hours: 24
  max_concurrent: 3
  retry_times: 2
  retry_backoff_seconds: 3
  progress_save_seconds: 60
auth:
  token_ttl_hours: 168
  bcrypt_cost: 10
`

func writeSample(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(sampleConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStoreUpdateCollectorPersistsAndKeepsComments(t *testing.T) {
	path := writeSample(t)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(cfg)

	in := cfg.Collector
	in.TimeoutSeconds = 45
	in.MaxConcurrent = 7
	in.RetryTimes = 0
	in.UserAgent = "MyBot/2.0"
	in.PageIntervalMs = 1500
	got, err := store.UpdateCollector(in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Collector.TimeoutSeconds != 45 || got.Collector.MaxConcurrent != 7 ||
		got.Collector.RetryTimes != 0 || got.Collector.UserAgent != "MyBot/2.0" ||
		got.Collector.PageIntervalMs != 1500 {
		t.Fatalf("内存快照未更新: %+v", got.Collector)
	}
	// 即时生效：读路径拿到新值。
	if store.Collector().MaxConcurrent != 7 {
		t.Fatalf("Collector() 应读到最新值，实际 %d", store.Collector().MaxConcurrent)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"顶部注释要保留", "请求超时注释", "行尾注释",
		"timeout_seconds: 45", "max_concurrent: 7", "retry_times: 0", `user_agent: MyBot/2.0`,
		"page_interval_ms: 1500"} {
		if !strings.Contains(text, want) {
			t.Errorf("写回后缺少 %q，实际内容:\n%s", want, text)
		}
	}
	// 未改动的段与键保持原值。
	if !strings.Contains(text, `addr: ":8080"`) || !strings.Contains(text, "bcrypt_cost: 10") {
		t.Errorf("非 collector 段被破坏:\n%s", text)
	}

	// 重新加载后新值应恢复。
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Collector.TimeoutSeconds != 45 || reloaded.Collector.MaxConcurrent != 7 ||
		reloaded.Collector.PageIntervalMs != 1500 {
		t.Fatalf("重启后未恢复在线修改: %+v", reloaded.Collector)
	}
}

func TestStoreWriteBackAddsMissingKeys(t *testing.T) {
	// 模拟老配置：collector 段缺少 retry_* 两个键。
	path := filepath.Join(t.TempDir(), "old.yaml")
	old := strings.ReplaceAll(sampleConfig, "  retry_times: 2\n  retry_backoff_seconds: 3\n", "")
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(cfg)
	in := cfg.Collector // 缺省 retry 会由 Default 补全，这里显式写回
	if _, err := store.UpdateCollector(in); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	for _, key := range []string{"retry_times:", "retry_backoff_seconds:"} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("应补齐缺失的键 %s，实际:\n%s", key, raw)
		}
	}
}

func TestStoreRejectsInvalidCollector(t *testing.T) {
	store := NewStore(Default())
	in := Default().Collector
	in.MaxConcurrent = 0
	if _, err := store.UpdateCollector(in); err == nil {
		t.Fatal("非法的 max_concurrent 应被拒绝")
	}
	in = Default().Collector
	in.TimeoutSeconds = -1
	if _, err := store.UpdateCollector(in); err == nil {
		t.Fatal("非法的 timeout_seconds 应被拒绝")
	}
}

func TestStoreLocksEnvOverriddenFields(t *testing.T) {
	path := writeSample(t)
	t.Setenv("ASSETSPIDER_COLLECTOR_MAX_CONCURRENT", "9")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(cfg)
	if got := store.EnvOverridden(); len(got) != 1 || got[0] != "max_concurrent" {
		t.Fatalf("应标记 max_concurrent 被 env 覆盖，实际 %v", got)
	}
	// 只改其它字段：允许。
	in := cfg.Collector
	in.TimeoutSeconds = 33
	if _, err := store.UpdateCollector(in); err != nil {
		t.Fatalf("改非 env 字段应允许: %v", err)
	}
	// 试图改被 env 接管的字段：拒绝，且不落盘。
	in = store.Collector()
	in.MaxConcurrent = 12
	if _, err := store.UpdateCollector(in); err == nil {
		t.Fatal("改动 env 覆盖的字段应被拒绝")
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "max_concurrent: 12") {
		t.Fatalf("被拒绝的修改不应落盘:\n%s", raw)
	}
}

func TestStoreNoPersistenceWithoutPath(t *testing.T) {
	// 测试环境用 Default()（path 为空）：只更新内存，不报错。
	store := NewStore(Default())
	in := Default().Collector
	in.MaxPages = 42
	got, err := store.UpdateCollector(in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Collector.MaxPages != 42 {
		t.Fatalf("无路径时也应更新内存: %+v", got.Collector)
	}
}
