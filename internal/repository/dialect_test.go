package repository

import (
	"path/filepath"
	"testing"
)

// TestTestConnectionSQLite 验证 SQLite 路径可建连并 Ping（含自动创建父目录）。
func TestTestConnectionSQLite(t *testing.T) {
	dir := t.TempDir()
	// 父目录尚不存在，TestConnection 应自行创建并打开。
	path := filepath.Join(dir, "nested", "probe.db")
	if err := TestConnection(Options{Driver: "sqlite", Path: path}); err != nil {
		t.Fatalf("SQLite 测试连接失败: %v", err)
	}
}

// TestTestConnectionMySQLEmptyDSN 验证缺 DSN 时 DSN 规范化阶段即报错，不发起连接。
func TestTestConnectionMySQLEmptyDSN(t *testing.T) {
	if err := TestConnection(Options{Driver: "mysql", DSN: ""}); err == nil {
		t.Fatal("MySQL 缺 DSN 应返回错误")
	}
}

// TestTestConnectionMySQLBadDSN 验证 DSN 格式非法时 ParseDSN 阶段报错。
func TestTestConnectionMySQLBadDSN(t *testing.T) {
	if err := TestConnection(Options{Driver: "mysql", DSN: "@@@"}); err == nil {
		t.Fatal("非法 MySQL DSN 应返回错误")
	}
}
