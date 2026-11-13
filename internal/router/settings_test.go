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

	// 2) 在线更新：在 GET 返回的全部采集字段基础上覆盖 maxConcurrent / timeoutSeconds。
	newMax := config.Default().Collector.MaxConcurrent + 4
	if newMax > 64 {
		newMax = 64
	}
	collector := view.Collector
	collector.MaxConcurrent = newMax
	collector.TimeoutSeconds = 37
	w = doJSON(r, http.MethodPut, "/api/settings/v1", map[string]interface{}{
		"collector": collector,
		"database": map[string]interface{}{
			"driver":       "sqlite",
			"path":         config.Default().Database.Path,
			"maxOpenConns": 16,
		},
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

	// 4) 非法值拒绝：并发上限越界、超时为负、MySQL 缺 DSN。
	for _, payload := range []map[string]interface{}{
		{"collector": map[string]interface{}{"maxConcurrent": 0}},
		{"collector": map[string]interface{}{"maxConcurrent": 100}},
		{"collector": map[string]interface{}{"timeoutSeconds": -5}},
		{"collector": map[string]interface{}{"maxConcurrent": 3}, "database": map[string]interface{}{"driver": "mysql", "dsn": ""}},
	} {
		w = doJSON(r, http.MethodPut, "/api/settings/v1", payload)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("非法值 %v 应返回 400，实际 %d %s", payload, w.Code, w.Body.String())
		}
	}
}

func TestSettingsUpdateDatabase(t *testing.T) {
	r, _ := newTestRouter(t)

	// 1) 默认 SQLite。
	w := doJSON(r, http.MethodGet, "/api/settings/v1", nil)
	var before service.SettingsView
	if err := json.Unmarshal(w.Body.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	if before.Database.Driver != "sqlite" {
		t.Fatalf("默认应为 sqlite: %+v", before.Database)
	}
	if before.Database.DSNConfigured {
		t.Fatalf("SQLite 默认不应声称已配置 DSN: %+v", before.Database)
	}

	// 2) 切换到 MySQL 并填连接串。
	dsn := "u:p@tcp(127.0.0.1:3306)/assetspider?charset=utf8mb4&parseTime=True&loc=Local"
	col := config.Default().Collector
	w = doJSON(r, http.MethodPut, "/api/settings/v1", map[string]interface{}{
		"collector": col,
		"database": map[string]interface{}{
			"driver":       "mysql",
			"dsn":          dsn,
			"maxOpenConns": 32,
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("更新数据库配置失败: %d %s", w.Code, w.Body.String())
	}
	var after service.SettingsView
	if err := json.Unmarshal(w.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if after.Database.Driver != "mysql" || !after.Database.DSNConfigured || after.Database.MaxOpenConns != 32 {
		t.Fatalf("数据库配置未生效: %+v", after.Database)
	}

	// 3) 留空 DSN 应沿用现有连接串（不回显明文，也不误清空）。
	w = doJSON(r, http.MethodPut, "/api/settings/v1", map[string]interface{}{
		"collector": col,
		"database":  map[string]interface{}{"driver": "mysql", "dsn": ""},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("留空 DSN 应沿用现有连接串: %d %s", w.Code, w.Body.String())
	}
	var kept service.SettingsView
	if err := json.Unmarshal(w.Body.Bytes(), &kept); err != nil {
		t.Fatal(err)
	}
	if !kept.Database.DSNConfigured {
		t.Fatalf("留空 DSN 后连接串应仍被视为已配置: %+v", kept.Database)
	}
}

func TestSettingsTestDatabaseAPI(t *testing.T) {
	r, dbPath := newTestRouter(t)

	// 1) SQLite 成功：用独立的临时文件作为 path（不触碰运行中的库）。
	w := doJSON(r, http.MethodPost, "/api/settings/v1/database/test", map[string]interface{}{
		"database": map[string]interface{}{"driver": "sqlite", "path": dbPath + ".probe"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("测试连接应返回 200: %d %s", w.Code, w.Body.String())
	}
	var okRes service.DatabaseTestResult
	if err := json.Unmarshal(w.Body.Bytes(), &okRes); err != nil {
		t.Fatal(err)
	}
	if !okRes.OK {
		t.Fatalf("SQLite 应连接成功: %+v", okRes)
	}

	// 2) MySQL 缺 DSN：校验失败，但仍返回 200 且 ok=false（连接成败是预期探测结果）。
	w = doJSON(r, http.MethodPost, "/api/settings/v1/database/test", map[string]interface{}{
		"database": map[string]interface{}{"driver": "mysql"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("测试连接失败也应返回 200: %d %s", w.Code, w.Body.String())
	}
	var failRes service.DatabaseTestResult
	if err := json.Unmarshal(w.Body.Bytes(), &failRes); err != nil {
		t.Fatal(err)
	}
	if failRes.OK {
		t.Fatalf("MySQL 缺 DSN 应失败: %+v", failRes)
	}
	if failRes.Error == "" {
		t.Fatal("失败应带原因")
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

// TestSettingsRestartAPI 重启接口只允许管理员调用：重启会短暂中断全体用户的访问。
// 测试里没有 main 在监听重启通道，因此请求只会被接受并排队，进程不会真的重建。
func TestSettingsRestartAPI(t *testing.T) {
	r, _ := newTestRouter(t)

	authCreateUser(t, r, testToken, "viewer2", "viewer123456", nil)
	viewerToken, _ := login(t, r, "viewer2", "viewer123456")
	if got := authCall(r, http.MethodPost, "/api/settings/v1/restart", viewerToken, nil); got.Code != http.StatusForbidden {
		t.Fatalf("普通用户重启应返回 403，实际 %d %s", got.Code, got.Body.String())
	}

	// 管理员：请求被接受（202），响应先于停服返回，页面才能显示「重启中」。
	w := doJSON(r, http.MethodPost, "/api/settings/v1/restart", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("管理员重启应返回 202，实际 %d %s", w.Code, w.Body.String())
	}
	var res service.RestartResult
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("重启请求应被接受: %+v", res)
	}

	// 同一进程生命周期内重复触发应被拒绝：进程即将被替换，再排一次没有意义。
	w = doJSON(r, http.MethodPost, "/api/settings/v1/restart", nil)
	var again service.RestartResult
	if err := json.Unmarshal(w.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if again.OK || again.Error == "" {
		t.Fatalf("重复重启应被拒绝: %+v", again)
	}
}
