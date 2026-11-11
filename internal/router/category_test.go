package router_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
)

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
	if w.Code != http.StatusOK || listItemCount(t, w.Body.Bytes()) != 1 {
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
	if w.Code != http.StatusOK || listItemCount(t, w.Body.Bytes()) != 3 {
		t.Fatalf("按一级分类筛选应命中 3 部影片: %d %s", w.Code, w.Body.String())
	}
}

// TestCrawlModeFromJSONBody 验证 RESTful 创建动作把参数放在 JSON body。
