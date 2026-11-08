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

// newTestServer 返回未经认证的原始路由，供鉴权与权限隔离测试直接使用。
func newTestServer(t *testing.T) (*gin.Engine, string) {
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
	cfg := config.Default()
	cfg.Web.Dir = webDir
	cfg.Auth.BCryptCost = 4 // 加快测试中的密码哈希
	return New(s, cfg).Router(), dbPath
}

// authedHandler 自动为 /api 请求附加管理员令牌，
// 让原有业务测试无需逐个改动即可通过鉴权（鉴权本身由 auth_test.go 单独验证）。
type authedHandler struct {
	http.Handler
	token string
}

func (h *authedHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if strings.HasPrefix(req.URL.Path, "/api/") && req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	h.Handler.ServeHTTP(w, req)
}

func loginForToken(t *testing.T, r http.Handler, username, password string) string {
	t.Helper()
	w := doAuthJSON(r, http.MethodPost, "/api/auth/login", "",
		map[string]string{"username": username, "password": password})
	if w.Code != http.StatusOK {
		t.Fatalf("登录失败: %d %s", w.Code, w.Body.String())
	}
	var d struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Token == "" {
		t.Fatal("登录响应缺少 token")
	}
	return d.Token
}

func newTestRouter(t *testing.T) (http.Handler, string) {
	t.Helper()
	r, dbPath := newTestServer(t)
	return &authedHandler{Handler: r, token: loginForToken(t, r, "admin", "pass123")}, dbPath
}

func jsonBody(v interface{}) *strings.Reader {
	b, _ := json.Marshal(v)
	return strings.NewReader(string(b))
}

func doJSON(r http.Handler, method, path string, body interface{}) *httptest.ResponseRecorder {
	return doAuthJSON(r, method, path, "", body)
}

// doAuthJSON 发起带可选令牌的 JSON 请求。token 为空表示不带认证头。
func doAuthJSON(r http.Handler, method, path, token string, body interface{}) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, jsonBody(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
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

func TestVideoFieldMetaAndDetailAPI(t *testing.T) {
	r, _ := newTestRouter(t)

	// 1. 字段元数据：覆盖全部标准字段，且带中文分组信息
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/meta/fields", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("字段元数据接口异常: %d %s", w.Code, w.Body.String())
	}
	var meta struct {
		Total  int                  `json:"total"`
		Fields []model.VodFieldMeta `json:"fields"`
		Groups []struct {
			Key   string `json:"key"`
			Label string `json:"label"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Total < 90 || len(meta.Fields) != meta.Total || len(meta.Groups) == 0 {
		t.Fatalf("字段元数据不完整: total=%d fields=%d groups=%d", meta.Total, len(meta.Fields), len(meta.Groups))
	}
	foundPlayURL := false
	for _, f := range meta.Fields {
		if f.Key == "vod_play_url" {
			foundPlayURL = f.Json == "vodPlayUrl" && f.Label != ""
		}
	}
	if !foundPlayURL {
		t.Fatal("vod_play_url 元数据缺失或前端键不正确")
	}

	// 2. 采集一条数据，确认 API 返回完整字段、原始 JSON 与详情接口
	site := newMockVideoSite(t)
	w = doJSON(r, http.MethodPost, "/api/sources", map[string]interface{}{
		"name": "字段测试站", "apiBase": site.URL + "/api.php/provide/vod/",
		"format": "seacms_json", "enabled": true, "incrementalHours": 24,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(r, http.MethodPost, "/api/sources/1/crawl?mode=full", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("采集失败: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/videos", nil))
	var list struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("入库条数异常: %d", len(list.Items))
	}
	v := list.Items[0]
	if v["vodName"] == nil || v["vodId"] == nil {
		t.Errorf("响应缺少结构化字段: %+v", v)
	}
	raw, _ := v["raw"].(string)
	if !strings.Contains(raw, "vod_name") || !strings.Contains(raw, "vod_id") {
		t.Errorf("响应未返回原始 JSON: %q", raw)
	}
	hasPlayURL := false
	for _, item := range list.Items {
		if r, _ := item["raw"].(string); strings.Contains(r, "vod_play_url") {
			hasPlayURL = true
		}
	}
	if !hasPlayURL {
		t.Error("原始 JSON 未保留播放地址字段")
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/videos/1", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"raw"`) {
		t.Fatalf("详情接口异常: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/videos/9999", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("不存在的影片应返回 404，实际 %d", w.Code)
	}
}
