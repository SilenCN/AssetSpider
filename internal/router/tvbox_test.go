package router_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// tvboxReq 发起一条带路径 key 的 TVBox 请求（无登录会话）。
func tvboxReq(r *gin.Engine, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(""))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// seedTVBoxData 直接开库写入一个站点、一个分类与三条影片。
func seedTVBoxData(t *testing.T, dbPath string) uint {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	src := model.Source{Name: "TVBox测试站", APIBase: "https://example.com/provide/vod/", Enabled: true, Format: model.FormatMacCMSJSON}
	if err := db.Create(&src).Error; err != nil {
		t.Fatal(err)
	}
	cat := model.Category{SourceID: src.ID, Key: "cat-action", TypeID: "10", Name: "动作片", Level: 1}
	if err := db.Create(&cat).Error; err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		v := model.Video{
			SourceID: src.ID, CategoryID: cat.ID, VodID: fmt.Sprintf("vod-%d", i),
			TypeID: "10", TypeName: "动作片", VodName: fmt.Sprintf("影片%d", i),
			VodPic: fmt.Sprintf("https://img/%d.jpg", i), VodTime: "2026-11-17 10:00:00",
			VodPlayFrom: "default", VodPlayURL: fmt.Sprintf("第1集$https://v/%d.m3u8", i),
		}
		if err := db.Create(&v).Error; err != nil {
			t.Fatal(err)
		}
	}
	return src.ID
}

// genKey 通过登录态接口为当前用户生成 api key 并取出明文。
func genKey(t *testing.T, r *gin.Engine) string {
	t.Helper()
	w := doJSON(r, http.MethodPost, "/api/auth/v1/api-key", map[string]interface{}{})
	if w.Code != http.StatusOK {
		t.Fatalf("生成 key 失败: %d %s", w.Code, w.Body.String())
	}
	var out struct{ Key string }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Key == "" {
		t.Fatalf("生成 key 响应异常: %s", w.Body.String())
	}
	return out.Key
}

func TestTVBoxConfigAndVodProtocol(t *testing.T) {
	r, dbPath := newTestRouter(t)
	sourceID := seedTVBoxData(t, dbPath)
	key := genKey(t, r)

	// 1) config：返回 type=1 的站点，api 内嵌 key 与 sourceID。
	w := tvboxReq(r, http.MethodGet, "/tvbox/"+key+"/config")
	if w.Code != http.StatusOK {
		t.Fatalf("config 状态码: %d %s", w.Code, w.Body.String())
	}
	var cfg struct {
		Sites []struct {
			Key  string `json:"key"`
			API  string `json:"api"`
			Type int    `json:"type"`
		} `json:"sites"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sites) != 1 || cfg.Sites[0].Type != 1 {
		t.Fatalf("config sites 异常: %s", w.Body.String())
	}
	if !strings.Contains(cfg.Sites[0].API, "/tvbox/"+key+"/vod/"+fmt.Sprint(sourceID)) {
		t.Fatalf("config api 未内嵌 key/sourceID: %s", cfg.Sites[0].API)
	}

	// 2) ac=list：返回苹果CMS 分类 class[]。
	w = tvboxReq(r, http.MethodGet, fmt.Sprintf("/tvbox/%s/vod/%d?ac=list", key, sourceID))
	var listResp struct {
		Code  int `json:"code"`
		Class []struct {
			TypeID string `json:"type_id"`
			Name   string `json:"type_name"`
		} `json:"class"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	if listResp.Code != 1 || len(listResp.Class) != 1 || listResp.Class[0].TypeID != "10" {
		t.Fatalf("ac=list 响应异常: %s", w.Body.String())
	}

	// 3) ac=videolist：带 total/pagecount 与播放字段。
	w = tvboxReq(r, http.MethodGet, fmt.Sprintf("/tvbox/%s/vod/%d?ac=videolist&pg=1&limit=2", key, sourceID))
	var feed struct {
		Code      int `json:"code"`
		Total     int `json:"total"`
		PageCount int `json:"pagecount"`
		List      []struct {
			VodID      string `json:"vod_id"`
			TypeName   string `json:"type_name"`
			VodPlayURL string `json:"vod_play_url"`
		} `json:"list"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &feed); err != nil {
		t.Fatal(err)
	}
	if feed.Code != 1 || feed.Total != 3 || feed.PageCount != 2 || len(feed.List) != 2 {
		t.Fatalf("ac=videolist 分页异常: %s", w.Body.String())
	}
	if feed.List[0].TypeName != "动作片" || !strings.Contains(feed.List[0].VodPlayURL, "第1集$") {
		t.Fatalf("播放字段 / 分类名未透出: %s", w.Body.String())
	}

	// 4) ac=videolist&t=10：按苹果CMS type_id 过滤仍能命中。
	w = tvboxReq(r, http.MethodGet, fmt.Sprintf("/tvbox/%s/vod/%d?ac=videolist&t=10", key, sourceID))
	var byType struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &byType); err != nil || byType.Total != 3 {
		t.Fatalf("按 t 过滤异常: %s", w.Body.String())
	}

	// 5) ac=detail&ids：按 vod_id 取详情。
	w = tvboxReq(r, http.MethodGet, fmt.Sprintf("/tvbox/%s/vod/%d?ac=detail&ids=vod-2", key, sourceID))
	var detail struct {
		Code      int    `json:"code"`
		Msg       string `json:"msg"`
		Page      int    `json:"page"`
		PageCount int    `json:"pagecount"`
		Limit     int    `json:"limit"`
		Total     int    `json:"total"`
		List      []struct {
			VodID      string `json:"vod_id"`
			VodPlayURL string `json:"vod_play_url"`
		} `json:"list"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || len(detail.List) != 1 || detail.List[0].VodID != "vod-2" {
		t.Fatalf("ac=detail 异常: %s", w.Body.String())
	}
	// 部分 TVBox / 影视仓修改版用统一分页解析器读取这些字段，缺失会把详情
	// 判为无效并显示「没找到数据」。detail 必须与 videolist 顶层结构一致。
	if detail.Page != 1 || detail.PageCount != 1 || detail.Limit < 1 || detail.Total != 1 {
		t.Fatalf("ac=detail 缺少完整分页字段: %s", w.Body.String())
	}
	// 回归守卫：q215613905/TVBoxOS 等老版 TVBox 在 DetailActivity 里判定
	// msg 非空且 != "数据列表" 就 showEmpty() 并 return，导致播放组/播放列表
	// 一律无法显示（"没找到数据"）。msg 必须是苹果CMS 标准值 "数据列表"。
	if detail.Msg != "数据列表" {
		t.Fatalf("ac=detail msg 非苹果CMS标准值（应为\"数据列表\"）: %q, body=%s", detail.Msg, w.Body.String())
	}

	// 6) 客户端点播详情的写法并不统一：ids 搭配任意 ac、仅 ids、单数 id、
	//    pagesize 等，都必须精确返回目标影片并带播放字段，否则点击影片后
	//    拿不到播放组 / 播放列表。这些是协议兼容回归用例。
	detailPaths := []string{
		fmt.Sprintf("/tvbox/%s/vod/%d?ac=videolist&ids=vod-2", key, sourceID),
		fmt.Sprintf("/tvbox/%s/vod/%d?ids=vod-2", key, sourceID),
		fmt.Sprintf("/tvbox/%s/vod/%d?ac=detail&id=vod-2", key, sourceID),
		fmt.Sprintf("/tvbox/%s/vod/%d?ac=list&ids=vod-2&pagesize=5", key, sourceID),
	}
	for _, path := range detailPaths {
		w = tvboxReq(r, http.MethodGet, path)
		var got struct {
			List []struct {
				VodID       string `json:"vod_id"`
				VodPlayFrom string `json:"vod_play_from"`
				VodPlayURL  string `json:"vod_play_url"`
			} `json:"list"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s 响应非法: %v | %s", path, err, w.Body.String())
		}
		if len(got.List) != 1 || got.List[0].VodID != "vod-2" {
			t.Fatalf("%s 未按 ids 精确返回: %s", path, w.Body.String())
		}
		if !strings.Contains(got.List[0].VodPlayURL, "第1集$") || got.List[0].VodPlayFrom == "" {
			t.Fatalf("%s 播放组 / 播放列表缺失: %s", path, w.Body.String())
		}
	}

	// 7) ids 顺序：传入多个时按 ids 顺序返回，客户端点哪部哪部在前。
	w = tvboxReq(r, http.MethodGet, fmt.Sprintf("/tvbox/%s/vod/%d?ac=detail&ids=vod-3,vod-1", key, sourceID))
	var ordered struct {
		List []struct {
			VodID string `json:"vod_id"`
		} `json:"list"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ordered); err != nil || len(ordered.List) != 2 ||
		ordered.List[0].VodID != "vod-3" || ordered.List[1].VodID != "vod-1" {
		t.Fatalf("ids 顺序未按入参返回: %s", w.Body.String())
	}

	// 8) ac=detail 不带 ids：按苹果CMS 语义返回含播放字段的详情列表（非空）。
	w = tvboxReq(r, http.MethodGet, fmt.Sprintf("/tvbox/%s/vod/%d?ac=detail", key, sourceID))
	var bareDetail struct {
		List []struct {
			VodPlayURL string `json:"vod_play_url"`
		} `json:"list"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &bareDetail); err != nil || len(bareDetail.List) == 0 ||
		!strings.Contains(bareDetail.List[0].VodPlayURL, "第1集$") {
		t.Fatalf("ac=detail 无 ids 应返回详情列表: %s", w.Body.String())
	}
}

func TestTVBoxKeyAuthAndVisibility(t *testing.T) {
	r, dbPath := newTestRouter(t)
	sourceID := seedTVBoxData(t, dbPath)
	key := genKey(t, r)

	// 无 key / 错误 key 一律 401。
	if w := tvboxReq(r, http.MethodGet, "/tvbox//config"); w.Code != http.StatusUnauthorized && w.Code != http.StatusNotFound {
		t.Fatalf("空 key 应被拒绝，实际 %d", w.Code)
	}
	if w := tvboxReq(r, http.MethodGet, "/tvbox/wrongkey/config"); w.Code != http.StatusUnauthorized {
		t.Fatalf("错误 key 应 401，实际 %d", w.Code)
	}

	// 重置后旧 key 失效、新 key 可用。
	w := doJSON(r, http.MethodPost, "/api/auth/v1/api-key", map[string]interface{}{})
	var regenerated struct{ Key string }
	if err := json.Unmarshal(w.Body.Bytes(), &regenerated); err != nil || regenerated.Key == key {
		t.Fatalf("重置应产生新 key: %s", w.Body.String())
	}
	if w := tvboxReq(r, http.MethodGet, "/tvbox/"+key+"/config"); w.Code != http.StatusUnauthorized {
		t.Fatalf("旧 key 应失效，实际 %d", w.Code)
	}
	if w := tvboxReq(r, http.MethodGet, "/tvbox/"+regenerated.Key+"/config"); w.Code != http.StatusOK {
		t.Fatalf("新 key 应可用，实际 %d", w.Code)
	}

	// 撤销后新 key 也失效。
	if w := doJSON(r, http.MethodDelete, "/api/auth/v1/api-key", nil); w.Code != http.StatusOK {
		t.Fatalf("撤销失败: %d %s", w.Code, w.Body.String())
	}
	if w := tvboxReq(r, http.MethodGet, "/tvbox/"+regenerated.Key+"/config"); w.Code != http.StatusUnauthorized {
		t.Fatalf("撤销后 key 应失效，实际 %d", w.Code)
	}
	_ = sourceID
}

// TestTVBoxSourceVisibility 验证普通用户的 TVBox 接口只能看到被授权站点：
// config 只列授权站点，访问未授权站点的 vod 返回 403。
func TestTVBoxSourceVisibility(t *testing.T) {
	r, dbPath := newTestRouter(t)
	sourceA := seedTVBoxData(t, dbPath)
	// 再造一个未授权站点 B。
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	srcB := model.Source{Name: "未授权站", APIBase: "https://b.example.com/provide/vod/", Enabled: true, Format: model.FormatMacCMSJSON}
	if err := db.Create(&srcB).Error; err != nil {
		t.Fatal(err)
	}

	authCreateUser(t, r, testToken, "viewer", "pass123456", []uint{sourceA})
	viewerToken, _ := login(t, r, "viewer", "pass123456")
	// 用 viewer 自己的登录态生成 key。
	w := authCall(r, http.MethodPost, "/api/auth/v1/api-key", viewerToken, map[string]interface{}{})
	var out struct{ Key string }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Key == "" {
		t.Fatalf("viewer 生成 key 失败: %d %s", w.Code, w.Body.String())
	}
	key := out.Key

	// config 只应列出授权站点 A。
	w = tvboxReq(r, http.MethodGet, "/tvbox/"+key+"/config")
	var cfg struct {
		Sites []struct {
			API string `json:"api"`
		} `json:"sites"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sites) != 1 || !strings.Contains(cfg.Sites[0].API, fmt.Sprintf("/vod/%d", sourceA)) {
		t.Fatalf("config 应只列授权站点，实际 %s", w.Body.String())
	}

	// 访问未授权站点 B 的 vod → 403。
	if w := tvboxReq(r, http.MethodGet, fmt.Sprintf("/tvbox/%s/vod/%d?ac=videolist", key, srcB.ID)); w.Code != http.StatusForbidden {
		t.Fatalf("未授权站点应 403，实际 %d %s", w.Code, w.Body.String())
	}

	// 管理员的 key 可见全部站点（含 B）。
	adminKey := genKey(t, r)
	w = tvboxReq(r, http.MethodGet, "/tvbox/"+adminKey+"/config")
	if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sites) < 2 {
		t.Fatalf("管理员 config 应含全部站点，实际 %s", w.Body.String())
	}
}
