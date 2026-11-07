package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/store"
	"github.com/gin-gonic/gin"
)

func newTestRouter(t *testing.T) (*gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "api.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	webDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"),
		[]byte("<div id=\"app\">AssetSpider</div>"), 0o600); err != nil {
		t.Fatal(err)
	}
	return New(s, webDir, config.Default().Collector).Router(), dbPath
}

func jsonBody(v interface{}) *strings.Reader {
	b, _ := json.Marshal(v)
	return strings.NewReader(string(b))
}

func doJSON(r *gin.Engine, method, path string, body interface{}) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, jsonBody(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func newMockVideoSite(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "数据列表", "page": 1, "pagecount": 1,
			"limit": "20", "total": 2,
			"list": []map[string]interface{}{
				{"vod_id": 101, "vod_name": "测试影片A", "type_name": "动作片",
					"vod_time": "2026-11-07 10:00:00", "vod_play_url": "https://x/a.m3u8"},
				{"vod_id": "102", "vod_name": "测试影片B", "type_name": "喜剧片",
					"vod_time": "2026-11-07 11:00:00"},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHealthAndIndex(t *testing.T) {
	r, dbPath := newTestRouter(t)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), dbPath) {
		t.Fatalf("health 异常: %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "AssetSpider") {
		t.Fatalf("首页未托管 Vue 页面: %d %s", w.Code, w.Body.String())
	}

	// SPA 回退：未知的前端路由也应返回 index.html
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/some/spa/route", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("SPA 回退失败: %d", w.Code)
	}
}

func TestAssetAPI(t *testing.T) {
	r, _ := newTestRouter(t)

	w := doJSON(r, http.MethodPost, "/api/assets",
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
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/assets", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("列表失败: %d", w.Code)
	}
	var items []model.Asset
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Host != "example.com" {
		t.Fatalf("列表内容异常: %+v", items)
	}

	// 参数校验
	w = doJSON(r, http.MethodPost, "/api/assets", map[string]string{"ip": "1.2.3.4"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("空 host 应返回 400，实际 %d", w.Code)
	}

	// 删除
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete,
		"/api/assets/"+strconv.Itoa(int(created.ID)), nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("删除失败: %d", w.Code)
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
		if w := doJSON(r, http.MethodPost, "/api/sources", c); w.Code != http.StatusBadRequest {
			t.Errorf("用例 %d 应返回 400，实际 %d %s", i, w.Code, w.Body.String())
		}
	}
}

func TestSourceAndCrawlAPI(t *testing.T) {
	r, _ := newTestRouter(t)
	site := newMockVideoSite(t)
	apiBase := site.URL + "/api.php/provide/vod/"
	payload := map[string]interface{}{
		"name": "模拟资源站", "apiBase": apiBase, "format": "seacms_json",
		"extraParams": "t=1", "enabled": true, "incrementalHours": 24, "maxPages": 10,
	}

	// 1. 创建站点
	w := doJSON(r, http.MethodPost, "/api/sources", payload)
	if w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}
	var src model.Source
	if err := json.Unmarshal(w.Body.Bytes(), &src); err != nil {
		t.Fatal(err)
	}
	if src.ID == 0 || !src.Enabled {
		t.Fatalf("创建结果异常: %+v", src)
	}

	// 2. 列表附带影片数
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/sources", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"videoCount":0`) {
		t.Fatalf("站点列表异常: %d %s", w.Code, w.Body.String())
	}

	// 3. 全量采集并入库
	w = doJSON(r, http.MethodPost, "/api/sources/"+strconv.Itoa(int(src.ID))+"/crawl?mode=full", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("全量采集失败: %d %s", w.Code, w.Body.String())
	}
	var first struct {
		New       int `json:"new"`
		Fetched   int `json:"fetched"`
		Unchanged int `json:"unchanged"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.Fetched != 2 || first.New != 2 {
		t.Fatalf("首次全量应新增 2 条: %+v", first)
	}

	// 4. 数据入库可查询
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/videos?q=影片A", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("视频查询失败: %d %s", w.Code, w.Body.String())
	}
	var videos struct {
		Total int           `json:"total"`
		Items []model.Video `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &videos); err != nil {
		t.Fatal(err)
	}
	if videos.Total != 1 || len(videos.Items) != 1 || videos.Items[0].VodID != "101" {
		t.Fatalf("入库数据异常: %+v", videos)
	}

	// 5. 再次全量 → 全部去重
	w = doJSON(r, http.MethodPost, "/api/sources/"+strconv.Itoa(int(src.ID))+"/crawl?mode=full", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"new":0`) ||
		!strings.Contains(w.Body.String(), `"unchanged":2`) {
		t.Fatalf("重复采集未去重: %d %s", w.Code, w.Body.String())
	}

	// 6. 批量增量采集
	w = doJSON(r, http.MethodPost, "/api/crawl?mode=incremental", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"mode":"incremental"`) {
		t.Fatalf("批量采集失败: %d %s", w.Code, w.Body.String())
	}

	// 7. 采集记录
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/jobs", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("采集记录查询失败: %d", w.Code)
	}
	var jobs []model.CrawlJob
	if err := json.Unmarshal(w.Body.Bytes(), &jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 3 || jobs[0].Status != model.JobSuccess {
		t.Fatalf("采集记录异常: %d %+v", len(jobs), jobs)
	}

	// 8. 参数与资源校验
	if got := doJSON(r, http.MethodPost, "/api/sources/"+strconv.Itoa(int(src.ID))+"/crawl?mode=bad", nil); got.Code != http.StatusBadRequest {
		t.Errorf("非法模式应返回 400，实际 %d", got.Code)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/sources/9999/crawl?mode=full", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("不存在的站点应返回 404，实际 %d", w.Code)
	}

	// 9. 更新与级联删除
	payload["name"] = "改名后的资源站"
	if w = doJSON(r, http.MethodPut, "/api/sources/"+strconv.Itoa(int(src.ID)), payload); w.Code != http.StatusOK {
		t.Fatalf("更新站点失败: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/sources/"+strconv.Itoa(int(src.ID)), nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("删除站点失败: %d", w.Code)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/videos", nil))
	if !strings.Contains(w.Body.String(), `"total":0`) {
		t.Fatalf("删除站点后影片未级联清理: %s", w.Body.String())
	}
}
