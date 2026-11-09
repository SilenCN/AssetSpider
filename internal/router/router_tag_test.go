package router_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// TestTagAPI 验证 /api/tags/v1 拉取采集自动生成的标签（含影片数），
// 以及 /api/videos/v1?tag_id= 按标签筛选影片。
func TestTagAPI(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 苹果CMS 采集会额外请求 typelist 补全分类名，给一个空列表即可。
		if r.URL.Query().Get("ac") == "typelist" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 1, "list": []interface{}{}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "数据列表", "page": 1, "pagecount": 1,
			"limit": "20", "total": 2,
			"list": []map[string]interface{}{
				{"vod_id": 201, "vod_name": "标签片A", "type_name": "动作片",
					"vod_tag": "动作,科幻", "vod_time": "2026-11-07 10:00:00",
					"vod_play_url": "https://x/a.m3u8"},
				{"vod_id": 202, "vod_name": "标签片B", "type_name": "喜剧片",
					"vod_tag": "科幻，喜剧", "vod_time": "2026-11-07 11:00:00"},
			},
		})
	}))
	t.Cleanup(site.Close)

	r, _ := newTestRouter(t)

	// 初始为空标签列表。
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/tags/v1", nil))
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("空标签列表异常: %d %s", w.Code, w.Body.String())
	}

	// 建站并全量采集。
	w = doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "标签测试站", "apiBase": site.URL + "/api.php/provide/vod/", "enabled": true,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(r, http.MethodPost, "/api/sources/v1/1/crawls", map[string]string{"mode": "full"})
	if w.Code != http.StatusOK {
		t.Fatalf("采集失败: %d %s", w.Code, w.Body.String())
	}

	// 应自动生成 动作 / 科幻 / 喜剧 三个标签。
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/tags/v1", nil))
	var tags []struct {
		ID         uint   `json:"id"`
		SourceID   uint   `json:"sourceId"`
		Name       string `json:"name"`
		VideoCount int    `json:"videoCount"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &tags) != nil || len(tags) != 3 {
		t.Fatalf("应生成 3 个标签: %d %s", w.Code, w.Body.String())
	}
	var scifiID uint
	for _, tg := range tags {
		if tg.SourceID != 1 || tg.Name == "" {
			t.Errorf("标签数据异常: %+v", tg)
		}
		if tg.Name == "科幻" {
			scifiID = tg.ID
			if tg.VideoCount != 2 {
				t.Errorf("「科幻」影片数应为 2，实际 %d", tg.VideoCount)
			}
		}
	}
	if scifiID == 0 {
		t.Fatalf("未生成「科幻」标签: %+v", tags)
	}

	// 按站点过滤：不存在的站点返回空数组。
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/tags/v1?source_id=999", nil))
	if !strings.Contains(w.Body.String(), "[]") {
		t.Errorf("按站点过滤标签异常: %s", w.Body.String())
	}

	// 按标签筛选影片：科幻命中 2 条。
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet,
		"/api/videos/v1?tag_id="+strconv.Itoa(int(scifiID)), nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"total":2`) {
		t.Fatalf("按标签筛选影片异常: %d %s", w.Code, w.Body.String())
	}
}
