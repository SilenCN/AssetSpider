package config

import (
	"os"
	"path/filepath"
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
	if err := cfg.Validate(); err != nil {
		t.Fatalf("默认配置校验失败: %v", err)
	}
}

func TestLoadYAMLAndEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "server:\n  addr: \"127.0.0.1:9090\"\ndatabase:\n  path: \"" + filepath.Join(dir, "custom.db") + "\"\n"
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

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("配置文件不存在时应返回错误")
	}
}
