package router_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
	routerpkg "github.com/SilenCN/AssetSpider/internal/router"
)

func TestHealthAndIndex(t *testing.T) {
	r, dbPath := newTestRouter(t)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/system/v1/health", nil))
	// 直接解析 JSON 比对，避免 Windows 路径在 JSON 中被转义成 \\ 导致字符串匹配失败。
	var health struct {
		Status   string `json:"status"`
		Database string `json:"database"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &health) != nil ||
		health.Status != "ok" || health.Database != dbPath {
		t.Fatalf("health 异常: %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "AssetSpider") {
		t.Fatalf("首页未托管 Vue 页面: %d %s", w.Code, w.Body.String())
	}

	// SPA 回退：未知的前端路由也应返回 index.html
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/some/spa/route", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("SPA 回退失败: %d", w.Code)
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/not-exist", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("未知 API 应返回 404，实际 %d", w.Code)
	}
}

func TestAssetAPI(t *testing.T) {
	r, _ := newTestRouter(t)

	w := doJSON(r, http.MethodPost, "/api/assets/v1",
		map[string]string{"host": "example.com", "ip": "1.2.3.4", "category": "domain"})
	if w.Code != http.StatusCreated {
		t.Fatalf("创建失败: %d %s", w.Code, w.Body.String())
	}
	var created model.Asset
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 {
		t.Fatal("响应缺少 ID")
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/assets/v1", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("列表失败: %d %s", w.Code, w.Body.String())
	}
	var items []model.Asset
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Host != "example.com" {
		t.Fatalf("列表内容异常: %+v", items)
	}

	// 参数校验
	w = doJSON(r, http.MethodPost, "/api/assets/v1", map[string]string{"ip": "1.2.3.4"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("空 host 应返回 400，实际 %d", w.Code)
	}

	// 删除
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodDelete,
		"/api/assets/v1/"+strconv.Itoa(int(created.ID)), nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("删除失败: %d %s", w.Code, w.Body.String())
	}
}

func TestSourceValidation(t *testing.T) {
	r, _ := newTestRouter(t)
	cases := []map[string]interface{}{
		{"name": "", "apiBase": "https://a.com/api.php"},
		{"name": "x", "apiBase": "not-a-url"},
		{"name": "x", "apiBase": "https://a.com/api.php", "format": "xml"},
		{"name": "x", "apiBase": "https://a.com/api.php", "incrementalHours": -1},
	}
	for i, c := range cases {
		if w := doJSON(r, http.MethodPost, "/api/sources/v1", c); w.Code != http.StatusBadRequest {
			t.Errorf("用例 %d 应返回 400，实际 %d %s", i, w.Code, w.Body.String())
		}
	}
}

func TestAPIRoutesFollowModuleVersionConvention(t *testing.T) {
	r, _ := newTestRouter(t)
	modules := map[string]bool{
		"system": true, "assets": true, "sources": true,
		"crawls": true, "videos": true, "categories": true, "tags": true, "areas": true,
		"meta": true, "jobs": true, "tasks": true,
		"auth": true, "users": true, "settings": true,
	}
	forbidden := map[string]bool{
		"crawl": true, "crawls-run": true, "create": true, "update": true,
		"delete": true, "list": true, "get": true, "run": true,
	}
	count := 0
	for _, route := range r.Routes() {
		if !strings.HasPrefix(route.Path, "/api/") {
			continue
		}
		count++
		parts := strings.Split(strings.TrimPrefix(route.Path, "/api/"), "/")
		if len(parts) < 2 {
			t.Errorf("%s %s 不符合 /api/{模块名}/{版本号} 结构", route.Method, route.Path)
			continue
		}
		if !modules[parts[0]] {
			t.Errorf("未知模块名 %q（%s），模块应为复数资源名词", parts[0], route.Path)
		}
		if parts[1] != routerpkg.APIVersion {
			t.Errorf("%s 的版本段应为 %s，实际 %q", route.Path, routerpkg.APIVersion, parts[1])
		}
		for _, seg := range parts[2:] {
			if forbidden[seg] {
				t.Errorf("%s 的路径段 %q 是动词，应由 HTTP 动词表达", route.Path, seg)
			}
		}
	}
	if count < 16 {
		t.Errorf("注册的 v1 接口数量异常: %d", count)
	}
}

func TestLegacyRoutesAreGone(t *testing.T) {
	r, _ := newTestRouter(t)
	old := []struct{ method, path string }{
		{http.MethodGet, "/api/health"},
		{http.MethodGet, "/api/sources"},
		{http.MethodGet, "/api/videos"},
		{http.MethodGet, "/api/meta/fields"},
		{http.MethodGet, "/api/jobs"},
		{http.MethodPost, "/api/crawl?mode=full"},
		{http.MethodPost, "/api/sources/1/crawl?mode=full"},
	}
	for _, o := range old {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authReq(o.method, o.path, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("旧路由 %s 应返回 404，实际 %d", o.path, w.Code)
		}
	}
}

func TestCrawlModeFromJSONBody(t *testing.T) {
	r, _ := newTestRouter(t)
	site := newMockVideoSite(t)
	w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "body-mode", "apiBase": site.URL + "/api.php/provide/vod/", "enabled": true,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}

	w = doJSON(r, http.MethodPost, "/api/sources/v1/1/crawls", map[string]string{"mode": "full"})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"mode":"full"`) {
		t.Fatalf("单站点采集应支持 body 传 mode: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(r, http.MethodPost, "/api/crawls/v1", map[string]string{"mode": "incremental"})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"mode":"incremental"`) {
		t.Fatalf("批量采集应支持 body 传 mode: %d %s", w.Code, w.Body.String())
	}
}

// TestVideoPlayGroupsEndpoint 验证 /api/videos/v1/:id/play-groups 按苹果CMS 字段
// 解析播放组并以结构化 JSON 返回；不存在的影片返回 404。
