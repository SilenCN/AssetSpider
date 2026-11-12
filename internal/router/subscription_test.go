package router_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SilenCN/AssetSpider/internal/service"
)

// TestSubscriptionLifecycle 走一遍订阅的完整 HTTP 链路：
// 新建 → 列表 → 手动刷新写入站点 → 修改 → 普通用户只读 → 删除后站点解绑。
func TestSubscriptionLifecycle(t *testing.T) {
	payload := `{"version":1,"sources":[{"name":"订阅站点","apiBase":"https://sub.example.com/api.php/provide/vod/"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	r, _ := newTestRouter(t)

	w := doJSON(r, http.MethodPost, "/api/subscriptions/v1", map[string]interface{}{
		"name": "公共订阅", "url": srv.URL + "/sources.json", "intervalHours": 6,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("创建订阅失败: %d %s", w.Code, w.Body.String())
	}
	var sub struct {
		ID            uint       `json:"id"`
		Enabled       bool       `json:"enabled"`
		NextRefreshAt *time.Time `json:"nextRefreshAt"`
	}
	decode(t, w, &sub)
	if sub.ID == 0 || !sub.Enabled {
		t.Fatalf("订阅默认应启用: %+v", sub)
	}
	if sub.NextRefreshAt == nil {
		t.Fatal("配置了间隔就应给出下次刷新时间")
	}

	w = authCall(r, http.MethodGet, "/api/subscriptions/v1", testToken, nil)
	var list []struct {
		ID   uint   `json:"id"`
		Name string `json:"name"`
	}
	decode(t, w, &list)
	if w.Code != http.StatusOK || len(list) != 1 || list[0].Name != "公共订阅" {
		t.Fatalf("订阅列表异常: %d %s", w.Code, w.Body.String())
	}

	// 手动刷新：把订阅里的站点写进站点表，并回报本次变更数量。
	refreshPath := "/api/subscriptions/v1/" + strconv.Itoa(int(sub.ID)) + "/refreshes"
	w = authCall(r, http.MethodPost, refreshPath, testToken, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("刷新订阅失败: %d %s", w.Code, w.Body.String())
	}
	var res service.RefreshResult
	decode(t, w, &res)
	if res.Created != 1 {
		t.Fatalf("刷新结果 = %+v，期望新增 1 个站点", res)
	}

	w = authCall(r, http.MethodGet, "/api/sources/v1", testToken, nil)
	var srcs []struct {
		Name           string `json:"name"`
		SubscriptionID uint   `json:"subscriptionId"`
	}
	decode(t, w, &srcs)
	if len(srcs) != 1 || srcs[0].Name != "订阅站点" || srcs[0].SubscriptionID != sub.ID {
		t.Fatalf("订阅没有把站点写进站点表: %+v", srcs)
	}

	// 订阅的刷新状态也应回到列表上，页面才能显示「上次刷新做了什么」。
	w = authCall(r, http.MethodGet, "/api/subscriptions/v1", testToken, nil)
	var views []struct {
		LastStatus  string `json:"lastStatus"`
		LastCreated int    `json:"lastCreated"`
		LastSyncAt  string `json:"lastSyncAt"`
	}
	decode(t, w, &views)
	if len(views) != 1 || views[0].LastStatus != service.SubscriptionSuccess || views[0].LastCreated != 1 || views[0].LastSyncAt == "" {
		t.Fatalf("刷新结果未写回订阅: %+v", views)
	}

	w = doJSON(r, http.MethodPut, "/api/subscriptions/v1/"+strconv.Itoa(int(sub.ID)), map[string]interface{}{
		"name": "改名的订阅", "url": srv.URL, "intervalHours": 12, "enabled": false,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("修改订阅失败: %d %s", w.Code, w.Body.String())
	}
	var updated struct {
		Name          string `json:"name"`
		IntervalHours int    `json:"intervalHours"`
		Enabled       bool   `json:"enabled"`
	}
	decode(t, w, &updated)
	if updated.Name != "改名的订阅" || updated.IntervalHours != 12 || updated.Enabled {
		t.Fatalf("订阅修改未生效: %+v", updated)
	}

	// 普通用户能看订阅列表，但不能改（订阅会写站点表，属于管理员操作）。
	authCreateUser(t, r, testToken, "viewer", "viewer123456", nil)
	userToken, _ := login(t, r, "viewer", "viewer123456")
	if got := authCall(r, http.MethodGet, "/api/subscriptions/v1", userToken, nil); got.Code != http.StatusOK {
		t.Fatalf("普通用户应能查看订阅列表，实际 %d", got.Code)
	}
	if got := authCall(r, http.MethodPost, "/api/subscriptions/v1", userToken,
		map[string]interface{}{"name": "x", "url": srv.URL}); got.Code != http.StatusForbidden {
		t.Fatalf("普通用户创建订阅应返回 403，实际 %d", got.Code)
	}
	if got := authCall(r, http.MethodPost, refreshPath, userToken, nil); got.Code != http.StatusForbidden {
		t.Fatalf("普通用户刷新订阅应返回 403，实际 %d", got.Code)
	}

	// 删除订阅：站点保留并解绑，不能连已采数据一起消失。
	if got := authCall(r, http.MethodDelete, "/api/subscriptions/v1/"+strconv.Itoa(int(sub.ID)), testToken, nil); got.Code != http.StatusNoContent {
		t.Fatalf("删除订阅失败: %d %s", got.Code, got.Body.String())
	}
	w = authCall(r, http.MethodGet, "/api/sources/v1", testToken, nil)
	srcs = nil
	decode(t, w, &srcs)
	if len(srcs) != 1 || srcs[0].SubscriptionID != 0 {
		t.Fatalf("删除订阅后站点应解绑保留: %+v", srcs)
	}
}

// TestSubscriptionRefreshFailureIsVisible 验证刷新失败时接口把原因带回来，
// 而不是只留一个 500。
func TestSubscriptionRefreshFailureIsVisible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	r, _ := newTestRouter(t)
	w := doJSON(r, http.MethodPost, "/api/subscriptions/v1", map[string]interface{}{
		"name": "坏订阅", "url": srv.URL + "/missing.json",
	})
	var sub struct {
		ID uint `json:"id"`
	}
	decode(t, w, &sub)

	w = authCall(r, http.MethodPost, "/api/subscriptions/v1/"+strconv.Itoa(int(sub.ID))+"/refreshes", testToken, nil)
	if w.Code == http.StatusOK {
		t.Fatalf("订阅地址 404 应视为刷新失败: %s", w.Body.String())
	}
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || payload.Error == "" {
		t.Fatalf("失败响应应包含原因: %s", w.Body.String())
	}
	if !strings.Contains(payload.Error, "404") {
		t.Fatalf("失败原因应指出状态码: %s", payload.Error)
	}
}
