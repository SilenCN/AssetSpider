package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
)

// TestCrawlBuildsParentCategoriesFromClass 复刻真实站点响应：
// ac=typelist 也被当成影片列表返回，但响应里带 class[] 分类定义（type_id /
// type_pid 是数字）。一级分类是纯容器、自身没有影片，必须靠 class[] 才能建出
// 带真实名称的一级分类，否则二级分类会全部落到「未归类」。
func TestCrawlBuildsParentCategoriesFromClass(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "数据列表", "page": 1, "pagecount": 1,
			"limit": "20", "total": 2,
			"list": []map[string]interface{}{
				{"vod_id": 1, "vod_name": "剧集A", "type_id": 26, "type_name": "国产剧",
					"type_id_1": 8, "vod_time": "2026-11-07 10:00:00",
					"vod_play_from": "m3u8", "vod_play_url": "第1集$https://x/1.m3u8"},
				{"vod_id": 2, "vod_name": "动漫A", "type_id": 1, "type_name": "国产动漫",
					"type_id_1": 40, "vod_time": "2026-11-07 10:00:00",
					"vod_play_from": "m3u8", "vod_play_url": "第1集$https://x/2.m3u8"},
			},
			"class": []map[string]interface{}{
				{"type_id": 8, "type_pid": 0, "type_name": "连续剧"},
				{"type_id": 40, "type_pid": 0, "type_name": "动漫"},
				{"type_id": 26, "type_pid": 8, "type_name": "国产剧"},
				{"type_id": 1, "type_pid": 40, "type_name": "国产动漫"},
			},
		})
	}))
	defer srv.Close()

	st, svc := newCrawlService(t)
	id := addSource(t, st, srv.URL+"/api.php/provide/vod/", true)
	if _, err := svc.RunOne(context.Background(), id, model.ModeFull); err != nil {
		t.Fatal(err)
	}
	cats, err := st.Categories.List(id)
	if err != nil {
		t.Fatal(err)
	}
	byType := make(map[string]model.Category)
	for _, c := range cats {
		byType[c.TypeID] = c
	}
	for _, tc := range []struct{ id, name string }{{"8", "连续剧"}, {"40", "动漫"}} {
		p, ok := byType[tc.id]
		if !ok {
			t.Fatalf("未建出一级分类 %s: %+v", tc.id, cats)
		}
		if p.Name != tc.name || p.Level != 1 || p.ParentID != 0 {
			t.Fatalf("一级分类 %s 应带权威名称且为一级: %+v", tc.id, p)
		}
	}
	for _, tc := range []struct{ child, parent string }{{"26", "8"}, {"1", "40"}} {
		ch, p := byType[tc.child], byType[tc.parent]
		if ch.ParentID == 0 || ch.ParentID != p.ID || ch.Level != 2 {
			t.Fatalf("二级分类 %s 未挂到 %s: %+v", tc.child, tc.parent, ch)
		}
	}
}
