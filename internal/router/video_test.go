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
	// 2026-11 裁剪 23 个后台管理字段后标准字段为 68、分组为 13，精确锁定防误增删。
	if meta.Total != 68 || len(meta.Fields) != meta.Total || len(meta.Groups) != 13 {
		t.Fatalf("字段元数据异常: total=%d fields=%d groups=%d（应 68/68/13）", meta.Total, len(meta.Fields), len(meta.Groups))
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
	// 列表只返回卡片投影：播放组 / 播放地址 / 简介 / 扩展字段等完整记录内容
	// 不随列表下发，用户点开影片时由详情接口按 id 单独返回。
	for _, item := range list.Items {
		for _, full := range []string{"vodPlayUrl", "vodPlayList", "vodContent", "extra"} {
			if _, ok := item[full]; ok {
				t.Errorf("列表卡片不应包含完整字段 %s: %+v", full, item)
			}
		}
	}

	// 点开列表里第一张卡片：按 id 请求详情，完整结构化字段随详情接口返回。
	id := int(v["id"].(float64))
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/videos/v1/"+strconv.Itoa(id), nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || strings.Contains(body, `"raw"`) || !strings.Contains(body, `"vodName"`) {
		t.Fatalf("详情接口异常（不应含 raw）: %d %s", w.Code, body)
	}
	var detail map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	// 详情接口返回完整记录：列表卡片没有的播放组 / 播放地址 / 简介字段
	// 都随详情返回（字段值可为空，但键必须存在，证明下发的是完整 model.Video）。
	for _, full := range []string{"vodPlayUrl", "vodPlayList", "vodContent"} {
		if _, ok := detail[full]; !ok {
			t.Errorf("详情接口应返回完整字段 %s: %+v", full, detail)
		}
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/videos/v1/9999", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("不存在的影片应返回 404，实际 %d", w.Code)
	}
}

// TestAPIRoutesFollowModuleVersionConvention 固化 /api/{模块名}/{版本号} 约定：
// 模块必须是复数资源名词，版本段必须存在，路径中不允许出现动词。

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

// TestVideoSearchSortAndCardProjection 验证移除标签 / 地域 / 年份筛选与评分排序后，
// 资源库仍保留关键词检索与更新时间排序；列表只返回卡片字段，完整记录由详情接口按 id 返回。
func TestVideoSearchSortAndCardProjection(t *testing.T) {
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

	type listResp struct {
		Items []map[string]interface{} `json:"items"`
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

	// 列表卡片：结构化字段在，但不含播放组 / 简介 / 扩展字段等完整记录内容。
	all := get("")
	if len(all.Items) != 2 {
		t.Fatalf("默认应返回 2 条，实际 %d", len(all.Items))
	}
	for _, item := range all.Items {
		for _, full := range []string{"vodPlayUrl", "vodPlayList", "vodContent", "extra", "raw"} {
			if _, ok := item[full]; ok {
				t.Errorf("列表卡片不应包含完整字段 %s: %+v", full, item)
			}
		}
	}

	// 名称检索
	if got := get("q=影片A"); len(got.Items) != 1 || got.Items[0]["vodName"] != "测试影片A" {
		t.Errorf("名称检索异常: %+v", got.Items)
	}
	// 按更新时间排序：B 的 vod_time 更晚
	if got := get("sort=time_desc"); got.Items[0]["vodName"] != "测试影片B" {
		t.Errorf("按更新时间倒序异常: %+v", got.Items)
	}
	if got := get("sort=time_asc"); got.Items[0]["vodName"] != "测试影片A" {
		t.Errorf("按更新时间升序异常: %+v", got.Items)
	}

	// 已移除的枚举端点：areas / tags 是独立路由组，移除后直接 404；
	// years 会落到 /api/videos/v1/:id，因 id 非数字返回 400，两者都表示资源不再可用。
	for _, path := range []string{"/api/areas/v1", "/api/videos/v1/years", "/api/tags/v1"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authReq(http.MethodGet, path, nil))
		if w.Code == http.StatusOK {
			t.Errorf("已移除的端点 %s 不应再返回 200，实际 %d", path, w.Code)
		}
	}
	// 已移除的筛选参数与评分排序：被忽略，不报错、也不应把结果筛空。
	for _, qs := range []string{"area_id=1", "year=1999", "sort=score_desc", "sort=score_asc"} {
		if got := get(qs); len(got.Items) != 2 {
			t.Errorf("已移除的参数 %s 不应影响列表结果，实际 %d 条", qs, len(got.Items))
		}
	}

	// 点开第一张卡片：按 id 取详情，完整字段随详情接口返回。
	id := int(all.Items[0]["id"].(float64))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/videos/v1/"+strconv.Itoa(id), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("详情接口异常: %d %s", w.Code, w.Body.String())
	}
	var detail map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if _, ok := detail["vodPlayUrl"]; !ok {
		t.Errorf("详情接口应返回完整字段 vodPlayUrl: %+v", detail)
	}
}

// TestTaskPauseResumeAndCapacity 验证采集任务的手动暂停/继续与并发上限配置：
// 任务异步入队 → 运行中可暂停（保存续采页码）→ 继续时从暂停页码接着采。
