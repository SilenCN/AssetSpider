package service

import (
	"testing"

	"github.com/SilenCN/AssetSpider/internal/config"
)

// newTestSettings 构造最小可用的 SettingsService：TestDatabase 不依赖 CrawlService，
// 因此传入零值即可，避免拉起整条采集调度链。
func newTestSettings(t *testing.T, cfg *config.Config) *SettingsService {
	t.Helper()
	store := config.NewStore(cfg)
	return NewSettingsService(store, &CrawlService{})
}

// TestSettingsGetLocksOnlyEnvFields 验证数据库段按字段粒度只读：只设置了
// ASSETSPIDER_DB_PATH 时，仅 path 被标记锁定，其余字段仍可编辑。
func TestSettingsGetLocksOnlyEnvFields(t *testing.T) {
	for _, k := range []string{
		"ASSETSPIDER_DB_DRIVER", "ASSETSPIDER_DB_DSN",
		"ASSETSPIDER_DB_MAX_OPEN_CONNS", "ASSETSPIDER_DB_MAX_IDLE_CONNS",
		"ASSETSPIDER_DB_CONN_MAX_LIFETIME",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("ASSETSPIDER_DB_PATH", "data/locked.db")
	cfg, err := config.Load("") // 空路径 = 默认值 + 环境变量覆盖
	if err != nil {
		t.Fatal(err)
	}
	svc := newTestSettings(t, cfg)

	view := svc.Get()
	if len(view.Database.Locked) != 1 || view.Database.Locked[0] != "path" {
		t.Fatalf("只应锁 path，实际 %v", view.Database.Locked)
	}
}

// TestSettingsTestDatabaseSQLite 提交空配置时以当前已保存配置为基准，SQLite 应连通。
func TestSettingsTestDatabaseSQLite(t *testing.T) {
	cfg := config.Default()
	cfg.Database = config.DatabaseConfig{Driver: "sqlite", Path: t.TempDir() + "/probe.db"}
	svc := newTestSettings(t, cfg)

	res := svc.TestDatabase(DatabaseTestInput{})
	if !res.OK {
		t.Fatalf("SQLite 测试连接应成功: %+v", res)
	}
}

// TestSettingsTestDatabaseMySQLUnreachable 沿用现有 MySQL 连接串，连接不可达应失败。
func TestSettingsTestDatabaseMySQLUnreachable(t *testing.T) {
	cfg := config.Default()
	cfg.Database = config.DatabaseConfig{
		Driver: "mysql",
		DSN:    "u:p@tcp(127.0.0.1:1)/nope?charset=utf8mb4&parseTime=True&loc=Local",
	}
	svc := newTestSettings(t, cfg)

	res := svc.TestDatabase(DatabaseTestInput{})
	if res.OK {
		t.Fatalf("不可达的 MySQL 应失败: %+v", res)
	}
	if res.Error == "" {
		t.Fatal("连接失败应带原因")
	}
}

// TestSettingsTestDatabaseMySQLBlankDSN 当前未配置连接串且提交空 DSN，校验应拦截。
func TestSettingsTestDatabaseMySQLBlankDSN(t *testing.T) {
	cfg := config.Default()
	cfg.Database = config.DatabaseConfig{Driver: "mysql", DSN: ""}
	svc := newTestSettings(t, cfg)

	res := svc.TestDatabase(DatabaseTestInput{})
	if res.OK {
		t.Fatalf("MySQL 缺连接串应失败: %+v", res)
	}
}

// TestSettingsTestDatabaseValidate 提交 mysql 但缺 dsn，应被校验拦截而非发连接。
func TestSettingsTestDatabaseValidate(t *testing.T) {
	cfg := config.Default()
	cfg.Database = config.DatabaseConfig{Driver: "sqlite", Path: "data/x.db"}
	svc := newTestSettings(t, cfg)

	res := svc.TestDatabase(DatabaseTestInput{Driver: "mysql"})
	if res.OK {
		t.Fatalf("mysql 缺 dsn 应失败: %+v", res)
	}
	if res.Error == "" {
		t.Fatal("校验失败应带原因")
	}
}
