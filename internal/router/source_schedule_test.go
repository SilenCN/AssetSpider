package router_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
)

// TestSourceScheduleAPI 验证站点定时增量配置可创建 / 更新 / 列出，
// 非法 cron 会被拒绝，列表会给出按 cron 现算的下次触发时刻。
func TestSourceScheduleAPI(t *testing.T) {
	r, _ := newTestRouter(t)
	payload := map[string]interface{}{
		"name": "定时站", "apiBase": "https://example.com/api.php/provide/vod/",
		"enabled": true, "scheduleEnabled": true, "scheduleCron": "0 3 * * *",
	}
	w := doJSON(r, http.MethodPost, "/api/sources/v1", payload)
	if w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}
	var src model.Source
	if err := json.Unmarshal(w.Body.Bytes(), &src); err != nil {
		t.Fatal(err)
	}
	if !src.ScheduleEnabled || src.ScheduleCron != "0 3 * * *" {
		t.Fatalf("定时配置未保存: %+v", src)
	}
	path := "/api/sources/v1/" + strconv.Itoa(int(src.ID))

	// 列表接口带上下次触发时刻（不落库）。
	w = doJSON(r, http.MethodGet, "/api/sources/v1", nil)
	var list []struct {
		model.Source
		NextScheduleAt *string `json:"nextScheduleAt"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &list) != nil {
		t.Fatalf("站点列表异常: %d %s", w.Code, w.Body.String())
	}
	if len(list) != 1 || list[0].NextScheduleAt == nil || *list[0].NextScheduleAt == "" {
		t.Fatalf("应返回下次触发时刻: %s", w.Body.String())
	}

	// 非法 cron 直接拒绝，不落库。
	bad := map[string]interface{}{
		"name": "定时站", "apiBase": "https://example.com/api.php/provide/vod/",
		"enabled": true, "scheduleEnabled": true, "scheduleCron": "0 99 * * *",
	}
	if w := doJSON(r, http.MethodPut, path, bad); w.Code == http.StatusOK {
		t.Fatalf("非法 cron 应被拒绝: %s", w.Body.String())
	}
	// 启用定时但不填表达式也要拒绝。
	bad["scheduleCron"] = ""
	if w := doJSON(r, http.MethodPut, path, bad); w.Code == http.StatusOK {
		t.Fatalf("启用定时却无 cron 应被拒绝: %s", w.Body.String())
	}
	// 关闭定时后表达式可以留空。
	off := map[string]interface{}{
		"name": "定时站", "apiBase": "https://example.com/api.php/provide/vod/",
		"enabled": true, "scheduleEnabled": false, "scheduleCron": "",
	}
	if w := doJSON(r, http.MethodPut, path, off); w.Code != http.StatusOK {
		t.Fatalf("关闭定时失败: %d %s", w.Code, w.Body.String())
	}
}
