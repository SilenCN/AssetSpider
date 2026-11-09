package repository

import (
	"os"
	"path/filepath"
	"testing"
)

// newTestRepos 供本包各实体的测试共用，只负责准备一组干净的仓储对象。
func newTestRepos(t *testing.T) *Repositories {
	t.Helper()
	r, err := Open(filepath.Join(t.TempDir(), "db", "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestOpenMigratesAndAssemblesRepositories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "asset.db")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if r.Path() != path {
		t.Errorf("Path() = %q, want %q", r.Path(), path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("数据库文件未创建（嵌套目录未自动建立）: %v", err)
	}
	if r.Assets == nil || r.Sources == nil || r.Videos == nil || r.Categories == nil || r.Tags == nil || r.Jobs == nil || r.Users == nil {
		t.Fatal("各实体仓储对象必须全部装配")
	}
	// 迁移后每张表都可访问，且每个对象只服务自己的实体。
	if _, err := r.Assets.List(); err != nil {
		t.Fatalf("assets 表未迁移: %v", err)
	}
	if _, err := r.Sources.ListSources(); err != nil {
		t.Fatalf("sources 表未迁移: %v", err)
	}
	if _, _, err := r.Videos.ListVideos(NewVideoQuery(0, "", 1, 1)); err != nil {
		t.Fatalf("videos 表未迁移: %v", err)
	}
	if _, err := r.Categories.List(0); err != nil {
		t.Fatalf("categories 表未迁移: %v", err)
	}
	if _, err := r.Tags.List(0); err != nil {
		t.Fatalf("tags 表未迁移: %v", err)
	}
	if _, err := r.Jobs.ListJobs(0, 10); err != nil {
		t.Fatalf("crawl_jobs 表未迁移: %v", err)
	}
	if _, err := r.Users.CountUsers(); err != nil {
		t.Fatalf("users 表未迁移: %v", err)
	}
	if _, err := r.Users.ListSourceIDsByUsers(); err != nil {
		t.Fatalf("user_sources 表未迁移: %v", err)
	}
	if err := r.Users.PurgeExpiredSessions(); err != nil {
		t.Fatalf("sessions 表未迁移: %v", err)
	}
}
