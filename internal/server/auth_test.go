package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
)

func decodeJSON(t *testing.T, body []byte, dst interface{}) {
	t.Helper()
	if err := json.Unmarshal(body, dst); err != nil {
		t.Fatalf("解析响应失败: %v | %s", err, body)
	}
}

// TestFirstLoginBecomesAdmin 验证首用户自动注册、密码校验、令牌与登出。
func TestFirstLoginBecomesAdmin(t *testing.T) {
	r, _ := newTestServer(t)

	// 初始状态：未初始化
	var status struct {
		Initialized bool `json:"initialized"`
		UserCount   int  `json:"userCount"`
	}
	if w := doAuthJSON(r, http.MethodGet, "/api/auth/status", "", nil); w.Code != http.StatusOK {
		t.Fatalf("状态接口异常: %d %s", w.Code, w.Body.String())
	} else {
		decodeJSON(t, w.Body.Bytes(), &status)
	}
	if status.Initialized || status.UserCount != 0 {
		t.Fatalf("初始状态异常: %+v", status)
	}

	// 未登录不能访问业务接口
	if w := doAuthJSON(r, http.MethodGet, "/api/sources", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应返回 401，实际 %d", w.Code)
	}
	if w := doAuthJSON(r, http.MethodGet, "/api/users", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("未登录访问用户管理应返回 401，实际 %d", w.Code)
	}
	// 参数校验
	if w := doAuthJSON(r, http.MethodPost, "/api/auth/login", "",
		map[string]string{"username": "", "password": "x"}); w.Code != http.StatusBadRequest {
		t.Fatalf("空用户名应返回 400，实际 %d", w.Code)
	}

	// 第一个登录者自动成为管理员
	w := doAuthJSON(r, http.MethodPost, "/api/auth/login", "",
		map[string]string{"username": "boss", "password": "pass123"})
	if w.Code != http.StatusOK {
		t.Fatalf("首次登录失败: %d %s", w.Code, w.Body.String())
	}
	var first struct {
		Token     string     `json:"token"`
		FirstUser bool       `json:"firstUser"`
		IsAdmin   bool       `json:"isAdmin"`
		User      model.User `json:"user"`
	}
	decodeJSON(t, w.Body.Bytes(), &first)
	if first.Token == "" || !first.FirstUser || !first.IsAdmin || first.User.Role != model.RoleAdmin {
		t.Fatalf("首个账号未成为管理员: %+v", first)
	}
	if first.User.PasswordHash != "" {
		t.Fatal("响应不应包含密码哈希")
	}

	// /me 与状态
	var me struct {
		IsAdmin bool       `json:"isAdmin"`
		User    model.User `json:"user"`
	}
	w = doAuthJSON(r, http.MethodGet, "/api/auth/me", first.Token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("/me 异常: %d %s", w.Code, w.Body.String())
	}
	decodeJSON(t, w.Body.Bytes(), &me)
	if !me.IsAdmin || me.User.Username != "boss" {
		t.Fatalf("/me 内容异常: %+v", me)
	}
	decodeJSON(t, doAuthJSON(r, http.MethodGet, "/api/auth/status", "", nil).Body.Bytes(), &status)
	if !status.Initialized || status.UserCount != 1 {
		t.Fatalf("初始化后状态异常: %+v", status)
	}

	// 系统已有账号后，陌生用户名不再自动注册；错误密码被拒绝
	if got := doAuthJSON(r, http.MethodPost, "/api/auth/login", "",
		map[string]string{"username": "stranger", "password": "pass123"}); got.Code != http.StatusUnauthorized {
		t.Errorf("陌生账号不应自动注册，实际 %d", got.Code)
	}
	if got := doAuthJSON(r, http.MethodPost, "/api/auth/login", "",
		map[string]string{"username": "boss", "password": "wrong"}); got.Code != http.StatusUnauthorized {
		t.Errorf("错误密码应返回 401，实际 %d", got.Code)
	}

	// 无效令牌
	if got := doAuthJSON(r, http.MethodGet, "/api/sources", "not-a-token", nil); got.Code != http.StatusUnauthorized {
		t.Errorf("无效令牌应返回 401，实际 %d", got.Code)
	}

	// 正常再次登录会创建另一个会话；登出只吊销该会话
	second := loginForToken(t, r, "boss", "pass123")
	if got := doAuthJSON(r, http.MethodPost, "/api/auth/logout", second, nil); got.Code != http.StatusOK {
		t.Fatalf("登出失败: %d %s", got.Code, got.Body.String())
	}
	if got := doAuthJSON(r, http.MethodGet, "/api/sources", second, nil); got.Code != http.StatusUnauthorized {
		t.Errorf("登出后令牌应立即失效，实际 %d", got.Code)
	}
	if got := doAuthJSON(r, http.MethodGet, "/api/sources", first.Token, nil); got.Code != http.StatusOK {
		t.Errorf("登出不应影响其他会话，实际 %d", got.Code)
	}
}

// TestUserManagementAndSourceVisibility 验证用户管理、站点可见性隔离与越权拒绝。
func TestUserManagementAndSourceVisibility(t *testing.T) {
	r, _ := newTestServer(t)
	admin := loginForToken(t, r, "admin", "pass123")
	site := newMockVideoSite(t)

	newSource := func(name string) uint {
		t.Helper()
		w := doAuthJSON(r, http.MethodPost, "/api/sources", admin, map[string]interface{}{
			"name": name, "apiBase": site.URL + "/api.php/provide/vod/",
			"format": "seacms_json", "enabled": true, "incrementalHours": 24,
		})
		if w.Code != http.StatusCreated {
			t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
		}
		var src model.Source
		decodeJSON(t, w.Body.Bytes(), &src)
		return src.ID
	}
	srcA, srcB := newSource("站点A"), newSource("站点B")
	idA, idB := strconv.Itoa(int(srcA)), strconv.Itoa(int(srcB))

	// 管理员创建普通用户，只授权站点 A
	w := doAuthJSON(r, http.MethodPost, "/api/users", admin, map[string]interface{}{
		"username": "viewer", "password": "pass456", "role": model.RoleUser,
		"sourceIds": []uint{srcA},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("创建用户失败: %d %s", w.Code, w.Body.String())
	}
	var viewer userView
	decodeJSON(t, w.Body.Bytes(), &viewer)
	if viewer.ID == 0 || viewer.Role != model.RoleUser || len(viewer.SourceIDs) != 1 ||
		viewer.SourceIDs[0] != srcA {
		t.Fatalf("用户创建结果异常: %+v", viewer)
	}
	if viewer.PasswordHash != "" {
		t.Fatal("用户列表不应泄露密码哈希")
	}
	viewerID := strconv.Itoa(int(viewer.ID))

	// 重名校验
	if got := doAuthJSON(r, http.MethodPost, "/api/users", admin, map[string]interface{}{
		"username": "viewer", "password": "pass456",
	}); got.Code != http.StatusConflict {
		t.Errorf("重名用户应返回 409，实际 %d", got.Code)
	}

	viewerToken := loginForToken(t, r, "viewer", "pass456")

	// 普通用户只看到被授权的站点
	var srcs []sourceView
	decodeJSON(t, doAuthJSON(r, http.MethodGet, "/api/sources", viewerToken, nil).Body.Bytes(), &srcs)
	if len(srcs) != 1 || srcs[0].ID != srcA {
		t.Fatalf("普通用户可见站点异常: %+v", srcs)
	}
	// 管理员可见全部
	decodeJSON(t, doAuthJSON(r, http.MethodGet, "/api/sources", admin, nil).Body.Bytes(), &srcs)
	if len(srcs) != 2 {
		t.Fatalf("管理员应可见全部站点，实际 %d", len(srcs))
	}

	// 普通用户不能管理用户、不能改站点配置
	if got := doAuthJSON(r, http.MethodGet, "/api/users", viewerToken, nil); got.Code != http.StatusForbidden {
		t.Errorf("普通用户访问用户管理应返回 403，实际 %d", got.Code)
	}
	if got := doAuthJSON(r, http.MethodPost, "/api/sources", viewerToken, map[string]interface{}{
		"name": "x", "apiBase": "https://x.com/api.php",
	}); got.Code != http.StatusForbidden {
		t.Errorf("普通用户新增站点应返回 403，实际 %d", got.Code)
	}
	// 普通用户不能采集未授权站点（返回 404，不暴露存在性）
	if got := doAuthJSON(r, http.MethodPost, "/api/sources/"+idB+"/crawl?mode=full", viewerToken, nil); got.Code != http.StatusNotFound {
		t.Errorf("越权采集应返回 404，实际 %d", got.Code)
	}

	// 普通用户可采集授权站点；管理员采集另一个站点，制造两侧数据
	if got := doAuthJSON(r, http.MethodPost, "/api/sources/"+idA+"/crawl?mode=full", viewerToken, nil); got.Code != http.StatusOK {
		t.Fatalf("授权站点采集失败: %d %s", got.Code, got.Body.String())
	}
	if got := doAuthJSON(r, http.MethodPost, "/api/sources/"+idB+"/crawl?mode=full", admin, nil); got.Code != http.StatusOK {
		t.Fatalf("管理员采集失败: %d %s", got.Code, got.Body.String())
	}

	// 影片列表按站点隔离
	var videos struct {
		Total int           `json:"total"`
		Items []model.Video `json:"items"`
	}
	decodeJSON(t, doAuthJSON(r, http.MethodGet, "/api/videos", viewerToken, nil).Body.Bytes(), &videos)
	if videos.Total != 2 {
		t.Fatalf("普通用户影片总数应为 2，实际 %d", videos.Total)
	}
	for _, v := range videos.Items {
		if v.SourceID != srcA {
			t.Fatalf("普通用户看到了未授权站点的影片: sourceId=%d", v.SourceID)
		}
	}
	decodeJSON(t, doAuthJSON(r, http.MethodGet, "/api/videos?source_id="+idB, viewerToken, nil).Body.Bytes(), &videos)
	if videos.Total != 0 {
		t.Errorf("显式指定未授权站点仍应查不到数据，实际 %d", videos.Total)
	}
	decodeJSON(t, doAuthJSON(r, http.MethodGet, "/api/videos", admin, nil).Body.Bytes(), &videos)
	if videos.Total != 4 {
		t.Fatalf("管理员应看到 4 条影片，实际 %d", videos.Total)
	}

	// 单条详情也不允许越权读取
	var adminVideos struct {
		Items []model.Video `json:"items"`
	}
	decodeJSON(t, doAuthJSON(r, http.MethodGet, "/api/videos?source_id="+idB, admin, nil).Body.Bytes(), &adminVideos)
	if len(adminVideos.Items) == 0 {
		t.Fatal("站点 B 缺少影片数据")
	}
	bid := strconv.Itoa(int(adminVideos.Items[0].ID))
	if got := doAuthJSON(r, http.MethodGet, "/api/videos/"+bid, viewerToken, nil); got.Code != http.StatusNotFound {
		t.Errorf("越权读取影片详情应返回 404，实际 %d", got.Code)
	}

	// 采集记录也按站点隔离
	var jobs []model.CrawlJob
	decodeJSON(t, doAuthJSON(r, http.MethodGet, "/api/jobs", viewerToken, nil).Body.Bytes(), &jobs)
	for _, j := range jobs {
		if j.SourceID != srcA {
			t.Fatalf("普通用户看到了未授权站点的采集记录: sourceId=%d", j.SourceID)
		}
	}

	// 批量采集严格限定在可见站点
	var batch struct {
		Results []struct {
			SourceID uint `json:"sourceId"`
		} `json:"results"`
	}
	decodeJSON(t, doAuthJSON(r, http.MethodPost, "/api/crawl?mode=incremental", viewerToken, nil).Body.Bytes(), &batch)
	if len(batch.Results) != 1 || batch.Results[0].SourceID != srcA {
		t.Fatalf("批量采集未限制在可见站点: %+v", batch.Results)
	}

	// 管理员调整授权后立即生效（无需重新登录）
	if got := doAuthJSON(r, http.MethodPut, "/api/users/"+viewerID, admin,
		map[string]interface{}{"sourceIds": []uint{srcB}}); got.Code != http.StatusOK {
		t.Fatalf("更新授权失败: %d %s", got.Code, got.Body.String())
	}
	decodeJSON(t, doAuthJSON(r, http.MethodGet, "/api/sources", viewerToken, nil).Body.Bytes(), &srcs)
	if len(srcs) != 1 || srcs[0].ID != srcB {
		t.Fatalf("授权变更未即时生效: %+v", srcs)
	}
	// 清空授权：什么都看不到
	if got := doAuthJSON(r, http.MethodPut, "/api/users/"+viewerID, admin,
		map[string]interface{}{"sourceIds": []uint{}}); got.Code != http.StatusOK {
		t.Fatalf("清空授权失败: %d %s", got.Code, got.Body.String())
	}
	decodeJSON(t, doAuthJSON(r, http.MethodGet, "/api/sources", viewerToken, nil).Body.Bytes(), &srcs)
	if len(srcs) != 0 {
		t.Errorf("清空授权后不应看到站点，实际 %d", len(srcs))
	}
	decodeJSON(t, doAuthJSON(r, http.MethodGet, "/api/videos", viewerToken, nil).Body.Bytes(), &videos)
	if videos.Total != 0 {
		t.Errorf("清空授权后不应看到影片，实际 %d", videos.Total)
	}

	// 系统保护：不能删自己、不能停用/降级最后一个管理员
	if got := doAuthJSON(r, http.MethodDelete, "/api/users/1", admin, nil); got.Code != http.StatusBadRequest {
		t.Errorf("删除自己应被拒绝，实际 %d", got.Code)
	}
	if got := doAuthJSON(r, http.MethodPut, "/api/users/1", admin,
		map[string]interface{}{"role": model.RoleUser}); got.Code != http.StatusBadRequest {
		t.Errorf("降级唯一管理员应被拒绝，实际 %d", got.Code)
	}
	if got := doAuthJSON(r, http.MethodPut, "/api/users/1", admin,
		map[string]interface{}{"enabled": false}); got.Code != http.StatusBadRequest {
		t.Errorf("停用唯一管理员应被拒绝，实际 %d", got.Code)
	}

	// 禁用用户后其令牌立即失效，且无法再登录
	if got := doAuthJSON(r, http.MethodPut, "/api/users/"+viewerID, admin,
		map[string]interface{}{"enabled": false}); got.Code != http.StatusOK {
		t.Fatalf("禁用用户失败: %d %s", got.Code, got.Body.String())
	}
	if got := doAuthJSON(r, http.MethodGet, "/api/sources", viewerToken, nil); got.Code != http.StatusUnauthorized {
		t.Errorf("禁用后令牌应立即失效，实际 %d", got.Code)
	}
	if got := doAuthJSON(r, http.MethodPost, "/api/auth/login", "",
		map[string]string{"username": "viewer", "password": "pass456"}); got.Code != http.StatusForbidden {
		t.Errorf("禁用账号登录应返回 403，实际 %d", got.Code)
	}

	// 删除用户
	if got := doAuthJSON(r, http.MethodDelete, "/api/users/"+viewerID, admin, nil); got.Code != http.StatusNoContent {
		t.Fatalf("删除用户失败: %d %s", got.Code, got.Body.String())
	}
	var users []userView
	decodeJSON(t, doAuthJSON(r, http.MethodGet, "/api/users", admin, nil).Body.Bytes(), &users)
	if len(users) != 1 {
		t.Fatalf("删除后用户数应为 1，实际 %d", len(users))
	}
}

// TestChangePassword 验证自助改密会吊销该账号的旧会话。
func TestChangePassword(t *testing.T) {
	r, _ := newTestServer(t)
	old := loginForToken(t, r, "admin", "pass123")

	if got := doAuthJSON(r, http.MethodPost, "/api/auth/password", old,
		map[string]string{"oldPassword": "wrong", "newPassword": "newpass"}); got.Code != http.StatusUnauthorized {
		t.Errorf("原密码错误应返回 401，实际 %d", got.Code)
	}
	if got := doAuthJSON(r, http.MethodPost, "/api/auth/password", old,
		map[string]string{"oldPassword": "pass123", "newPassword": "123"}); got.Code != http.StatusBadRequest {
		t.Errorf("过短密码应返回 400，实际 %d", got.Code)
	}
	w := doAuthJSON(r, http.MethodPost, "/api/auth/password", old,
		map[string]string{"oldPassword": "pass123", "newPassword": "newpass"})
	if w.Code != http.StatusOK {
		t.Fatalf("改密失败: %d %s", w.Code, w.Body.String())
	}
	var d struct {
		Token string `json:"token"`
	}
	decodeJSON(t, w.Body.Bytes(), &d)
	if d.Token == "" {
		t.Fatal("改密后未返回新令牌")
	}
	if got := doAuthJSON(r, http.MethodGet, "/api/sources", old, nil); got.Code != http.StatusUnauthorized {
		t.Errorf("改密后旧令牌应失效，实际 %d", got.Code)
	}
	if got := doAuthJSON(r, http.MethodGet, "/api/sources", d.Token, nil); got.Code != http.StatusOK {
		t.Errorf("改密后新令牌应可用，实际 %d", got.Code)
	}
	if got := doAuthJSON(r, http.MethodPost, "/api/auth/login", "",
		map[string]string{"username": "admin", "password": "pass123"}); got.Code != http.StatusUnauthorized {
		t.Errorf("旧密码应无法登录，实际 %d", got.Code)
	}
	_ = loginForToken(t, r, "admin", "newpass")
}
