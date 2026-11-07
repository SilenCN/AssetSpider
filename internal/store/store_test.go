package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
)

func TestCRUD(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "asset.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if s.Path() != path {
		t.Errorf("Path() = %q, want %q", s.Path(), path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("数据库文件未创建（嵌套目录未自动建立）: %v", err)
	}

	items, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("新库应为空，实际 %d 条", len(items))
	}

	a := &model.Asset{Host: "example.com", IP: "1.2.3.4", Category: "domain"}
	if err := s.Create(a); err != nil {
		t.Fatal(err)
	}
	if a.ID == 0 {
		t.Fatal("Create 未回填 ID")
	}

	items, err = s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Host != "example.com" || items[0].IP != "1.2.3.4" {
		t.Fatalf("列表内容异常: %+v", items)
	}

	if err := s.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	items, err = s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("删除后应为空，实际 %d 条", len(items))
	}
}
