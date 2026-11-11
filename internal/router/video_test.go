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

	// 地域枚举：地域已从 vod_area 字符串拆成独立实体（与标签同构），
	// 接口返回带主键与影片数，筛选按 area_id。
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/areas/v1", nil))
	var areas []struct {
		ID         uint   `json:"id"`
		Name       string `json:"name"`
		VideoCount int64  `json:"videoCount"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &areas) != nil {
		t.Fatalf("地域接口异常: %d %s", w.Code, w.Body.String())
	}
	if len(areas) != 2 {
		t.Fatalf("应有 2 个地域，实际 %v", areas)
	}
	areaID := map[string]uint{}
	for _, a := range areas {
		areaID[a.Name] = a.ID
		if a.VideoCount != 1 {
			t.Errorf("地域 %s 的影片数应为 1，实际 %d", a.Name, a.VideoCount)
		}
	}
	if areaID["大陆"] == 0 || areaID["美国"] == 0 {
		t.Fatalf("地域名称缺失: %v", areas)
	}

	// 年份枚举：年份不拆表，直接按 vod_year 聚合（影片上的单值属性），
	// 下拉选项按年份倒序并带影片数，筛选按 year 等值匹配。
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/videos/v1/years", nil))
	var years []struct {
		Year       string `json:"year"`
		VideoCount int64  `json:"videoCount"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &years) != nil {
		t.Fatalf("年份接口异常: %d %s", w.Code, w.Body.String())
	}
	if len(years) != 2 || years[0].Year != "2025" || years[1].Year != "2024" {
		t.Fatalf("年份枚举异常（应为 2025、2024 倒序）: %+v", years)
	}
	for _, y := range years {
		if y.VideoCount != 1 {
			t.Errorf("年份 %s 的影片数应为 1，实际 %d", y.Year, y.VideoCount)
		}
	}

	// 年份筛选（get 在下方定义，见「地域筛选」位置）
	type listResp struct {
		Items []model.Video `json:"items"`
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

	// 地域筛选（按 area_id，走 video_areas 连接表）
	if got := get("area_id=" + strconv.Itoa(int(areaID["大陆"]))); len(got.Items) != 1 || got.Items[0].VodName != "测试影片A" {
		t.Errorf("按地域筛选异常: %+v", got)
	}
	// 年份筛选：等值匹配，不存在的年份返回空
	if got := get("year=2024"); len(got.Items) != 1 || got.Items[0].VodName != "测试影片A" {
		t.Errorf("按年份筛选异常: %+v", got)
	}
	if got := get("year=1999"); len(got.Items) != 0 {
		t.Errorf("不存在的年份应无结果，实际 %+v", got)
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
	if got := get("q=影片A"); len(got.Items) != 1 || got.Items[0].VodName != "测试影片A" {
		t.Errorf("名称检索异常: %+v", got)
	}
}

// TestTaskPauseResumeAndCapacity 验证采集任务的手动暂停/继续与并发上限配置：
// 任务异步入队 → 运行中可暂停（保存续采页码）→ 继续时从暂停页码接着采。
