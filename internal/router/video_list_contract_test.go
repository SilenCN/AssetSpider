package router_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestVideoListResponseHasNoTotal 锁定列表接口的响应契约：
// 只返回 items / page / size，不带 total / hasMore，也不做任何 COUNT。
func TestVideoListResponseHasNoTotal(t *testing.T) {
	r, _ := newTestRouter(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/videos/v1?page=1&size=24", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("列表接口异常: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, `"total"`) || strings.Contains(body, `"hasMore"`) {
		t.Fatalf("列表响应不应带 total / hasMore: %s", body)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"items", "page", "size"} {
		if _, ok := out[key]; !ok {
			t.Fatalf("列表响应缺少 %s: %s", key, body)
		}
	}
	if len(out) != 3 {
		t.Fatalf("列表响应应只含 3 个字段，实际 %v", out)
	}
	var payload struct {
		Page int `json:"page"`
		Size int `json:"size"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Page != 1 || payload.Size != 24 {
		t.Fatalf("分页参数应原样回传: %+v", payload)
	}
}
