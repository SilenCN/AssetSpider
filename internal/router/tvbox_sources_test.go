package router_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestTVBoxSourcesAdminAvailableNull 保护管理员场景：available 必须是 JSON null
// （表示"全部站点"），不能是 []，否则前端会把它当"无站点"，「我的接口」页
// 的站点勾选列表为空。
func TestTVBoxSourcesAdminAvailableNull(t *testing.T) {
	r, _ := newTestRouter(t)
	w := authCall(r, http.MethodGet, "/api/auth/v1/tvbox-sources", testToken, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("管理员 GET tvbox-sources 失败: %d %s", w.Code, w.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("解析响应失败: %v; body=%s", err, w.Body.String())
	}
	avail, ok := raw["available"]
	if !ok {
		t.Fatalf("响应缺少 available 字段: %s", w.Body.String())
	}
	if string(avail) != "null" {
		t.Fatalf("管理员 available 应为 null, 实际为 %s; body=%s", string(avail), w.Body.String())
	}
}

// TestTVBoxSourcesSelection 覆盖 TVBox 站点自选的完整链路：
// 默认全开 → selected 只列勾选站点 → 屏蔽站点无法经 vod 绕过 → 越权勾选被拒 → 切回全开。
func TestTVBoxSourcesSelection(t *testing.T) {
	r, dbPath := newTestRouter(t)

	// 两个已启用的采集站点。
	srcA := seedTVBoxData(t, dbPath) // 站点 A：表名 "TVBox测试站"
	srcB := authCreateSource(t, r, testToken, "站点B", "https://example.org/provide/vod/")

	// 一个普通用户，授权可见这两个站点。
	const username, password = "viewer_sel", "pass123456"
	authCreateUser(t, r, testToken, username, password, []uint{srcA, srcB})
	userToken, _ := login(t, r, username, password)

	// 为该用户生成 TVBox key。genKey 用管理员令牌，这里需要用户自己的令牌。
	w := authCall(r, http.MethodPost, "/api/auth/v1/api-key", userToken, map[string]interface{}{})
	if w.Code != http.StatusOK {
		t.Fatalf("生成用户 api key 失败: %d %s", w.Code, w.Body.String())
	}
	var keyOut struct{ Key string }
	decode(t, w, &keyOut)
	key := keyOut.Key
	configPath := "/tvbox/" + key + "/config"

	// --- 1) 默认 all：config 列出两个可见站点 ---
	w = tvboxReq(r, http.MethodGet, configPath)
	var cfg struct {
		Sites []struct {
			API string `json:"api"`
		} `json:"sites"`
	}
	decode(t, w, &cfg)
	if len(cfg.Sites) != 2 {
		t.Fatalf("默认应列出全部可见站点(2): %s", w.Body.String())
	}

	// --- 2) GET tvbox-sources：mode=all，available 含两个站点 ---
	w = authCall(r, http.MethodGet, "/api/auth/v1/tvbox-sources", userToken, nil)
	var view struct {
		Mode      string `json:"mode"`
		Selected  []uint `json:"selected"`
		Available []uint `json:"available"`
	}
	decode(t, w, &view)
	if view.Mode != "all" || len(view.Available) != 2 {
		t.Fatalf("tvbox-sources 初始视图异常: %s", w.Body.String())
	}

	// --- 3) 保存 selected，只勾选站点 A ---
	w = authCall(r, http.MethodPut, "/api/auth/v1/tvbox-sources", userToken,
		map[string]interface{}{"mode": "selected", "selected": []uint{srcA}})
	decode(t, w, &view)
	if w.Code != http.StatusOK || view.Mode != "selected" || len(view.Selected) != 1 || view.Selected[0] != srcA {
		t.Fatalf("保存 selected 失败: %d %s", w.Code, w.Body.String())
	}

	// --- 4) config 现在只列出站点 A ---
	w = tvboxReq(r, http.MethodGet, configPath)
	cfg = struct {
		Sites []struct {
			API string `json:"api"`
		} `json:"sites"`
	}{}
	decode(t, w, &cfg)
	if len(cfg.Sites) != 1 || !strings.Contains(cfg.Sites[0].API, fmt.Sprintf("/vod/%d", srcA)) {
		t.Fatalf("selected 模式 config 应只列站点A: %s", w.Body.String())
	}

	// --- 5) 被屏蔽的站点 B 不能经 vod 接口绕过 ---
	w = tvboxReq(r, http.MethodGet, fmt.Sprintf("/tvbox/%s/vod/%d?ac=videolist", key, srcB))
	if w.Code != http.StatusForbidden {
		t.Fatalf("屏蔽站点 B 的 vod 请求应 403: %d %s", w.Code, w.Body.String())
	}
	// 勾选的站点 A 仍可正常访问。
	w = tvboxReq(r, http.MethodGet, fmt.Sprintf("/tvbox/%s/vod/%d?ac=videolist", key, srcA))
	if w.Code != http.StatusOK {
		t.Fatalf("勾选站点 A 的 vod 请求应 200: %d %s", w.Code, w.Body.String())
	}

	// --- 6) 越权勾选：勾选未授权的站点应被拒 ---
	// 建一个该用户无权访问的站点 C。
	srcC := authCreateSource(t, r, testToken, "站点C-未授权", "https://example.net/provide/vod/")
	w = authCall(r, http.MethodPut, "/api/auth/v1/tvbox-sources", userToken,
		map[string]interface{}{"mode": "selected", "selected": []uint{srcC}})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("越权勾选应 400: %d %s", w.Code, w.Body.String())
	}

	// --- 7) 切回 all：config 恢复全部可见站点 ---
	w = authCall(r, http.MethodPut, "/api/auth/v1/tvbox-sources", userToken,
		map[string]interface{}{"mode": "all", "selected": []uint{}})
	decode(t, w, &view)
	if w.Code != http.StatusOK || view.Mode != "all" {
		t.Fatalf("切回 all 失败: %d %s", w.Code, w.Body.String())
	}
	w = tvboxReq(r, http.MethodGet, configPath)
	decode(t, w, &cfg)
	if len(cfg.Sites) != 2 {
		t.Fatalf("切回 all 后应恢复全部可见站点(2): %s", w.Body.String())
	}
}
