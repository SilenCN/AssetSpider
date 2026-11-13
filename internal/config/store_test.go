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

// clearDBEnv 清除数据库相关的环境变量，避免部署/测试沙箱里预设的
// ASSETSPIDER_DB_* 变量干扰「是否被环境变量覆盖」的断言（与 collector 的 t.Setenv 同理）。
func clearDBEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"ASSETSPIDER_DB_DRIVER", "ASSETSPIDER_DB_PATH", "ASSETSPIDER_DB_DSN",
		"ASSETSPIDER_DB_MAX_OPEN_CONNS", "ASSETSPIDER_DB_MAX_IDLE_CONNS", "ASSETSPIDER_DB_CONN_MAX_LIFETIME",
	} {
		t.Setenv(name, "")
	}
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
	clearDBEnv(t)
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

func TestStoreUpdateDatabasePersistsAndKeepsComments(t *testing.T) {
	path := writeSample(t)
	clearDBEnv(t)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(cfg)

	in := cfg.Database
	in.Driver = "mysql"
	in.DSN = "u:p@tcp(127.0.0.1:3306)/assetspider?charset=utf8mb4&parseTime=True&loc=Local"
	in.MaxOpenConns = 20
	got, err := store.UpdateDatabase(in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Database.Driver != "mysql" || got.Database.DSN != in.DSN || got.Database.MaxOpenConns != 20 {
		t.Fatalf("内存快照未更新: %+v", got.Database)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"顶部注释要保留",
		"driver: mysql",
		"max_open_conns: 20",
		"dsn: u:p@tcp(127.0.0.1:3306)/assetspider?charset=utf8mb4&parseTime=True&loc=Local",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("写回后缺少 %q，实际内容:\n%s", want, text)
		}
	}
	// 老配置缺 driver/dsn/pool 键，应补齐。
	for _, key := range []string{"driver:", "dsn:", "max_open_conns:", "max_idle_conns:", "conn_max_lifetime_minutes:"} {
		if !strings.Contains(text, key) {
			t.Errorf("应补齐缺失的数据库键 %s，实际:\n%s", key, text)
		}
	}
	// 非 database 段保持原值。
	if !strings.Contains(text, `addr: ":8080"`) || !strings.Contains(text, "bcrypt_cost: 10") {
		t.Errorf("非 database 段被破坏:\n%s", text)
	}

	// 重新加载后新值应恢复。
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Database.Driver != "mysql" || reloaded.Database.MaxOpenConns != 20 {
		t.Fatalf("重启后未恢复在线修改: %+v", reloaded.Database)
	}
}

func TestStoreUpdateDatabaseKeepsDSNWhenBlank(t *testing.T) {
	path := writeSample(t)
	clearDBEnv(t)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(cfg)
	// 先写入一个 MySQL DSN。
	in := cfg.Database
	in.Driver = "mysql"
	in.DSN = "u:p@tcp(h:3306)/db"
	if _, err := store.UpdateDatabase(in); err != nil {
		t.Fatal(err)
	}
	// 再次提交时 DSN 留空：应沿用现有连接串，不误清空。
	next := store.Current().Database
	next.DSN = ""
	got, err := store.UpdateDatabase(next)
	if err != nil {
		t.Fatalf("留空 DSN 应沿用现有连接串: %v", err)
	}
	if got.Database.DSN != "u:p@tcp(h:3306)/db" {
		t.Fatalf("DSN 被误清空: %q", got.Database.DSN)
	}
}

func TestStoreUpdateDatabaseRejectsInvalid(t *testing.T) {
	clearDBEnv(t)
	store := NewStore(Default())
	// MySQL 缺 DSN 应被拒绝。
	in := Default().Database
	in.Driver = "mysql"
	in.DSN = ""
	if _, err := store.UpdateDatabase(in); err == nil {
		t.Fatal("MySQL 缺 DSN 应被拒绝")
	}
}

func TestStoreLocksEnvOverriddenDatabaseFields(t *testing.T) {
	path := writeSample(t)
	clearDBEnv(t)
	t.Setenv("ASSETSPIDER_DB_DSN", "env-provided-dsn")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(cfg)
	if got := store.EnvOverridden(); !contains(got, "dsn") {
		t.Fatalf("应标记 dsn 被 env 覆盖，实际 %v", got)
	}
	// 试图改被 env 接管的字段：拒绝，且不落盘。
	in := store.Current().Database
	in.DSN = "different"
	if _, err := store.UpdateDatabase(in); err == nil {
		t.Fatal("改动 env 覆盖的数据库字段应被拒绝")
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "different") {
		t.Fatalf("被拒绝的修改不应落盘:\n%s", raw)
	}
}

// TestStoreAllowsBlankDSNWhenEnvLocked 验证 MySQL 下 dsn 被环境变量接管时，页面提交
// 留空 DSN（表示「保留现有连接串」）不算改动，不应被拒绝。
func TestStoreAllowsBlankDSNWhenEnvLocked(t *testing.T) {
	clearDBEnv(t)
	t.Setenv("ASSETSPIDER_DB_DRIVER", "mysql")
	t.Setenv("ASSETSPIDER_DB_DSN", "env-provided-dsn")
	cfg, err := Load("") // 空路径：默认值 + 环境变量覆盖（mysql 的 dsn 由 env 提供）
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(cfg)
	if store.Current().Database.Driver != "mysql" {
		t.Fatalf("driver 应由 env 覆盖为 mysql")
	}
	in := store.Current().Database
	in.DSN = "" // 留空 = 保留现有
	if _, err := store.UpdateDatabase(in); err != nil {
		t.Fatalf("留空 DSN 不应被拒绝: %v", err)
	}
	if got := store.Current().Database.DSN; got != "env-provided-dsn" {
		t.Fatalf("DSN 应保持环境变量值，实际 %q", got)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
