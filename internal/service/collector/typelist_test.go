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

// TestParseListClassNumericTypeIDs 验证真实站点把分类定义放在影片响应的
// class[] 里、且 type_id / type_pid 是数字时也能解析出来。
func TestParseListClassNumericTypeIDs(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"code": 1, "msg": "数据列表", "page": 1, "pagecount": 1, "limit": "20", "total": 1,
		"list": []map[string]interface{}{
			{"vod_id": 1, "vod_name": "剧集A", "type_id": 26, "type_name": "国产剧"},
		},
		"class": []map[string]interface{}{
			{"type_id": 8, "type_pid": 0, "type_name": "连续剧"},
			{"type_id": 26, "type_pid": 8, "type_name": "国产剧"},
		},
	})
	r, err := ParseList(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Class) != 2 {
		t.Fatalf("应解析出 2 条 class 分类定义，实际 %d", len(r.Class))
	}
	if r.Class[0].TypeID.String() != "8" || r.Class[0].TypePID.String() != "0" || r.Class[0].TypeName.String() != "连续剧" {
		t.Fatalf("数字 type_id/type_pid 解析异常: %+v", r.Class[0])
	}
	if r.Class[1].TypePID.String() != "8" {
		t.Fatalf("class 父子关系解析异常: %+v", r.Class[1])
	}
}

// TestParseTypeListClassFallback 验证 ac=typelist 返回影片列表 + class[] 时，
// class[] 仍能被取到（TypeItem 兼容数字字段，不会解析失败）。
func TestParseTypeListClassFallback(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"code": 1, "msg": "数据列表",
		"list":  []map[string]interface{}{{"vod_id": 1, "type_id": 26, "type_name": "国产剧"}},
		"class": []map[string]interface{}{{"type_id": 8, "type_pid": 0, "type_name": "连续剧"}},
	})
	r, err := ParseTypeList(body)
	if err != nil {
		t.Fatalf("数字 type_id 不应导致解析失败: %v", err)
	}
	if len(r.Class) != 1 || r.Class[0].TypeName.String() != "连续剧" {
		t.Fatalf("class[] 未被解析: %+v", r)
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
