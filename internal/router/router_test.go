package router_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SilenCN/AssetSpider/internal/app"
	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
	routerpkg "github.com/SilenCN/AssetSpider/internal/router"
	"github.com/SilenCN/AssetSpider/internal/service"
	"github.com/gin-gonic/gin"
)

// testToken 是上一个测试路由里首个登录者（自动注册为管理员）的令牌。
// 测试串行执行，每个用例都会用独立数据库重建令牌。
var testToken string

// authReq 构造自动携带管理员令牌的请求，让既有接口测试在启用登录校验后仍然有效。
func authReq(method, path string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, path, body)
	if testToken != "" {
		req.Header.Set("Authorization", "Bearer "+testToken)
	}
	return req
}

func newTestRouter(t *testing.T) (*gin.Engine, string) {
	t.Helper()
	return newTestRouterWith(t, nil)
}

// newTestRouterWith 允许用例调整采集器配置（如自动重试次数与退避时长）。
func newTestRouterWith(t *testing.T, tune func(*config.Config)) (*gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "api.db")

	webDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"),
		[]byte("<div id=\"app\">AssetSpider</div>"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Database.Path = dbPath
	cfg.Web.Dir = webDir
	if tune != nil {
		tune(cfg)
	}
	application, err := app.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { application.Close() })

	// 每个测试库都是全新的：先通过登录接口注册首个管理员，
	// 之后本文件里的所有请求由 authReq 自动携带该令牌。
	loginReq, err := http.NewRequest(http.MethodPost, "/api/auth/v1/sessions",
		jsonBody(map[string]string{"username": "admin", "password": "admin123456"}))
	if err != nil {
		t.Fatal(err)
	}
	loginReq.Header.Set("Content-Type", "application/json")
	lw := httptest.NewRecorder()
	application.Engine.ServeHTTP(lw, loginReq)
	if lw.Code != http.StatusOK {
		t.Fatalf("初始化管理员失败: %d %s", lw.Code, lw.Body.String())
	}
	var lr struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(lw.Body.Bytes(), &lr); err != nil || lr.Token == "" {
		t.Fatalf("登录响应缺少 token: %s", lw.Body.String())
	}
	testToken = lr.Token
	return application.Engine, dbPath
}

func jsonBody(v interface{}) *strings.Reader {
	b, _ := json.Marshal(v)
	return strings.NewReader(string(b))
}

func doJSON(r *gin.Engine, method, path string, body interface{}) *httptest.ResponseRecorder {
	req := authReq(method, path, jsonBody(body))
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
					"vod_area": "大陆", "vod_score": "8.5",
					"vod_time": "2026-11-07 10:00:00", "vod_play_url": "https://x/a.m3u8"},
				{"vod_id": "102", "vod_name": "测试影片B", "type_name": "喜剧片",
					"vod_area": "美国", "vod_score": "9.1",
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

func TestSourceAndCrawlAPI(t *testing.T) {
	r, _ := newTestRouter(t)
	site := newMockVideoSite(t)
	apiBase := site.URL + "/api.php/provide/vod/"
	payload := map[string]interface{}{
		"name": "模拟资源站", "apiBase": apiBase, "format": "seacms_json",
		"extraParams": "t=1", "enabled": true, "incrementalHours": 24, "maxPages": 10,
	}

	// 1. 创建站点
	w := doJSON(r, http.MethodPost, "/api/sources/v1", payload)
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
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/sources/v1", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"videoCount":0`) {
		t.Fatalf("站点列表异常: %d %s", w.Code, w.Body.String())
	}

	// 3. 全量采集并入库
	w = doJSON(r, http.MethodPost, "/api/sources/v1/"+strconv.Itoa(int(src.ID))+"/crawls?mode=full", nil)
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
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/videos/v1?q=影片A", nil))
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
	w = doJSON(r, http.MethodPost, "/api/sources/v1/"+strconv.Itoa(int(src.ID))+"/crawls?mode=full", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"new":0`) ||
		!strings.Contains(w.Body.String(), `"unchanged":2`) {
		t.Fatalf("重复采集未去重: %d %s", w.Code, w.Body.String())
	}

	// 6. 批量增量采集
	w = doJSON(r, http.MethodPost, "/api/crawls/v1?mode=incremental", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"mode":"incremental"`) {
		t.Fatalf("批量采集失败: %d %s", w.Code, w.Body.String())
	}

	// 7. 采集记录
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/jobs/v1", nil))
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
	if got := doJSON(r, http.MethodPost, "/api/sources/v1/"+strconv.Itoa(int(src.ID))+"/crawls?mode=bad", nil); got.Code != http.StatusBadRequest {
		t.Errorf("非法模式应返回 400，实际 %d", got.Code)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodPost, "/api/sources/v1/9999/crawls?mode=full", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("不存在的站点应返回 404，实际 %d", w.Code)
	}

	// 9. 更新与级联删除
	payload["name"] = "改名后的资源站"
	if w = doJSON(r, http.MethodPut, "/api/sources/v1/"+strconv.Itoa(int(src.ID)), payload); w.Code != http.StatusOK {
		t.Fatalf("更新站点失败: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodDelete, "/api/sources/v1/"+strconv.Itoa(int(src.ID)), nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("删除站点失败: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/videos/v1", nil))
	if !strings.Contains(w.Body.String(), `"total":0`) {
		t.Fatalf("删除站点后影片未级联清理: %s", w.Body.String())
	}
}

func TestVideoFieldMetaAndDetailAPI(t *testing.T) {
	r, _ := newTestRouter(t)

	// 1. 字段元数据：覆盖全部标准字段，且带中文分组信息
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/meta/v1/fields", nil))
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

	// 2. 采集一条数据，确认 API 返回完整结构化字段（不再有 raw）与详情接口
	site := newMockVideoSite(t)
	w = doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "字段测试站", "apiBase": site.URL + "/api.php/provide/vod/",
		"format": "seacms_json", "enabled": true, "incrementalHours": 24,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(r, http.MethodPost, "/api/sources/v1/1/crawls?mode=full", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("采集失败: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/videos/v1", nil))
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
	if _, ok := v["raw"]; ok {
		t.Errorf("响应不应再包含 raw 字段: %+v", v)
	}
	// 播放地址已结构化保存到 vodPlayUrl，不再依赖 raw 兜底
	hasPlayURL := false
	for _, item := range list.Items {
		if u, _ := item["vodPlayUrl"].(string); strings.Contains(u, "a.m3u8") {
			hasPlayURL = true
		}
	}
	if !hasPlayURL {
		t.Error("播放地址字段未结构化保存")
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/videos/v1/1", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || strings.Contains(body, `"raw"`) || !strings.Contains(body, `"vodName"`) {
		t.Fatalf("详情接口异常（不应含 raw）: %d %s", w.Code, body)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/videos/v1/9999", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("不存在的影片应返回 404，实际 %d", w.Code)
	}
}

// TestAPIRoutesFollowModuleVersionConvention 固化 /api/{模块名}/{版本号} 约定：
// 模块必须是复数资源名词，版本段必须存在，路径中不允许出现动词。
func TestAPIRoutesFollowModuleVersionConvention(t *testing.T) {
	r, _ := newTestRouter(t)
	modules := map[string]bool{
		"system": true, "assets": true, "sources": true,
		"crawls": true, "videos": true, "categories": true, "tags": true,
		"meta": true, "jobs": true, "tasks": true,
		"auth": true, "users": true,
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

func TestCategoryAPI(t *testing.T) {
	r, _ := newTestRouter(t)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/categories/v1", nil))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("空分类列表异常: %d %s", w.Code, w.Body.String())
	}

	site := newMockVideoSite(t)
	w = doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "分类测试站", "apiBase": site.URL + "/api.php/provide/vod/", "enabled": true,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(r, http.MethodPost, "/api/sources/v1/1/crawls", map[string]string{"mode": "full"})
	if w.Code != http.StatusOK {
		t.Fatalf("采集失败: %d %s", w.Code, w.Body.String())
	}

	// 模拟站点没给 type_id，分类应按 type_name 回退生成
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/categories/v1", nil))
	var cats []struct {
		ID         uint   `json:"id"`
		SourceID   uint   `json:"sourceId"`
		TypeID     string `json:"typeId"`
		Name       string `json:"name"`
		VideoCount int    `json:"videoCount"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &cats) != nil || len(cats) != 2 {
		t.Fatalf("应自动生成 2 个分类: %d %s", w.Code, w.Body.String())
	}
	for _, c := range cats {
		if c.SourceID != 1 || c.VideoCount != 1 || c.Name == "" {
			t.Errorf("分类数据异常: %+v", c)
		}
	}

	// 详情与不存在
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/categories/v1/"+strconv.Itoa(int(cats[0].ID)), nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), cats[0].Name) {
		t.Fatalf("分类详情异常: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/categories/v1/9999", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("不存在的分类应返回 404，实际 %d", w.Code)
	}

	// 按站点过滤 + 按分类过滤影片
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/categories/v1?source_id=999", nil))
	if !strings.Contains(w.Body.String(), "[]") {
		t.Errorf("按站点过滤分类异常: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet,
		"/api/videos/v1?category_id="+strconv.Itoa(int(cats[0].ID)), nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"total":1`) {
		t.Fatalf("按分类筛选影片异常: %d %s", w.Code, w.Body.String())
	}
}

// TestCategoryTreeEndpoint 验证 /api/categories/v1/tree 返回二级树，
// 且按一级分类筛选影片会命中其下全部二级分类的影片。
func TestCategoryTreeEndpoint(t *testing.T) {
	r, _ := newTestRouter(t)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		recs := []map[string]interface{}{
			{"vod_id": 1, "vod_name": "父级片", "type_id": 1, "type_name": "电影", "type_id_1": 0, "vod_time": "2026-11-07 10:00:00"},
			{"vod_id": 2, "vod_name": "子级片A", "type_id": 6, "type_name": "动作片", "type_id_1": 1, "vod_time": "2026-11-07 10:00:00"},
			{"vod_id": 3, "vod_name": "子级片B", "type_id": 7, "type_name": "喜剧片", "type_id_1": 1, "vod_time": "2026-11-07 10:00:00"},
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "ok", "page": 1, "pagecount": 1, "limit": "20", "total": 3, "list": recs,
		})
	}))
	t.Cleanup(mock.Close)

	w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "tree站", "apiBase": mock.URL + "/api.php/provide/vod/", "enabled": true,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}
	var src model.Source
	if err := json.Unmarshal(w.Body.Bytes(), &src); err != nil {
		t.Fatal(err)
	}
	w = doJSON(r, http.MethodPost, "/api/sources/v1/"+strconv.Itoa(int(src.ID))+"/crawls?mode=full", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("采集失败: %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/categories/v1/tree", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("tree 接口异常: %d %s", w.Code, w.Body.String())
	}
	var tree []struct {
		ID       uint   `json:"id"`
		Name     string `json:"name"`
		Level    int    `json:"level"`
		TypeID   string `json:"typeId"`
		Children []struct {
			ID     uint   `json:"id"`
			Name   string `json:"name"`
			Level  int    `json:"level"`
			TypeID string `json:"typeId"`
		} `json:"children"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &tree); err != nil {
		t.Fatalf("tree 响应非法: %v | %s", err, w.Body.String())
	}
	if len(tree) != 1 {
		t.Fatalf("应只有 1 个一级分类，实际 %d: %+v", len(tree), tree)
	}
	root := tree[0]
	if root.Name != "电影" || root.Level != 1 || len(root.Children) != 2 {
		t.Fatalf("一级分类或子分类异常: %+v", root)
	}
	childTypeIDs := map[string]bool{}
	for _, ch := range root.Children {
		if ch.Level != 2 {
			t.Fatalf("子分类层级异常: %+v", ch)
		}
		childTypeIDs[ch.TypeID] = true
	}
	if !childTypeIDs["6"] || !childTypeIDs["7"] {
		t.Fatalf("二级分类不完整，期望 6 与 7: %+v", root.Children)
	}

	// 按一级分类筛选影片：应命中其下全部二级分类的影片（3 部）
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet,
		"/api/videos/v1?category_id="+strconv.Itoa(int(root.ID)), nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"total":3`) {
		t.Fatalf("按一级分类筛选应命中 3 部影片: %d %s", w.Code, w.Body.String())
	}
}

// TestCrawlModeFromJSONBody 验证 RESTful 创建动作把参数放在 JSON body。
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
func TestVideoPlayGroupsEndpoint(t *testing.T) {
	t.Helper()
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 一条带两组播放线路的影片：youku / qq，各两集。
		rec := map[string]interface{}{
			"vod_id":        201,
			"vod_name":      "播放组测试片",
			"type_id":       6,
			"type_name":     "动作片",
			"vod_time":      "2026-11-07 10:00:00",
			"vod_play_from": "youku$$$qq",
			"vod_play_url":  "第1集$https://x/1.m3u8#第2集$https://x/2.m3u8$$$第1集$https://y/1.m3u8#第2集$https://y/2.m3u8",
			"vod_play_note": "线路A$$$线路B",
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "ok", "page": 1, "pagecount": 1,
			"limit": "20", "total": 1, "list": []interface{}{rec},
		})
	}))
	t.Cleanup(mock.Close)

	r, _ := newTestRouter(t)
	w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "播放组测试站", "apiBase": mock.URL + "/api.php/provide/vod/", "enabled": true,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}
	var src model.Source
	if err := json.Unmarshal(w.Body.Bytes(), &src); err != nil {
		t.Fatal(err)
	}

	w = doJSON(r, http.MethodPost, "/api/sources/v1/"+strconv.Itoa(int(src.ID))+"/crawls?mode=full", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("采集失败: %d %s", w.Code, w.Body.String())
	}

	// 取入库影片 ID
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/videos/v1?q=播放组测试片", nil))
	var videos struct {
		Items []model.Video `json:"items"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &videos) != nil || len(videos.Items) != 1 {
		t.Fatalf("查询入库影片失败: %d %s", w.Code, w.Body.String())
	}
	vid := videos.Items[0].ID

	// 播放组子资源
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/videos/v1/"+strconv.Itoa(int(vid))+"/play-groups", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("播放组接口异常: %d %s", w.Code, w.Body.String())
	}
	var groups []model.PlayGroup
	if err := json.Unmarshal(w.Body.Bytes(), &groups); err != nil {
		t.Fatalf("播放组响应非法: %v | %s", err, w.Body.String())
	}
	if len(groups) != 2 {
		t.Fatalf("应返回 2 个播放组，实际 %d: %+v", len(groups), groups)
	}
	if groups[0].From != "youku" || len(groups[0].Episodes) != 2 ||
		groups[0].Episodes[0].Url != "https://x/1.m3u8" {
		t.Errorf("第一播放组解析异常: %+v", groups[0])
	}
	if groups[1].From != "qq" || groups[1].Note != "线路B" {
		t.Errorf("第二播放组解析异常: %+v", groups[1])
	}

	// 不存在的影片返回 404
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/videos/v1/999999/play-groups", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("不存在影片的播放组应返回 404，实际 %d", w.Code)
	}
}

// TestVideoAreasAndSortEndpoint 验证资源检索：地域枚举、地域筛选、按更新时间/评分排序。
func TestVideoAreasAndSortEndpoint(t *testing.T) {
	r, _ := newTestRouter(t)
	site := newMockVideoSite(t)
	if w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "检索站", "apiBase": site.URL + "/api.php/provide/vod/",
		"format": "maccms_json", "enabled": true, "incrementalHours": 24,
	}); w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}
	if w := doJSON(r, http.MethodPost, "/api/sources/v1/1/crawls?mode=full", nil); w.Code != http.StatusOK {
		t.Fatalf("采集失败: %d %s", w.Code, w.Body.String())
	}

	// 地域枚举去重
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/videos/v1/areas", nil))
	var areas []string
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &areas) != nil {
		t.Fatalf("地域接口异常: %d %s", w.Code, w.Body.String())
	}
	if len(areas) != 2 {
		t.Fatalf("应有 2 个地域，实际 %v", areas)
	}

	type listResp struct {
		Items []model.Video `json:"items"`
		Total int64         `json:"total"`
	}
	get := func(qs string) listResp {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authReq(http.MethodGet, "/api/videos/v1?"+qs, nil))
		var out listResp
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("列表接口异常(%s): %d %s", qs, w.Code, w.Body.String())
		}
		return out
	}

	// 地域筛选
	if got := get("area=大陆"); got.Total != 1 || got.Items[0].VodName != "测试影片A" {
		t.Errorf("按地域筛选异常: %+v", got)
	}
	// 按更新时间倒序：B 的 vod_time 更晚
	if got := get("sort=time_desc"); got.Items[0].VodName != "测试影片B" {
		t.Errorf("按更新时间倒序异常: %+v", got.Items)
	}
	// 按评分倒序：B 9.1 > A 8.5
	if got := get("sort=score_desc"); got.Items[0].VodName != "测试影片B" || got.Items[1].VodName != "测试影片A" {
		t.Errorf("按评分倒序异常: %+v", got.Items)
	}
	// 按评分升序：A 8.5 在前
	if got := get("sort=score_asc"); got.Items[0].VodName != "测试影片A" {
		t.Errorf("按评分升序异常: %+v", got.Items)
	}
	// 名称检索
	if got := get("q=影片A"); got.Total != 1 || got.Items[0].VodName != "测试影片A" {
		t.Errorf("名称检索异常: %+v", got)
	}
}

// TestTaskPauseResumeAndCapacity 验证采集任务的手动暂停/继续与并发上限配置：
// 任务异步入队 → 运行中可暂停（保存续采页码）→ 继续时从暂停页码接着采。
func TestTaskPauseResumeAndCapacity(t *testing.T) {
	r, _ := newTestRouter(t)

	// 慢速分页站点：每页 1 条、共 40 页，每页 sleep 50ms，
	// 保证「暂停」一定发生在采集中途，而不是采集结束后。
	var mu sync.Mutex
	pageHits := 0
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		page, _ := strconv.Atoi(req.URL.Query().Get("pg"))
		if page < 1 {
			page = 1
		}
		mu.Lock()
		pageHits++
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "ok", "page": page, "pagecount": 40, "limit": "1", "total": 40,
			"list": []map[string]interface{}{{
				"vod_id": 1000 + page, "vod_name": fmt.Sprintf("暂停测试%d", page),
				"type_name": "动作片", "vod_area": "大陆", "vod_score": "8.0",
				"vod_time": fmt.Sprintf("2026-11-07 10:%02d:00", page%60),
			}},
		})
	}))
	defer site.Close()

	if w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "慢速站点", "apiBase": site.URL + "/api.php/provide/vod/",
		"format": "maccms_json", "enabled": true, "incrementalHours": 24,
	}); w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}

	// ---- 并发上限 ----
	w := doJSON(r, http.MethodPut, "/api/tasks/v1/capacity", map[string]int{"maxConcurrent": 2})
	var cap struct {
		MaxConcurrent int `json:"maxConcurrent"`
		Running       int `json:"running"`
		Queued        int `json:"queued"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &cap) != nil || cap.MaxConcurrent != 2 {
		t.Fatalf("调整并发上限失败: %d %s", w.Code, w.Body.String())
	}
	if w := doJSON(r, http.MethodPut, "/api/tasks/v1/capacity", map[string]int{"maxConcurrent": 0}); w.Code != http.StatusBadRequest {
		t.Fatalf("非法并发上限应返回 400，实际 %d", w.Code)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/tasks/v1/capacity", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"maxConcurrent":2`) {
		t.Fatalf("读取并发上限异常: %d %s", w.Code, w.Body.String())
	}

	// ---- 异步入队 ----
	w = doJSON(r, http.MethodPost, "/api/tasks/v1", map[string]interface{}{"sourceId": 1, "mode": "full"})
	var created struct {
		JobID  uint   `json:"jobId"`
		Status string `json:"status"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.JobID == 0 {
		t.Fatalf("创建任务异常: %d %s", w.Code, w.Body.String())
	}

	// 同站点的第二个任务不再冲突，而是排队等待（这里不发起第二条，
	// 否则它会在这条用例后续「暂停 / 续采」的断言里抢占站点）。
	// 排队行为见 service 层的 TestSameSourceTasksQueueInsteadOfReject。
	// 不存在的任务
	if w := doJSON(r, http.MethodPut, "/api/tasks/v1/999999",
		map[string]string{"status": "paused"}); w.Code != http.StatusNotFound {
		t.Fatalf("暂停未知任务应返回 404，实际 %d", w.Code)
	}

	type taskItem struct {
		JobID  uint   `json:"jobId"`
		Status string `json:"status"`
		Pages  int    `json:"pages"`
	}
	type taskList struct {
		Items []taskItem `json:"items"`
	}
	listTasks := func() taskList {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authReq(http.MethodGet, "/api/tasks/v1", nil))
		var out taskList
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("任务列表异常: %d %s", w.Code, w.Body.String())
		}
		return out
	}
	find := func(list taskList, jobID uint) (taskItem, bool) {
		for _, it := range list.Items {
			if it.JobID == jobID {
				return it, true
			}
		}
		return taskItem{}, false
	}
	waitFor := func(desc string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(6 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("等待超时：%s", desc)
			}
			time.Sleep(30 * time.Millisecond)
		}
	}

	// 等到真正开跑且已完成至少 1 页
	waitFor("任务进入运行态且有进度", func() bool {
		it, ok := find(listTasks(), created.JobID)
		return ok && it.Status == model.JobRunning && it.Pages >= 1
	})

	// ---- 暂停 ----
	w = doJSON(r, http.MethodPut, "/api/tasks/v1/"+strconv.Itoa(int(created.JobID)),
		map[string]string{"status": "paused"})
	if w.Code != http.StatusOK {
		t.Fatalf("暂停任务失败: %d %s", w.Code, w.Body.String())
	}
	// 任务应在暂停后退出运行态（从任务表中移除）
	waitFor("暂停后任务结束", func() bool {
		_, ok := find(listTasks(), created.JobID)
		return !ok
	})

	mu.Lock()
	hitsAtPause := pageHits
	mu.Unlock()
	if hitsAtPause >= 40 {
		t.Fatalf("暂停过晚，站点已被完整采集（%d 页）", hitsAtPause)
	}

	firstPaused := lastJob(t, r)
	if firstPaused.Status != model.JobPaused || firstPaused.ResumePage <= 1 {
		t.Fatalf("暂停后应记录续采页码: %+v", firstPaused)
	}
	if firstPaused.Pages < 1 {
		t.Fatalf("暂停时应保留已完成的页数: %+v", firstPaused)
	}

	// ---- 继续：从暂停页码续采 ----
	w = doJSON(r, http.MethodPut, "/api/tasks/v1/"+strconv.Itoa(int(firstPaused.ID)),
		map[string]string{"status": "running"})
	var resumed struct {
		JobID uint `json:"jobId"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &resumed) != nil || resumed.JobID == 0 {
		t.Fatalf("继续任务失败: %d %s", w.Code, w.Body.String())
	}
	if resumed.JobID != firstPaused.ID {
		t.Fatalf("继续任务应复用原记录 %d，实际新开了 %d", firstPaused.ID, resumed.JobID)
	}
	// 复用原记录后续采视图一开始就带着上一轮的累计统计，不能只看 Pages>=1，
	// 否则会在还没真正采到新页面时就暂停（resume_page 不会前进）。
	var resumedView struct {
		Pages   int `json:"pages"`
		Fetched int `json:"fetched"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resumedView)
	if resumedView.Pages < firstPaused.Pages || resumedView.Fetched < firstPaused.Fetched {
		t.Fatalf("继续采集中应沿用原记录的累计统计：记录 %d 页/%d 条，视图 %d 页/%d 条",
			firstPaused.Pages, firstPaused.Fetched, resumedView.Pages, resumedView.Fetched)
	}
	waitFor("续采任务进入运行态并采到新页面", func() bool {
		it, ok := find(listTasks(), resumed.JobID)
		return ok && it.Status == model.JobRunning && it.Pages > firstPaused.Pages
	})
	if w := doJSON(r, http.MethodPut, "/api/tasks/v1/"+strconv.Itoa(int(resumed.JobID)),
		map[string]string{"status": "paused"}); w.Code != http.StatusOK {
		t.Fatalf("再次暂停失败: %d %s", w.Code, w.Body.String())
	}
	waitFor("续采任务结束", func() bool {
		_, ok := find(listTasks(), resumed.JobID)
		return !ok
	})

	secondPaused := lastJob(t, r)
	if secondPaused.ID != firstPaused.ID {
		t.Fatalf("继续采集不应新开记录：期望 #%d，实际 #%d", firstPaused.ID, secondPaused.ID)
	}
	if secondPaused.Status != model.JobPaused {
		t.Fatalf("续采后暂停状态异常: %+v", secondPaused)
	}
	if secondPaused.ResumePage <= firstPaused.ResumePage {
		t.Fatalf("续采应从暂停页码继续：首次 %d → 续采 %d",
			firstPaused.ResumePage, secondPaused.ResumePage)
	}
}

// lastJob 读取最近一条采集记录。
func lastJob(t *testing.T, r *gin.Engine) model.CrawlJob {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/jobs/v1?limit=1", nil))
	var jobs []model.CrawlJob
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &jobs) != nil || len(jobs) != 1 {
		t.Fatalf("采集记录异常: %d %s", w.Code, w.Body.String())
	}
	return jobs[0]
}

// liveOf 读取采集记录接口里某条记录的实时进度（任务不在运行时为 nil）。
func liveOf(t *testing.T, r *gin.Engine, id uint) *service.TaskView {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/jobs/v1?limit=50", nil))
	var jobs []service.JobView
	if err := json.Unmarshal(w.Body.Bytes(), &jobs); err != nil {
		t.Fatalf("采集记录解析失败: %v", err)
	}
	for _, j := range jobs {
		if j.ID == id {
			return j.Live
		}
	}
	return nil
}

// listJobs 读取全部采集记录（测试里用来断言任务条数）。
func listJobs(t *testing.T, r *gin.Engine) []model.CrawlJob {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/jobs/v1?limit=50", nil))
	var jobs []model.CrawlJob
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &jobs) != nil {
		t.Fatalf("采集记录异常: %d %s", w.Code, w.Body.String())
	}
	return jobs
}

// jobByID 读取指定的采集记录（任务结束前后都可读，运行中会带上 live 字段）。
func jobByID(t *testing.T, r *gin.Engine, id uint) model.CrawlJob {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/jobs/v1?limit=50", nil))
	var jobs []model.CrawlJob
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &jobs) != nil {
		t.Fatalf("采集记录异常: %d %s", w.Code, w.Body.String())
	}
	for _, j := range jobs {
		if j.ID == id {
			return j
		}
	}
	t.Fatalf("采集记录中找不到任务 %d", id)
	return model.CrawlJob{}
}

// waitJobStatus 轮询采集记录，直到任务进入指定状态。
func waitJobStatus(t *testing.T, r *gin.Engine, id uint, want string) model.CrawlJob {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		job := jobByID(t, r, id)
		if job.Status == want {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("任务 %d 未在超时内进入 %s，当前 %s（%s）", id, want, job.Status, job.Message)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func taskViews(r *gin.Engine) []service.TaskView {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/tasks/v1", nil))
	var out struct {
		Items []service.TaskView `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out.Items
}

// TaskViewOf 从任务列表中取出指定任务。
func taskViewOf(r *gin.Engine, jobID uint) (service.TaskView, bool) {
	for _, it := range taskViews(r) {
		if it.JobID == jobID {
			return it, true
		}
	}
	return service.TaskView{}, false
}

// TestTaskAutoRetryOnFailure 验证失败自动重试：站点第一次请求失败，
// 后台按退避自动重试一次后成功，采集记录标记为「第 2 次尝试成功」。
func TestTaskAutoRetryOnFailure(t *testing.T) {
	r, _ := newTestRouterWith(t, func(cfg *config.Config) {
		cfg.Collector.RetryTimes = 1
		cfg.Collector.RetryBackoffSeconds = 1
	})

	var mu sync.Mutex
	hits := 0
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// 采集还会额外请求 ac=typelist 补全父级分类名称；它失败不影响采集，
		// 这里直接返回空列表（不计入失败注入逻辑）。
		if req.URL.Query().Get("ac") == "typelist" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 1, "msg": "ok", "page": 1, "pagecount": 1, "limit": "1", "total": 0,
				"list": []interface{}{},
			})
			return
		}
		mu.Lock()
		hits++
		n := hits
		mu.Unlock()
		if n == 1 {
			http.Error(w, "upstream boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "ok", "page": 1, "pagecount": 1, "limit": "1", "total": 1,
			"list": []map[string]interface{}{{
				"vod_id": 3001, "vod_name": "自动重试影片", "type_name": "动作片",
				"vod_area": "大陆", "vod_time": "2026-11-07 10:00:00",
			}},
		})
	}))
	defer site.Close()

	if w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "重试站点", "apiBase": site.URL + "/api.php/provide/vod/",
		"format": "maccms_json", "enabled": true, "incrementalHours": 24,
	}); w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}

	w := doJSON(r, http.MethodPost, "/api/tasks/v1", map[string]interface{}{"sourceId": 1, "mode": "full"})
	var created struct {
		JobID uint `json:"jobId"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.JobID == 0 {
		t.Fatalf("创建任务异常: %d %s", w.Code, w.Body.String())
	}

	// 等待重试期间，任务列表应能看到倒计时与失败原因
	sawRetryWait := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := taskViewOf(r, created.JobID); ok && v.RetryInSeconds > 0 {
			sawRetryWait = true
			if v.Attempt != 1 || v.MaxAttempts != 2 {
				t.Fatalf("重试期间尝试次数异常: %+v", v)
			}
			if !strings.Contains(v.Message, "自动重试") {
				t.Fatalf("重试期间应说明自动重试: %+v", v)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawRetryWait {
		t.Fatalf("未观察到「等待自动重试」的任务状态")
	}

	job := waitJobStatus(t, r, created.JobID, model.JobSuccess)
	if job.Attempt != 2 {
		t.Fatalf("自动重试后应记录 2 次尝试，实际 %d（%s）", job.Attempt, job.Message)
	}
	if job.New != 1 || job.Fetched != 1 {
		t.Fatalf("重试成功后应正常入库: %+v", job)
	}
}

// TestTaskManualRetryAndRerun 验证手动重试（从失败页续采）与重新运行（从第 1 页）。
func TestTaskManualRetryAndRerun(t *testing.T) {
	r, _ := newTestRouterWith(t, func(cfg *config.Config) {
		cfg.Collector.RetryTimes = 0 // 关闭自动重试，专心验证手动动作
	})

	var mu sync.Mutex
	pages := []int{}
	failFrom := 2 // 第 2 页起返回 500
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Get("ac") == "typelist" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 1, "msg": "ok", "page": 1, "pagecount": 1, "limit": "1", "total": 0,
				"list": []interface{}{},
			})
			return
		}
		page, _ := strconv.Atoi(req.URL.Query().Get("pg"))
		if page < 1 {
			page = 1
		}
		mu.Lock()
		pages = append(pages, page)
		fail := failFrom > 0 && page >= failFrom
		mu.Unlock()
		if fail {
			http.Error(w, "upstream boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "ok", "page": page, "pagecount": 3, "limit": "1", "total": 3,
			"list": []map[string]interface{}{{
				"vod_id": 4000 + page, "vod_name": fmt.Sprintf("重试影片%d", page),
				"type_name": "动作片", "vod_area": "大陆",
				"vod_time": fmt.Sprintf("2026-11-07 10:%02d:00", page),
			}},
		})
	}))
	defer site.Close()

	if w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "手动重试站点", "apiBase": site.URL + "/api.php/provide/vod/",
		"format": "maccms_json", "enabled": true, "incrementalHours": 24,
	}); w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}

	// ---- 首次采集：第 2 页失败 ----
	w := doJSON(r, http.MethodPost, "/api/tasks/v1", map[string]interface{}{"sourceId": 1, "mode": "full"})
	var first struct {
		JobID uint `json:"jobId"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &first) != nil {
		t.Fatalf("创建任务异常: %d %s", w.Code, w.Body.String())
	}
	failed := waitJobStatus(t, r, first.JobID, model.JobFailed)
	if failed.ResumePage != 2 {
		t.Fatalf("失败任务的续采页码应为失败页 2，实际 %d", failed.ResumePage)
	}
	if failed.Pages != 1 {
		t.Fatalf("失败前应只完成 1 页，实际 %d", failed.Pages)
	}
	if failed.Attempt != 1 {
		t.Fatalf("关闭自动重试后尝试次数应为 1，实际 %d", failed.Attempt)
	}

	// ---- 手动重试：站点恢复，从失败页续采 ----
	mu.Lock()
	failFrom = 0
	pages = nil
	mu.Unlock()

	w = doJSON(r, http.MethodPost, "/api/tasks/v1/"+strconv.Itoa(int(first.JobID))+"/retry", nil)
	var retried struct {
		JobID   uint `json:"jobId"`
		Attempt int  `json:"attempt"`
		Pages   int  `json:"pages"`
		Fetched int  `json:"fetched"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &retried) != nil || retried.JobID == 0 {
		t.Fatalf("重试任务失败: %d %s", w.Code, w.Body.String())
	}
	if retried.JobID != first.JobID {
		t.Fatalf("重试应复用原任务记录 %d，实际新开了 %d", first.JobID, retried.JobID)
	}
	if retried.Attempt != 2 {
		t.Fatalf("重试后应接着原任务的尝试次数（2），实际 %d", retried.Attempt)
	}
	// 重试入队时就要带上原任务已累计的统计，页面不会闪回 0。
	if retried.Pages != 1 || retried.Fetched != 1 {
		t.Fatalf("重试视图应带原任务累计统计（1 页/1 条），实际 %d 页/%d 条", retried.Pages, retried.Fetched)
	}
	retryJob := waitJobStatus(t, r, first.JobID, model.JobSuccess)
	if retryJob.RetryOf != 0 {
		t.Fatalf("重试复用原记录，不应再标记来源任务，实际 %d", retryJob.RetryOf)
	}
	// 统计与页数在原任务基础上累加：失败前已采 1 页 1 条，重试再采 2 页 2 条。
	if retryJob.Pages != 3 {
		t.Fatalf("重试后页数应累计为 3，实际 %d", retryJob.Pages)
	}
	if retryJob.Fetched != 3 || retryJob.New != 3 {
		t.Fatalf("重试后拉取/新增应累计为 3/3，实际 %d/%d", retryJob.Fetched, retryJob.New)
	}
	if retryJob.Attempt != 2 {
		t.Fatalf("重试后尝试次数应累计为 2，实际 %d", retryJob.Attempt)
	}
	// 最终只有一条任务记录
	if jobs := listJobs(t, r); len(jobs) != 1 {
		t.Fatalf("手动重试不应新开任务，期望 1 条记录，实际 %d 条", len(jobs))
	}
	mu.Lock()
	firstPage := -1
	if len(pages) > 0 {
		firstPage = pages[0]
	}
	mu.Unlock()
	if firstPage != 2 {
		t.Fatalf("重试应从失败页 2 继续，实际首请求页 %d", firstPage)
	}

	// 成功的任务不允许再「重试」
	if w := doJSON(r, http.MethodPost,
		"/api/tasks/v1/"+strconv.Itoa(int(retried.JobID))+"/retry", nil); w.Code != http.StatusConflict {
		t.Fatalf("对成功任务重试应返回 409，实际 %d %s", w.Code, w.Body.String())
	}

	// ---- 重新运行：从第 1 页重新采集 ----
	mu.Lock()
	pages = nil
	mu.Unlock()
	w = doJSON(r, http.MethodPost, "/api/tasks/v1/"+strconv.Itoa(int(retried.JobID))+"/rerun", nil)
	var rerun struct {
		JobID uint `json:"jobId"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &rerun) != nil || rerun.JobID == 0 {
		t.Fatalf("重新运行失败: %d %s", w.Code, w.Body.String())
	}
	if rerun.JobID != retried.JobID {
		t.Fatalf("重新运行应复用原记录 %d，实际新开了 %d", retried.JobID, rerun.JobID)
	}
	rerunJob := waitJobStatus(t, r, rerun.JobID, model.JobSuccess)
	mu.Lock()
	rerunFirstPage := -1
	if len(pages) > 0 {
		rerunFirstPage = pages[0]
	}
	mu.Unlock()
	if rerunFirstPage != 1 {
		t.Fatalf("重新运行应从第 1 页开始，实际首请求页 %d", rerunFirstPage)
	}
	if rerunJob.RetryOf != 0 {
		t.Fatalf("复用原记录时不应标记来源任务，实际 %d", rerunJob.RetryOf)
	}
	// 统计在重新运行前被清零，最后只剩这一轮的 3 页 3 条。
	if rerunJob.Pages != 3 || rerunJob.Fetched != 3 {
		t.Fatalf("重新运行应完整采集 3 页 3 条，实际 %d 页 / %d 条", rerunJob.Pages, rerunJob.Fetched)
	}
	if rerunJob.Attempt != 1 {
		t.Fatalf("重新运行应把尝试次数重新计数（1），实际 %d", rerunJob.Attempt)
	}
	if jobs := listJobs(t, r); len(jobs) != 1 {
		t.Fatalf("重新运行不应新开任务，期望 1 条记录，实际 %d 条", len(jobs))
	}

	// 未知任务的各动作都返回 404
	for _, action := range []string{"retry", "rerun"} {
		if w := doJSON(r, http.MethodPost, "/api/tasks/v1/999999/"+action, nil); w.Code != http.StatusNotFound {
			t.Fatalf("%s 未知任务应返回 404，实际 %d", action, w.Code)
		}
	}
}

// TestTaskLiveProgress 验证任务列表的实时进度：页数、总页数与百分比。
func TestTaskLiveProgress(t *testing.T) {
	r, _ := newTestRouterWith(t, func(cfg *config.Config) { cfg.Collector.RetryTimes = 0 })

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		page, _ := strconv.Atoi(req.URL.Query().Get("pg"))
		if page < 1 {
			page = 1
		}
		time.Sleep(40 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "ok", "page": page, "pagecount": 10, "limit": "1", "total": 10,
			"list": []map[string]interface{}{{
				"vod_id": 5000 + page, "vod_name": fmt.Sprintf("进度影片%d", page),
				"type_name": "动作片", "vod_area": "大陆",
				"vod_time": fmt.Sprintf("2026-11-07 10:%02d:00", page),
			}},
		})
	}))
	defer site.Close()

	if w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "进度站点", "apiBase": site.URL + "/api.php/provide/vod/",
		"format": "maccms_json", "enabled": true, "incrementalHours": 24,
	}); w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}
	w := doJSON(r, http.MethodPost, "/api/tasks/v1", map[string]interface{}{"sourceId": 1, "mode": "full"})
	var created struct {
		JobID uint `json:"jobId"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatalf("创建任务异常: %d %s", w.Code, w.Body.String())
	}

	// 中途应能看到 0 < 百分比 < 100 且带总页数
	sawProgress := false
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) && !sawProgress {
		if v, ok := taskViewOf(r, created.JobID); ok {
			if v.Status == model.JobRunning && v.PageCount == 10 && v.Page >= 2 && v.Page < 10 {
				if v.Percent <= 0 || v.Percent >= 100 {
					t.Fatalf("运行中百分比应在 0..100 之间: %+v", v)
				}
				if v.ItemsPerSecond <= 0 {
					t.Fatalf("运行中应能算出采集速率: %+v", v)
				}
				// 拉取 / 新增 / 更新 / 未变也要随任务实时累计，且四者自洽。
				if v.Fetched < v.Pages {
					t.Fatalf("运行中拉取条数应随分页推进: %+v", v)
				}
				if v.Fetched != v.New+v.Updated+v.Unchanged {
					t.Fatalf("运行中拉取应等于新增+更新+未变: %+v", v)
				}
				// 采集记录的 live 也必须是同一份实时统计
				if lv := liveOf(t, r, created.JobID); lv == nil || lv.Fetched < v.Fetched {
					t.Fatalf("采集记录实时统计落后: live=%+v task=%+v", lv, v)
				}
				sawProgress = true
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !sawProgress {
		t.Fatalf("未观察到运行中的分页进度")
	}

	job := waitJobStatus(t, r, created.JobID, model.JobSuccess)
	if job.Pages != 10 {
		t.Fatalf("应完整采集 10 页，实际 %d", job.Pages)
	}
	// 任务结束后不应再留在任务列表里
	if _, ok := taskViewOf(r, created.JobID); ok {
		t.Fatalf("已结束的任务不应出现在运行任务列表中")
	}
	// 采集记录里的 live 字段应为空
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/jobs/v1?limit=1", nil))
	if strings.Contains(w.Body.String(), `"live"`) {
		t.Fatalf("已结束任务的采集记录不应带 live 进度: %s", w.Body.String())
	}
}

// TestTaskPauseDuringRetryWait 验证「等待自动重试」期间也能被手动暂停：
// 暂停应立刻打断退避等待，任务记为已暂停而不是等到重试结束。
func TestTaskPauseDuringRetryWait(t *testing.T) {
	r, _ := newTestRouterWith(t, func(cfg *config.Config) {
		cfg.Collector.RetryTimes = 3
		cfg.Collector.RetryBackoffSeconds = 5 // 退避足够长，便于在中途暂停
	})

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "always boom", http.StatusInternalServerError)
	}))
	defer site.Close()

	if w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "长退避站点", "apiBase": site.URL + "/api.php/provide/vod/",
		"format": "maccms_json", "enabled": true, "incrementalHours": 24,
	}); w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}
	w := doJSON(r, http.MethodPost, "/api/tasks/v1", map[string]interface{}{"sourceId": 1, "mode": "full"})
	var created struct {
		JobID uint `json:"jobId"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatalf("创建任务异常: %d %s", w.Code, w.Body.String())
	}

	deadline := time.Now().Add(4 * time.Second)
	for {
		if v, ok := taskViewOf(r, created.JobID); ok && v.RetryInSeconds > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("未进入等待自动重试状态")
		}
		time.Sleep(20 * time.Millisecond)
	}

	start := time.Now()
	if w := doJSON(r, http.MethodPut, "/api/tasks/v1/"+strconv.Itoa(int(created.JobID)),
		map[string]string{"status": "paused"}); w.Code != http.StatusOK {
		t.Fatalf("暂停失败: %d %s", w.Code, w.Body.String())
	}
	job := waitJobStatus(t, r, created.JobID, model.JobPaused)
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("暂停应打断退避等待，实际耗时 %v", took)
	}
	if !strings.Contains(job.Message, "已暂停") {
		t.Fatalf("暂停说明异常: %s", job.Message)
	}
}

// TestTaskProgressPersistedWhileRunning 验证运行中的任务会定期把进度写库：
// 采集记录里能看到已完成的页数、统计与续采页，即使进程被杀也不会丢进度。
func TestTaskProgressPersistedWhileRunning(t *testing.T) {
	r, _ := newTestRouterWith(t, func(cfg *config.Config) {
		cfg.Collector.RetryTimes = 0
		cfg.Collector.ProgressSaveSeconds = 1 // 测试里把落库间隔压到 1 秒
	})

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		page, _ := strconv.Atoi(req.URL.Query().Get("pg"))
		if page < 1 {
			page = 1
		}
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "ok", "page": page, "pagecount": 10, "limit": "1", "total": 10,
			"list": []map[string]interface{}{{
				"vod_id": 7000 + page, "vod_name": fmt.Sprintf("落库影片%d", page),
				"type_name": "动作片", "vod_area": "大陆",
				"vod_time": fmt.Sprintf("2026-11-08 10:%02d:00", page),
			}},
		})
	}))
	defer site.Close()

	if w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "落库站点", "apiBase": site.URL + "/api.php/provide/vod/",
		"format": "maccms_json", "enabled": true, "incrementalHours": 24,
	}); w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}
	w := doJSON(r, http.MethodPost, "/api/tasks/v1", map[string]interface{}{"sourceId": 1, "mode": "full"})
	var created struct {
		JobID uint `json:"jobId"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatalf("创建任务异常: %d %s", w.Code, w.Body.String())
	}

	// 运行中等待第一次落库：采集记录（落库值，非 live）里应出现已完成页数。
	var persisted model.CrawlJob
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		job := jobByID(t, r, created.JobID)
		if job.Status == model.JobRunning && job.Pages >= 2 {
			persisted = job
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if persisted.Pages == 0 {
		t.Fatalf("运行中未把进度写入采集记录（pages 仍为 0）")
	}
	if persisted.Fetched == 0 || persisted.Total != 10 {
		t.Fatalf("落库的统计不完整: %+v", persisted)
	}
	// 续采页应指向「已完成页 + 1」，重启后重试即可从这里继续。
	if persisted.ResumePage != persisted.Pages+1 {
		t.Fatalf("落库的续采页应为 %d，实际 %d", persisted.Pages+1, persisted.ResumePage)
	}

	job := waitJobStatus(t, r, created.JobID, model.JobSuccess)
	if job.Pages != 10 || job.Fetched != 10 {
		t.Fatalf("任务结束统计异常: %+v", job)
	}
	if job.ResumePage != 1 {
		t.Fatalf("任务正常结束后续采页应回到 1，实际 %d", job.ResumePage)
	}
}

// TestSourcePlayRewriteAPI 验证站点的「视频解析地址 / 视频播放域名」配置：
// 入库保存原始地址，读取影片列表与播放组时按配置改写（先换域名，再拼解析地址）。
func TestSourcePlayRewriteAPI(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		rec := map[string]interface{}{
			"vod_id": 301, "vod_name": "改写地址影片", "type_id": 6, "type_name": "动作片",
			"vod_time": "2026-11-08 10:00:00", "vod_play_from": "m3u8",
			"vod_play_url": "第1集$https://dead.example.net/v/1/index.m3u8",
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "ok", "page": 1, "pagecount": 1,
			"limit": "20", "total": 1, "list": []interface{}{rec},
		})
	}))
	t.Cleanup(mock.Close)

	r, _ := newTestRouter(t)

	// 1. 带解析地址与播放域名创建站点：域名会被规范化
	w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "改写站点", "apiBase": mock.URL + "/api.php/provide/vod/", "enabled": true,
		"parseApi":   "https://jx.example.com/?url=",
		"playDomain": " cdn.example.com/ ",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}
	var src model.Source
	if err := json.Unmarshal(w.Body.Bytes(), &src); err != nil {
		t.Fatal(err)
	}
	if src.ParseAPI != "https://jx.example.com/?url=" || src.PlayDomain != "https://cdn.example.com" {
		t.Fatalf("播放配置未按预期规范化: parseApi=%q playDomain=%q", src.ParseAPI, src.PlayDomain)
	}

	// 2. 采集后读取列表：播放地址应已被改写
	if w := doJSON(r, http.MethodPost,
		"/api/sources/v1/"+strconv.Itoa(int(src.ID))+"/crawls?mode=full", nil); w.Code != http.StatusOK {
		t.Fatalf("采集失败: %d %s", w.Code, w.Body.String())
	}
	want := "https://jx.example.com/?url=https://cdn.example.com/v/1/index.m3u8"
	listVideos := func() model.Video {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authReq(http.MethodGet, "/api/videos/v1?q=改写地址影片", nil))
		var out struct {
			Items []model.Video `json:"items"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Items) != 1 {
			t.Fatalf("查询影片失败: %d %s", w.Code, w.Body.String())
		}
		return out.Items[0]
	}
	v := listVideos()
	var groups []model.PlayGroup
	if err := json.Unmarshal([]byte(v.VodPlayList), &groups); err != nil || len(groups) != 1 {
		t.Fatalf("播放组数据异常: %v %s", err, v.VodPlayList)
	}
	if got := groups[0].Episodes[0].Url; got != want {
		t.Fatalf("列表播放地址改写异常: %q，期望 %q", got, want)
	}
	if !groups[0].Episodes[0].Frame {
		t.Error("列表播放组应带 frame=true，供前端用 iframe 承载解析页面")
	}

	// 3. 播放组子资源同样改写
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet,
		"/api/videos/v1/"+strconv.Itoa(int(v.ID))+"/play-groups", nil))
	var got []model.PlayGroup
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got) != 1 {
		t.Fatalf("播放组接口异常: %d %s", w.Code, w.Body.String())
	}
	if got[0].Episodes[0].Url != want {
		t.Fatalf("播放组地址改写异常: %q", got[0].Episodes[0].Url)
	}
	if !got[0].Episodes[0].Frame {
		t.Error("播放组接口应带 frame=true")
	}

	// 4. 清空配置后恢复原始地址（数据库里始终是原始值）
	if w := doJSON(r, http.MethodPut, "/api/sources/v1/"+strconv.Itoa(int(src.ID)), map[string]interface{}{
		"name": src.Name, "apiBase": src.APIBase, "enabled": true, "incrementalHours": 24,
		"parseApi": "", "playDomain": "",
	}); w.Code != http.StatusOK {
		t.Fatalf("更新站点失败: %d %s", w.Code, w.Body.String())
	}
	v = listVideos()
	groups = nil
	if err := json.Unmarshal([]byte(v.VodPlayList), &groups); err != nil || len(groups) != 1 {
		t.Fatalf("播放组数据异常: %v", err)
	}
	if got := groups[0].Episodes[0].Url; got != "https://dead.example.net/v/1/index.m3u8" {
		t.Fatalf("清空配置后应回到原始地址，实际 %q", got)
	}

	// 5. 非法解析地址被拒绝
	if w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "非法解析站", "apiBase": mock.URL + "/api.php/provide/vod/",
		"parseApi": "jx.example.com/?url=",
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("非法解析地址应返回 400，实际 %d %s", w.Code, w.Body.String())
	}
}

// TestRecoverInterruptedOnRestart 验证服务重启后自动恢复被中断的采集任务：
// 复用原记录、从续采页继续、统计累加、不新开任务；用户手动暂停的任务不动。
func TestRecoverInterruptedOnRestart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "restart.db")
	webDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("<div id=\"app\"></div>"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 模拟站点：共 3 页，记录每次请求的页码。
	var mu sync.Mutex
	var pages []int
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Get("ac") == "typelist" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 1, "msg": "ok", "page": 1, "pagecount": 1, "limit": "1", "total": 0,
				"list": []interface{}{},
			})
			return
		}
		page, _ := strconv.Atoi(req.URL.Query().Get("pg"))
		if page < 1 {
			page = 1
		}
		mu.Lock()
		pages = append(pages, page)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "ok", "page": page, "pagecount": 3, "limit": "1", "total": 3,
			"list": []map[string]interface{}{{
				"vod_id": 2000 + page, "vod_name": fmt.Sprintf("重启恢复%d", page),
				"type_name": "动作片", "vod_time": fmt.Sprintf("2026-11-07 10:%02d:00", page),
			}},
		})
	}))
	defer site.Close()

	// 预置「上次进程」遗留的站点与一条进度已保存到第 2 页的 running 记录。
	st, err := repository.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	src := &model.Source{
		Name: "重启恢复站点", APIBase: site.URL + "/api.php/provide/vod/",
		Format: model.FormatMacCMSJSON, Enabled: true, IncrementalHours: 24,
	}
	if err := st.Sources.CreateSource(src); err != nil {
		t.Fatal(err)
	}
	job := &model.CrawlJob{
		SourceID: src.ID, SourceName: src.Name, Mode: model.ModeFull,
		Status: model.JobRunning, StartedAt: time.Now(),
		ResumePage: 2, Pages: 1, Fetched: 1, Attempt: 1,
	}
	if err := st.Jobs.CreateJob(job); err != nil {
		t.Fatal(err)
	}
	interruptedID := job.ID
	// 另放一条用户手动暂停的记录，确认不会被自动恢复。
	paused := &model.CrawlJob{
		SourceID: src.ID, SourceName: src.Name, Mode: model.ModeFull,
		Status: model.JobPaused, StartedAt: time.Now(), ResumePage: 3,
	}
	if err := st.Jobs.CreateJob(paused); err != nil {
		t.Fatal(err)
	}
	pausedID := paused.ID
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// 模拟重启：同一个数据库，重新装配应用。
	cfg := config.Default()
	cfg.Database.Path = dbPath
	cfg.Web.Dir = webDir
	cfg.Collector.RetryTimes = 0
	application, err := app.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()

	loginReq, _ := http.NewRequest(http.MethodPost, "/api/auth/v1/sessions",
		jsonBody(map[string]string{"username": "admin", "password": "admin123456"}))
	loginReq.Header.Set("Content-Type", "application/json")
	lw := httptest.NewRecorder()
	application.Engine.ServeHTTP(lw, loginReq)
	if lw.Code != http.StatusOK {
		t.Fatalf("登录失败: %d %s", lw.Code, lw.Body.String())
	}
	var lr struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(lw.Body.Bytes(), &lr); err != nil || lr.Token == "" {
		t.Fatal("登录响应缺少 token")
	}
	testToken = lr.Token
	defer func() { testToken = "" }()

	r := application.Engine
	got := waitJobStatus(t, r, interruptedID, model.JobSuccess)

	if got.ID != interruptedID {
		t.Fatalf("应复用原记录 %d，实际 %d", interruptedID, got.ID)
	}
	if strings.Contains(got.Message, "服务重启") {
		t.Fatalf("恢复后中断提示应清空，实际 %q", got.Message)
	}
	if got.RetryOf != 0 {
		t.Fatalf("恢复不应标记来源任务，实际 %d", got.RetryOf)
	}
	// 从第 2 页续采 2 页，统计在原记录上累加：1 + 2 = 3。
	if got.Pages != 3 || got.Fetched != 3 {
		t.Fatalf("统计应在原记录上累加（3 页 / 3 条），实际 %d 页 / %d 条", got.Pages, got.Fetched)
	}
	if jobs := listJobs(t, r); len(jobs) != 2 {
		t.Fatalf("重启恢复不应新开任务，期望 2 条记录，实际 %d 条", len(jobs))
	}
	if p := jobByID(t, r, pausedID); p.Status != model.JobPaused {
		t.Fatalf("用户手动暂停的任务不应被自动恢复，实际 %s", p.Status)
	}
	mu.Lock()
	gotPages := append([]int(nil), pages...)
	mu.Unlock()
	if len(gotPages) != 2 || gotPages[0] != 2 || gotPages[1] != 3 {
		t.Fatalf("应从第 2 页续采且只请求 2、3 页，实际 %v", gotPages)
	}
}
