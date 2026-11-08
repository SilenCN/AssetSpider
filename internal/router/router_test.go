package router_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/app"
	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/model"
	routerpkg "github.com/SilenCN/AssetSpider/internal/router"
	"github.com/gin-gonic/gin"
)

func newTestRouter(t *testing.T) (*gin.Engine, string) {
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
	application, err := app.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { application.Close() })
	return application.Engine, dbPath
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
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/system/v1/health", nil))
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

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/not-exist", nil))
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
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/assets/v1", nil))
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
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete,
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
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/sources/v1", nil))
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
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/videos/v1?q=影片A", nil))
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
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/jobs/v1", nil))
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
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/sources/v1/9999/crawls?mode=full", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("不存在的站点应返回 404，实际 %d", w.Code)
	}

	// 9. 更新与级联删除
	payload["name"] = "改名后的资源站"
	if w = doJSON(r, http.MethodPut, "/api/sources/v1/"+strconv.Itoa(int(src.ID)), payload); w.Code != http.StatusOK {
		t.Fatalf("更新站点失败: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/sources/v1/"+strconv.Itoa(int(src.ID)), nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("删除站点失败: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/videos/v1", nil))
	if !strings.Contains(w.Body.String(), `"total":0`) {
		t.Fatalf("删除站点后影片未级联清理: %s", w.Body.String())
	}
}

func TestVideoFieldMetaAndDetailAPI(t *testing.T) {
	r, _ := newTestRouter(t)

	// 1. 字段元数据：覆盖全部标准字段，且带中文分组信息
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/meta/v1/fields", nil))
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
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/videos/v1", nil))
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
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/videos/v1/1", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || strings.Contains(body, `"raw"`) || !strings.Contains(body, `"vodName"`) {
		t.Fatalf("详情接口异常（不应含 raw）: %d %s", w.Code, body)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/videos/v1/9999", nil))
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
		"crawls": true, "videos": true, "categories": true,
		"meta": true, "jobs": true,
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
		r.ServeHTTP(w, httptest.NewRequest(o.method, o.path, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("旧路由 %s 应返回 404，实际 %d", o.path, w.Code)
		}
	}
}

func TestCategoryAPI(t *testing.T) {
	r, _ := newTestRouter(t)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/categories/v1", nil))
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
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/categories/v1", nil))
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
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/categories/v1/"+strconv.Itoa(int(cats[0].ID)), nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), cats[0].Name) {
		t.Fatalf("分类详情异常: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/categories/v1/9999", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("不存在的分类应返回 404，实际 %d", w.Code)
	}

	// 按站点过滤 + 按分类过滤影片
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/categories/v1?source_id=999", nil))
	if !strings.Contains(w.Body.String(), "[]") {
		t.Errorf("按站点过滤分类异常: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
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
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/categories/v1/tree", nil))
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
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
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
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/videos/v1?q=播放组测试片", nil))
	var videos struct {
		Items []model.Video `json:"items"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &videos) != nil || len(videos.Items) != 1 {
		t.Fatalf("查询入库影片失败: %d %s", w.Code, w.Body.String())
	}
	vid := videos.Items[0].ID

	// 播放组子资源
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/videos/v1/"+strconv.Itoa(int(vid))+"/play-groups", nil))
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
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/videos/v1/999999/play-groups", nil))
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
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/videos/v1/areas", nil))
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
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/videos/v1?"+qs, nil))
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
