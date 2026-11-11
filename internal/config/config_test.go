package config

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestDefault(t *testing.T) {
	cfg := Default()
	if cfg.Server.Addr != ":8080" {
		t.Errorf("默认 addr = %q", cfg.Server.Addr)
	}
	if cfg.Database.Path != "data/assetspider.db" {
		t.Errorf("默认数据库路径 = %q", cfg.Database.Path)
	}
	if cfg.Web.Dir != "web/dist" {
		t.Errorf("默认页面目录 = %q", cfg.Web.Dir)
	}
	cc := cfg.Collector
	if cc.TimeoutSeconds != 20 || cc.MaxPages != 1000 || cc.DefaultHours != 24 || cc.MaxConcurrent != 3 || cc.PageSize != 0 {
		t.Errorf("采集器默认值异常: %+v", cc)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("默认配置校验失败: %v", err)
	}
}

func TestLoadYAMLAndEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// Windows 路径含反斜杠，YAML 双引号标量会把它当作转义序列；用 strconv.Quote 转义。
	body := "server:\n  addr: \"127.0.0.1:9090\"\ndatabase:\n  path: " +
		strconv.Quote(filepath.Join(dir, "custom.db")) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Addr != "127.0.0.1:9090" {
		t.Errorf("addr 未被配置文件覆盖: %q", cfg.Server.Addr)
	}
	if cfg.Web.Dir != "web/dist" {
		t.Errorf("未提供的字段应保留默认值，实际 %q", cfg.Web.Dir)
	}
	if cfg.Collector.TimeoutSeconds != 20 {
		t.Errorf("未提供的采集器字段应保留默认值，实际 %d", cfg.Collector.TimeoutSeconds)
	}

	envPath := filepath.Join(dir, "from-env.db")
	t.Setenv("ASSETSPIDER_DB_PATH", envPath)
	overridden, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if overridden.Database.Path != envPath {
		t.Errorf("环境变量未生效: %q", overridden.Database.Path)
	}
}

func TestCollectorYAMLAndEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "collector:\n  timeout_seconds: 8\n  max_pages: 37\n  default_incremental_hours: 6\n  user_agent: \"yaml-agent\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Collector.TimeoutSeconds != 8 || cfg.Collector.MaxPages != 37 ||
		cfg.Collector.DefaultHours != 6 || cfg.Collector.UserAgent != "yaml-agent" {
		t.Fatalf("YAML 采集器配置未生效: %+v", cfg.Collector)
	}
	if cfg.Collector.MaxConcurrent != 3 {
		t.Errorf("未提供的字段应保持默认，实际 %d", cfg.Collector.MaxConcurrent)
	}

	t.Setenv("ASSETSPIDER_COLLECTOR_TIMEOUT", "55")
	t.Setenv("ASSETSPIDER_COLLECTOR_PARALLEL", "2") // 旧变量名兼容：映射到并发上限
	t.Setenv("ASSETSPIDER_COLLECTOR_USER_AGENT", "env-agent")
	t.Setenv("ASSETSPIDER_COLLECTOR_PROGRESS_SAVE", "7")
	cfg2, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Collector.TimeoutSeconds != 55 || cfg2.Collector.MaxConcurrent != 2 ||
		cfg2.Collector.UserAgent != "env-agent" || cfg2.Collector.MaxPages != 37 {
		t.Fatalf("环境变量覆盖异常: %+v", cfg2.Collector)
	}
	if cfg2.Collector.ProgressSaveSeconds != 7 {
		t.Fatalf("进度落库间隔未按环境变量覆盖，实际 %d", cfg2.Collector.ProgressSaveSeconds)
	}
}

func TestCollectorValidation(t *testing.T) {
	t.Setenv("ASSETSPIDER_COLLECTOR_TIMEOUT", "0")
	if _, err := Load(""); err == nil {
		t.Fatal("timeout_seconds=0 应校验失败")
	}
	t.Setenv("ASSETSPIDER_COLLECTOR_TIMEOUT", "abc")
	if _, err := Load(""); err == nil {
		t.Fatal("非整数环境变量应报错")
	}
	t.Setenv("ASSETSPIDER_COLLECTOR_TIMEOUT", "20")
	t.Setenv("ASSETSPIDER_COLLECTOR_MAX_CONCURRENT", "0")
	if _, err := Load(""); err == nil {
		t.Fatal("max_concurrent=0 应校验失败")
	}
	t.Setenv("ASSETSPIDER_COLLECTOR_MAX_CONCURRENT", "3")
	t.Setenv("ASSETSPIDER_COLLECTOR_PROGRESS_SAVE", "0")
	if _, err := Load(""); err == nil {
		t.Fatal("progress_save_seconds=0 应校验失败")
	}
	t.Setenv("ASSETSPIDER_COLLECTOR_PROGRESS_SAVE", "60")
	t.Setenv("ASSETSPIDER_COLLECTOR_PAGE_INTERVAL", "-1")
	if _, err := Load(""); err == nil {
		t.Fatal("page_interval_ms=-1 应校验失败")
	}
	t.Setenv("ASSETSPIDER_COLLECTOR_PAGE_INTERVAL", "60001")
	if _, err := Load(""); err == nil {
		t.Fatal("page_interval_ms 超过 60000 应校验失败")
	}
}

// TestPageIntervalDefaultAndEnv 验证每页间隔默认 0（不限速）、可被环境变量覆盖，
// 且覆盖会被记为 env 锁定字段（页面只读）。
func TestPageIntervalDefaultAndEnv(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Collector.PageIntervalMs != 0 {
		t.Fatalf("page_interval_ms 默认应为 0（不限速），实际 %d", cfg.Collector.PageIntervalMs)
	}
	t.Setenv("ASSETSPIDER_COLLECTOR_PAGE_INTERVAL", "800")
	cfg, err = Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Collector.PageIntervalMs != 800 {
		t.Fatalf("环境变量未生效，实际 %d", cfg.Collector.PageIntervalMs)
	}
	if !cfg.envKeys["page_interval_ms"] {
		t.Fatal("page_interval_ms 被环境变量覆盖后应记为锁定字段")
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("配置文件不存在时应返回错误")
	}
}
