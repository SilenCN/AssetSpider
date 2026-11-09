package router_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/service"
	"github.com/gin-gonic/gin"
)

type exportedBundle struct {
	Version    int                      `json:"version"`
	ExportedAt string                   `json:"exportedAt"`
	Sources    []map[string]interface{} `json:"sources"`
}

type importResultView struct {
	Total   int `json:"total"`
	Created int `json:"created"`
	Updated int `json:"updated"`
	Failed  int `json:"failed"`
	Issues  []struct {
		Index int    `json:"index"`
		Name  string `json:"name"`
		Error string `json:"error"`
	} `json:"issues"`
}

func sourceExport(t *testing.T, r *gin.Engine) exportedBundle {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/sources/v1/export", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("导出站点失败: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Content-Disposition"), ".json") {
		t.Fatalf("导出响应应带 JSON 附件名: %q", w.Header().Get("Content-Disposition"))
	}
	var out exportedBundle
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("导出内容不是合法 JSON: %v", err)
	}
	return out
}

func sourceImport(t *testing.T, r *gin.Engine, payload interface{}) (importResultView, *httptest.ResponseRecorder) {
	t.Helper()
	w := doJSON(r, http.MethodPost, "/api/sources/v1/import", payload)
	var out importResultView
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out, w
}

func sampleSources() []map[string]interface{} {
	return []map[string]interface{}{
		{
			"name": "导出站A", "apiBase": "https://a.example.com/api.php/provide/vod/",
			"format": "maccms_json", "extraParams": "t=1", "enabled": true,
			"incrementalHours": 12, "maxPages": 7,
			"parseApi": "https://jx.example.com/?url=", "playDomain": "cdn.example.com",
			"picApi": "https://img.example.com/proxy?url=", "picDomain": "img.example.com",
		},
		{
			"name": "导出站B", "apiBase": "https://b.example.com/api.php/provide/vod/",
			"format": "seacms_json", "enabled": false,
			"incrementalHours": 24, "maxPages": 0,
		},
	}
}

func sourceList(t *testing.T, r *gin.Engine) []map[string]interface{} {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/sources/v1", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("读取站点列表失败: %d %s", w.Code, w.Body.String())
	}
	var out []map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSourceExportImportRoundTrip 验证导出的是纯配置、按名称 upsert 且重复导入不产生重复站点。
func TestSourceExportImportRoundTrip(t *testing.T) {
	r, _ := newTestRouter(t)
	for _, src := range sampleSources() {
		if w := doJSON(r, http.MethodPost, "/api/sources/v1", src); w.Code != http.StatusCreated {
			t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
		}
	}

	bundle := sourceExport(t, r)
	if bundle.Version != service.SourceBundleVersion || bundle.ExportedAt == "" {
		t.Fatalf("导出文件应带版本与时间: %+v", bundle)
	}
	if len(bundle.Sources) != 2 {
		t.Fatalf("应导出 2 个站点，实际 %d", len(bundle.Sources))
	}
	first := bundle.Sources[0]
	for _, key := range []string{"id", "videoCount", "lastStatus", "lastCursor", "createdAt", "updatedAt"} {
		if _, ok := first[key]; ok {
			t.Errorf("导出内容不应包含运行时字段 %q", key)
		}
	}
	if first["name"] != "导出站A" || first["playDomain"] != "https://cdn.example.com" {
		t.Errorf("导出内容应保留可编辑字段并已规范化域名: %+v", first)
	}
	if first["enabled"] != true || bundle.Sources[1]["enabled"] != false {
		t.Errorf("启用状态应完整导出: %+v / %+v", first, bundle.Sources[1])
	}

	// 改掉一个字段后导入原文件：同名站点被更新，配置回到导出时的值。
	if w := doJSON(r, http.MethodPut, "/api/sources/v1/1", map[string]interface{}{
		"name": "导出站A", "apiBase": "https://a.example.com/api.php/provide/vod/",
		"maxPages": 99,
	}); w.Code != http.StatusOK {
		t.Fatalf("修改站点失败: %d %s", w.Code, w.Body.String())
	}
	res, w := sourceImport(t, r, bundle)
	if w.Code != http.StatusOK {
		t.Fatalf("导入失败: %d %s", w.Code, w.Body.String())
	}
	if res.Total != 2 || res.Created != 0 || res.Updated != 2 || res.Failed != 0 {
		t.Fatalf("重复导入应全部按名称更新: %+v", res)
	}
	if list := sourceList(t, r); len(list) != 2 || list[0]["maxPages"] != float64(7) {
		t.Fatalf("导入应恢复配置且不新增记录: %+v", list)
	}

	// 删掉 B 站再导入：缺的那条被新建，A 站仍走更新。
	del := httptest.NewRecorder()
	r.ServeHTTP(del, authReq(http.MethodDelete, "/api/sources/v1/2", nil))
	if del.Code != http.StatusNoContent {
		t.Fatalf("删除站点失败: %d %s", del.Code, del.Body.String())
	}
	res, w = sourceImport(t, r, bundle)
	if res.Created != 1 || res.Updated != 1 || res.Failed != 0 {
		t.Fatalf("缺失的站点应新建、已有的应更新: %+v", res)
	}
	if list := sourceList(t, r); len(list) != 2 {
		t.Fatalf("导入后应仍是 2 个站点，实际 %d", len(list))
	}
}

// TestSourceImportFormatsAndValidation 覆盖三种 JSON 形态与逐条校验。
func TestSourceImportFormatsAndValidation(t *testing.T) {
	r, _ := newTestRouter(t)

	// 裸数组
	res, w := sourceImport(t, r, sampleSources())
	if w.Code != http.StatusOK || res.Created != 2 || res.Failed != 0 {
		t.Fatalf("裸数组导入失败: %d %+v | %s", w.Code, res, w.Body.String())
	}
	// 单条对象
	res, w = sourceImport(t, r, map[string]interface{}{
		"name": "单条站点", "apiBase": "https://one.example.com/api.php/provide/vod/",
	})
	if w.Code != http.StatusOK || res.Created != 1 || res.Total != 1 {
		t.Fatalf("单条对象导入失败: %d %+v", w.Code, res)
	}
	// 导出文件结构
	res, w = sourceImport(t, r, map[string]interface{}{
		"version": 1, "sources": []map[string]interface{}{
			{"name": "单条站点", "apiBase": "https://one.example.com/api.php/provide/vod/", "maxPages": 3},
		},
	})
	if w.Code != http.StatusOK || res.Created != 0 || res.Updated != 1 {
		t.Fatalf("导出文件结构导入失败: %d %+v", w.Code, res)
	}

	// 合法 + 两条非法：只失败非法条目，并给出条目下标与原因
	mixed := []map[string]interface{}{
		{"name": "合法站点", "apiBase": "https://ok.example.com/api.php/provide/vod/"},
		{"name": "", "apiBase": "https://bad.example.com/"},
		{"name": "地址非法", "apiBase": "ftp://bad.example.com/"},
	}
	res, w = sourceImport(t, r, mixed)
	if w.Code != http.StatusOK || res.Created != 1 || res.Failed != 2 || len(res.Issues) != 2 {
		t.Fatalf("混合导入统计异常: %d %+v | %s", w.Code, res, w.Body.String())
	}
	if res.Issues[0].Index != 1 || res.Issues[1].Index != 2 || res.Issues[1].Name != "地址非法" {
		t.Fatalf("错误条目应带下标与站点名: %+v", res.Issues)
	}

	// 格式错误 / 空数组 / 超大文件
	for _, tc := range []struct {
		name    string
		payload interface{}
	}{
		{"空数组", []interface{}{}},
		{"sources 不是数组", map[string]interface{}{"sources": "oops"}},
	} {
		_, w := sourceImport(t, r, tc.payload)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s 应返回 400，实际 %d %s", tc.name, w.Code, w.Body.String())
		}
	}
	for _, raw := range []string{"{ not json", `["not-a-source"]`} {
		req := authReq(http.MethodPost, "/api/sources/v1/import", strings.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("非法 JSON %q 应返回 400，实际 %d %s", raw, w.Code, w.Body.String())
		}
	}
	req := authReq(http.MethodPost, "/api/sources/v1/import", strings.NewReader(strings.Repeat("x", (2<<20)+1)))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("超大文件应返回 400，实际 %d", w.Code)
	}
}

// TestSourceImportRequiresAdmin 验证导入仅管理员可做，导出按站点可见性过滤。
func TestSourceImportRequiresAdmin(t *testing.T) {
	r, _ := newTestRouter(t)
	admin := testToken
	authCreateSource(t, r, admin, "可见站点", "https://visible.example.com/api.php/provide/vod/")
	authCreateSource(t, r, admin, "不可见站点", "https://hidden.example.com/api.php/provide/vod/")
	authCreateUser(t, r, admin, "viewer", "pass123456", []uint{1})
	userToken, _ := login(t, r, "viewer", "pass123456")

	// 普通用户导入：403
	req := httptest.NewRequest(http.MethodPost, "/api/sources/v1/import",
		jsonBody(map[string]interface{}{"name": "偷偷加的站点", "apiBase": "https://x.example.com/"}))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+userToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("普通用户导入应返回 403，实际 %d %s", w.Code, w.Body.String())
	}

	// 普通用户导出：只含授权站点
	req = httptest.NewRequest(http.MethodGet, "/api/sources/v1/export", nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("普通用户导出失败: %d %s", w.Code, w.Body.String())
	}
	var bundle exportedBundle
	if err := json.Unmarshal(w.Body.Bytes(), &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Sources) != 1 || bundle.Sources[0]["name"] != "可见站点" {
		t.Fatalf("导出应只含授权站点: %+v", bundle.Sources)
	}
}

// TestSourceCreateDuplicateNameRejected 验证手动新增站点时按名称去重
// （忽略大小写与首尾空白），重名返回 409；与导入的「重名即更新」互补——
// 手工建站不允许重名，必须走编辑或导入更新。
func TestSourceCreateDuplicateNameRejected(t *testing.T) {
	r, _ := newTestRouter(t)
	if w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "重名站", "apiBase": "https://dup.example.com/api.php/provide/vod/",
	}); w.Code != http.StatusCreated {
		t.Fatalf("首次创建失败: %d %s", w.Code, w.Body.String())
	}
	// 完全相同
	if w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "重名站", "apiBase": "https://dup2.example.com/api.php/provide/vod/",
	}); w.Code != http.StatusConflict {
		t.Fatalf("同名应返回 409，实际 %d %s", w.Code, w.Body.String())
	}
	// 仅首尾空白不同
	if w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "  重名站  ", "apiBase": "https://dup3.example.com/api.php/provide/vod/",
	}); w.Code != http.StatusConflict {
		t.Fatalf("仅首尾空白不同的同名应返回 409，实际 %d %s", w.Code, w.Body.String())
	}
	// 不同名应成功
	if w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "另一个站", "apiBase": "https://dup4.example.com/api.php/provide/vod/",
	}); w.Code != http.StatusCreated {
		t.Fatalf("不同名应创建成功: %d %s", w.Code, w.Body.String())
	}
}

// TestSourceUpdateToExistingNameRejected 验证编辑站点时不能把名称改成另一条已存在站点，
// 但改名成「自身」（含仅大小写不同）是允许的。
func TestSourceUpdateToExistingNameRejected(t *testing.T) {
	r, _ := newTestRouter(t)
	authCreateSource(t, r, testToken, "SiteA", "https://a.example.com/api.php/provide/vod/")
	authCreateSource(t, r, testToken, "SiteB", "https://b.example.com/api.php/provide/vod/")

	// 甲改名成乙：409
	if w := doJSON(r, http.MethodPut, "/api/sources/v1/1", map[string]interface{}{
		"name": "SiteB", "apiBase": "https://a.example.com/api.php/provide/vod/",
	}); w.Code != http.StatusConflict {
		t.Fatalf("改名成已存在站点应返回 409，实际 %d %s", w.Code, w.Body.String())
	}
	// 甲改名成乙的大小写变体 siteb：大小写不敏感，仍 409
	if w := doJSON(r, http.MethodPut, "/api/sources/v1/1", map[string]interface{}{
		"name": "siteb", "apiBase": "https://a.example.com/api.php/provide/vod/",
	}); w.Code != http.StatusConflict {
		t.Fatalf("大小写变体仍应冲突，实际 %d %s", w.Code, w.Body.String())
	}
	// 甲改名成自身（仅改其它字段）：200
	if w := doJSON(r, http.MethodPut, "/api/sources/v1/1", map[string]interface{}{
		"name": "SiteA", "apiBase": "https://a.example.com/api.php/provide/vod/", "maxPages": 5,
	}); w.Code != http.StatusOK {
		t.Fatalf("改名成自身应成功，实际 %d %s", w.Code, w.Body.String())
	}
	// 甲改名成仅大小写不同的 Sitea（指向自身）：200
	if w := doJSON(r, http.MethodPut, "/api/sources/v1/1", map[string]interface{}{
		"name": "Sitea", "apiBase": "https://a.example.com/api.php/provide/vod/",
	}); w.Code != http.StatusOK {
		t.Fatalf("大小写变体指向自身应成功，实际 %d %s", w.Code, w.Body.String())
	}
}
