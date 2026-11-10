package router_test

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/service"
)

func TestSettingsAPI(t *testing.T) {
	r, _ := newTestRouter(t)

	// 1) 读取：返回采集配置、字段元信息与只读启动项。
	w := doJSON(r, http.MethodGet, "/api/settings/v1", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("读取设置失败: %d %s", w.Code, w.Body.String())
	}
	var view service.SettingsView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Fields) < 8 || view.Collector.MaxConcurrent != config.Default().Collector.MaxConcurrent {
		t.Fatalf("设置视图不完整: %+v", view)
	}
	if view.Static.ServerAddr == "" || view.Static.DatabasePath == "" {
		t.Fatalf("只读启动项缺失: %+v", view.Static)
	}
	foundConcurrency := false
	for _, f := range view.Fields {
		if f.Key == "maxConcurrent" {
			foundConcurrency = true
			if f.Min != 1 || f.Max != 64 {
				t.Fatalf("并发上限字段元信息异常: %+v", f)
			}
		}
	}
	if !foundConcurrency {
		t.Fatal("设置字段里应含 maxConcurrent（并发上限已从任务页迁入设置页）")
	}

	// 2) 在线更新：只提交部分字段（maxConcurrent / timeoutSeconds），其余保持原值。
	newMax := config.Default().Collector.MaxConcurrent + 4
	if newMax > 64 {
		newMax = 64
	}
	w = doJSON(r, http.MethodPut, "/api/settings/v1", map[string]interface{}{
		"maxConcurrent":  newMax,
		"timeoutSeconds": 37,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("更新设置失败: %d %s", w.Code, w.Body.String())
	}
	var updated service.SettingsView
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Collector.MaxConcurrent != newMax || updated.Collector.TimeoutSeconds != 37 {
		t.Fatalf("更新未生效: %+v", updated.Collector)
	}
	if updated.Collector.MaxPages != config.Default().Collector.MaxPages {
		t.Fatalf("未提交的字段被零值覆盖: %+v", updated.Collector)
	}

	// 3) 即时生效：并发容量端点应立即反映新的上限（limiter 已同步）。
	w = doJSON(r, http.MethodGet, "/api/tasks/v1/capacity", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"maxConcurrent":`+strconv.Itoa(newMax)) {
		t.Fatalf("并发上限未即时生效: %d %s", w.Code, w.Body.String())
	}

	// 4) 非法值拒绝：并发上限越界与超时为负。
	for _, payload := range []map[string]interface{}{
		{"maxConcurrent": 0}, {"maxConcurrent": 100}, {"timeoutSeconds": -5},
	} {
		w = doJSON(r, http.MethodPut, "/api/settings/v1", payload)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("非法值 %v 应返回 400，实际 %d %s", payload, w.Code, w.Body.String())
		}
	}
}

func TestSettingsRequireAdmin(t *testing.T) {
	r, _ := newTestRouter(t)
	// 普通用户可读取配置（设置页对他可见但只读）。
	authCreateUser(t, r, testToken, "viewer", "viewer123456", nil)
	viewerToken, _ := login(t, r, "viewer", "viewer123456")
	if got := authCall(r, http.MethodGet, "/api/settings/v1", viewerToken, nil); got.Code != http.StatusOK {
		t.Fatalf("普通用户应能读取设置: %d %s", got.Code, got.Body.String())
	}
	// 但修改必须管理员。
	if got := authCall(r, http.MethodPut, "/api/settings/v1", viewerToken,
		map[string]int{"maxConcurrent": 5}); got.Code != http.StatusForbidden {
		t.Fatalf("普通用户改设置应返回 403，实际 %d", got.Code)
	}
}
