package collector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/model"
)

// TestParseTypeList 验证 ac=typelist 响应的解析：code==1 成功解析、含父子关系。
func TestParseTypeList(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"code": 1, "msg": "", "page": 1, "pagecount": 1,
		"list": []map[string]interface{}{
			{"type_id": "1", "type_name": "电影", "type_pid": "0"},
			{"type_id": "6", "type_name": "动作", "type_pid": "1"},
			{"type_id": "7", "type_name": "喜剧", "type_pid": "1"},
		},
	})
	r, err := ParseTypeList(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.List) != 3 {
		t.Fatalf("应解析出 3 个分类，实际 %d", len(r.List))
	}
	if r.List[0].TypeID != "1" || r.List[0].TypeName != "电影" || r.List[0].TypePID != "0" {
		t.Fatalf("首条分类解析异常: %+v", r.List[0])
	}
	if r.List[1].TypePID != "1" {
		t.Fatalf("二级分类父级关系解析异常: %+v", r.List[1])
	}
}

// TestParseTypeListNonSuccess 验证非成功状态被识别为错误。
func TestParseTypeListNonSuccess(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"code": -1, "msg": "参数错误", "list": []map[string]interface{}{},
	})
	if _, err := ParseTypeList(body); err == nil {
		t.Fatal("code != 1 应返回错误")
	}
}

// TestFetchTypeList 验证 FetchTypeList 通过真实 HTTP 请求拉到分类定义，
// 且 BuildTypeListURL 强制 ac=typelist（覆盖附加参数里的 videolist）。
func TestFetchTypeList(t *testing.T) {
	var gotAC, gotExtra string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAC = r.URL.Query().Get("ac")
		gotExtra = r.URL.Query().Get("t")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "", "page": 1, "pagecount": 1,
			"list": []map[string]interface{}{
				{"type_id": "1", "type_name": "电影", "type_pid": "0"},
				{"type_id": "6", "type_name": "动作", "type_pid": "1"},
			},
		})
	}))
	defer srv.Close()

	src := model.Source{
		APIBase:     srv.URL + "/api.php/provide/vod/",
		ExtraParams: "ac=videolist&t=1",
	}
	f := NewFetcher(config.Default().Collector)
	resp, err := f.FetchTypeList(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if gotAC != "typelist" {
		t.Fatalf("应强制 ac=typelist，实际 %q", gotAC)
	}
	if gotExtra != "1" {
		t.Fatalf("附加参数 t=1 应被保留，实际 %q", gotExtra)
	}
	if len(resp.List) != 2 || resp.List[0].TypeName != "电影" {
		t.Fatalf("typelist 解析结果异常: %+v", resp.List)
	}
}

// TestFetchTypeListError 验证分类接口异常时返回错误（调用方按非致命处理）。
func TestFetchTypeListError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	src := model.Source{APIBase: srv.URL + "/api.php/provide/vod/"}
	f := NewFetcher(config.Default().Collector)
	if _, err := f.FetchTypeList(context.Background(), src); err == nil {
		t.Fatal("分类接口 500 应返回错误")
	}
}
