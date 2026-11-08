package repository

import (
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
)

func TestAssetRepositoryCRUD(t *testing.T) {
	r := newTestRepos(t)

	items, err := r.Assets.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("新库应为空，实际 %d 条", len(items))
	}

	a := &model.Asset{Host: "example.com", IP: "1.2.3.4", Category: "domain"}
	if err := r.Assets.Create(a); err != nil {
		t.Fatal(err)
	}
	if a.ID == 0 {
		t.Fatal("Create 未回填 ID")
	}

	items, err = r.Assets.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Host != "example.com" || items[0].IP != "1.2.3.4" {
		t.Fatalf("列表内容异常: %+v", items)
	}

	if err := r.Assets.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	items, err = r.Assets.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("删除后应为空，实际 %d 条", len(items))
	}
}
