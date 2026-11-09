package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
)

// TestCrawlBackfillsParentNamesFromTypeList 端到端验证：真实 MacCMS 站点里一级分类
// 「电影」是纯容器（自身无影片），videolist 不给它的名称，只能以 type_id 兜底；
// 采集时拉取 ac=typelist 拿到权威名称，应在采集结束后把父级名称补全为「电影」，
// 同时二级分类（动作/喜剧）名称与 typelist 一致。
func TestCrawlBackfillsParentNamesFromTypeList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ac := r.URL.Query().Get("ac")
		w.Header().Set("Content-Type", "application/json")
		if ac == "typelist" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 1, "msg": "", "page": 1, "pagecount": 1,
				"list": []map[string]interface{}{
					{"type_id": "1", "type_name": "电影", "type_pid": "0"},
					{"type_id": "6", "type_name": "动作", "type_pid": "1"},
					{"type_id": "7", "type_name": "喜剧", "type_pid": "1"},
				},
			})
			return
		}
		// 默认 ac=videolist：返回两条挂在二级分类上的影片（父级「电影」自身无影片）。
		item := func(id, name, tid, tname, tid1 string) map[string]interface{} {
			return map[string]interface{}{
				"vod_id": id, "vod_name": name, "vod_time": "2026-11-07 10:00:00",
				"type_id": tid, "type_name": tname, "type_id_1": tid1,
				"vod_play_from": "m3u8", "vod_play_url": "第1集$https://x/1.m3u8",
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "数据列表", "page": 1, "pagecount": 1,
			"limit": "20", "total": 2,
			"list": []interface{}{
				item("1", "影片A", "6", "动作", "1"),
				item("2", "影片B", "7", "喜剧", "1"),
			},
		})
	}))
	defer srv.Close()

	st, svc := newCrawlService(t)
	src := &model.Source{
		Name: "MacCMS站", APIBase: srv.URL + "/api.php/provide/vod/",
		Format: "maccms_json", Enabled: true, IncrementalHours: 2,
	}
	if err := st.Sources.CreateSource(src); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunOne(context.Background(), src.ID, model.ModeFull); err != nil {
		t.Fatal(err)
	}

	cats, err := st.Categories.List(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cats) != 3 {
		t.Fatalf("应创建 3 个分类（2 二级 + 1 推断父级），实际 %d: %+v", len(cats), cats)
	}
	var parent *model.Category
	for i := range cats {
		if cats[i].TypeID == "1" {
			parent = &cats[i]
		}
	}
	if parent == nil {
		t.Fatal("未找到从 type_id_1 推断出的父级分类")
	}
	if parent.Name != "电影" {
		t.Fatalf("父级名称应被 typelist 补全为「电影」，实际 %q", parent.Name)
	}
	if parent.Level != 1 || parent.ParentID != 0 {
		t.Fatalf("父级应为一级分类且无父: %+v", parent)
	}
	for _, c := range cats {
		want := map[string]string{"6": "动作", "7": "喜剧"}[c.TypeID]
		if want != "" && c.Name != want {
			t.Errorf("二级分类 %s 名称异常: %q（期望 %q）", c.TypeID, c.Name, want)
		}
	}

	// 树结构应正确：电影(一级) 下辖 动作、喜剧。
	tree, err := NewCategoryService(st.Categories, st.Videos).Tree(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree) != 1 || tree[0].Name != "电影" || len(tree[0].Children) != 2 {
		t.Fatalf("分类树结构异常: %+v", tree)
	}
}

// TestCrawlSkipsTypeListWhenUnavailable 验证：typelist 不可用时，采集不应中断，
// 父级退回 type_id 兜底名称（旧行为），且二级分类名称仍来自 videolist。
func TestCrawlSkipsTypeListWhenUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("ac") == "typelist" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "数据列表", "page": 1, "pagecount": 1,
			"limit": "20", "total": 1,
			"list": []interface{}{
				map[string]interface{}{
					"vod_id": "1", "vod_name": "影片A", "vod_time": "2026-11-07 10:00:00",
					"type_id": "6", "type_name": "动作", "type_id_1": "1",
					"vod_play_from": "m3u8", "vod_play_url": "第1集$https://x/1.m3u8",
				},
			},
		})
	}))
	defer srv.Close()

	st, svc := newCrawlService(t)
	src := &model.Source{
		Name: "MacCMS站", APIBase: srv.URL + "/api.php/provide/vod/",
		Format: "maccms_json", Enabled: true, IncrementalHours: 2,
	}
	if err := st.Sources.CreateSource(src); err != nil {
		t.Fatal(err)
	}
	// typelist 500 时采集应仍成功完成，不被打断。
	res, err := svc.RunOne(context.Background(), src.ID, model.ModeFull)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != model.JobSuccess || res.Fetched != 1 {
		t.Fatalf("typelist 异常不应阻断采集: %+v", res)
	}
	cats, _ := st.Categories.List(src.ID)
	var parent *model.Category
	for i := range cats {
		if cats[i].TypeID == "1" {
			parent = &cats[i]
		}
	}
	if parent == nil {
		t.Fatal("未找到父级分类")
	}
	// 拿不到 typelist，父级退回 type_id 兜底展示。
	if parent.Name != "1" {
		t.Fatalf("typelist 不可用时父级应退回 type_id 兜底，实际 %q", parent.Name)
	}
}
