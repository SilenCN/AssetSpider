package router_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/app"
	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newAuthRouter 构造一个尚未初始化的应用（不预先创建管理员）。
func newAuthRouter(t *testing.T) (*gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "auth.db")
	webDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"),
		[]byte("<div id=\"app\">AssetSpider</div>"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Database.Path = dbPath
	cfg.Web.Dir = webDir
	application, err := app.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { application.Close() })
	return application.Engine, dbPath
}

// authCall 发起一次可指定令牌的请求；token 为空即匿名请求。
func authCall(r *gin.Engine, method, path, token string, body interface{}) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		reader = strings.NewReader(string(b))
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder, dst interface{}) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), dst); err != nil {
		t.Fatalf("解析响应失败: %v | %s", err, w.Body.String())
	}
}

func login(t *testing.T, r *gin.Engine, username, password string) (string, map[string]interface{}) {
	t.Helper()
	w := authCall(r, http.MethodPost, "/api/auth/v1/sessions", "",
		map[string]string{"username": username, "password": password})
	if w.Code != http.StatusOK {
		t.Fatalf("登录 %s 失败: %d %s", username, w.Code, w.Body.String())
	}
	var out map[string]interface{}
	decode(t, w, &out)
	token, _ := out["token"].(string)
	if token == "" {
		t.Fatalf("登录响应没有 token: %s", w.Body.String())
	}
	return token, out
}

func authCreateSource(t *testing.T, r *gin.Engine, adminToken, name, apiBase string) uint {
	t.Helper()
	created := authCall(r, http.MethodPost, "/api/sources/v1", adminToken, map[string]interface{}{
		"name": name, "apiBase": apiBase, "enabled": true,
		"incrementalHours": 24, "maxPages": 10,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("创建站点 %s 失败: %d %s", name, created.Code, created.Body.String())
	}
	var src struct {
		ID uint `json:"id"`
	}
	decode(t, created, &src)
	return src.ID
}

func authCreateUser(t *testing.T, r *gin.Engine, adminToken, username, password string, sourceIDs []uint) uint {
	t.Helper()
	payload := map[string]interface{}{
		"username": username, "password": password, "role": "user",
		"enabled": true, "sourceIds": sourceIDs,
	}
	w := authCall(r, http.MethodPost, "/api/users/v1", adminToken, payload)
	if w.Code != http.StatusCreated {
		t.Fatalf("创建用户 %s 失败: %d %s", username, w.Code, w.Body.String())
	}
	var u struct {
		ID uint `json:"id"`
	}
	decode(t, w, &u)
	return u.ID
}

// TestAuthFirstUserBecomesAdmin 验证首个登录者自动成为管理员、
// 未登录请求被拦截、密码以 bcrypt 存储、会话只保存令牌摘要。
func TestAuthFirstUserBecomesAdmin(t *testing.T) {
	r, dbPath := newAuthRouter(t)

	w := authCall(r, http.MethodGet, "/api/auth/v1/status", "", nil)
	var st struct {
		Initialized bool  `json:"initialized"`
		UserCount   int64 `json:"userCount"`
	}
	decode(t, w, &st)
	if w.Code != http.StatusOK || st.Initialized || st.UserCount != 0 {
		t.Fatalf("初始状态异常: %d %s", w.Code, w.Body.String())
	}

	// 所有数据接口都要求登录
	if got := authCall(r, http.MethodGet, "/api/sources/v1", "", nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("匿名访问 sources 应返回 401，实际 %d %s", got.Code, got.Body.String())
	}
	if got := authCall(r, http.MethodGet, "/api/videos/v1", "", nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("匿名访问 videos 应返回 401，实际 %d", got.Code)
	}
	// 健康检查与登录页状态公开
	if got := authCall(r, http.MethodGet, "/api/system/v1/health", "", nil); got.Code != http.StatusOK {
		t.Fatalf("健康检查应公开，实际 %d", got.Code)
	}

	token, out := login(t, r, "root", "root123456")
	if out["firstUser"] != true || out["isAdmin"] != true {
		t.Fatalf("首个登录者应自动成为管理员: %+v", out)
	}
	user, _ := out["user"].(map[string]interface{})
	if user["role"] != "admin" || user["username"] != "root" {
		t.Fatalf("登录返回的用户信息异常: %+v", user)
	}
	if _, leaked := user["passwordHash"]; leaked {
		t.Fatal("登录响应不应泄露 passwordHash")
	}
	if len(token) != 64 {
		t.Fatalf("令牌应为 32 字节随机数的十六进制（64 字符），实际 %d", len(token))
	}

	me := authCall(r, http.MethodGet, "/api/auth/v1/me", token, nil)
	var meView struct {
		IsAdmin bool `json:"isAdmin"`
		User    struct {
			Username string `json:"username"`
		} `json:"user"`
	}
	decode(t, me, &meView)
	if me.Code != http.StatusOK || !meView.IsAdmin || meView.User.Username != "root" {
		t.Fatalf("/me 异常: %d %s", me.Code, me.Body.String())
	}

	if got := authCall(r, http.MethodPost, "/api/auth/v1/sessions", "",
		map[string]string{"username": "root", "password": "wrong"}); got.Code != http.StatusUnauthorized {
		t.Fatalf("错误密码应返回 401，实际 %d %s", got.Code, got.Body.String())
	}
	if got := authCall(r, http.MethodGet, "/api/sources/v1", "invalid-token", nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("无效令牌应返回 401，实际 %d", got.Code)
	}
	// 第二个账号不会再自动注册（只能由管理员创建）
	if got := authCall(r, http.MethodPost, "/api/auth/v1/sessions", "",
		map[string]string{"username": "someone", "password": "pass123456"}); got.Code != http.StatusUnauthorized {
		t.Fatalf("系统初始化后不应自动注册新账号，实际 %d", got.Code)
	}

	// 直接查库：密码为 bcrypt 哈希，会话只存 SHA-256 摘要
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := db.Raw("SELECT password_hash FROM users WHERE username = ?", "root").Row().Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$2a$10$") && !strings.HasPrefix(hash, "$2b$10$") {
		t.Fatalf("密码应为 bcrypt 哈希，实际 %q", hash)
	}
	var stored string
	if err := db.Raw("SELECT token_hash FROM sessions LIMIT 1").Row().Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 64 || stored == token {
		t.Fatalf("会话表应只存令牌的 SHA-256 摘要，实际 %q", stored)
	}
	var tables int64
	if err := db.Raw("SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('users','sessions','user_sources')").Row().Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 3 {
		t.Fatalf("认证相关表应自动迁移，实际 %d 张", tables)
	}
}

// TestAuthSourceVisibility 验证管理员分配站点后，普通用户只能看见被授权的站点
// 及其影片/采集记录/分类，越权操作被拦截，且授权变更即时生效。
func TestAuthSourceVisibility(t *testing.T) {
	r, _ := newAuthRouter(t)
	admin, _ := login(t, r, "admin", "admin123456")
	mock := newMockVideoSite(t)

	srcA := authCreateSource(t, r, admin, "站点A", mock.URL+"/")
	srcB := authCreateSource(t, r, admin, "站点B", mock.URL+"/")
	if srcA == 0 || srcB == 0 || srcA == srcB {
		t.Fatal("站点创建失败")
	}

	// 两个站点各采集一次（同步接口），让库里存在影片、分类与采集记录
	for _, id := range []uint{srcA, srcB} {
		w := authCall(r, http.MethodPost,
			"/api/sources/v1/"+strconv.Itoa(int(id))+"/crawls?mode=full", admin, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("站点 %d 采集失败: %d %s", id, w.Code, w.Body.String())
		}
	}

	userA := authCreateUser(t, r, admin, "userA", "pass123456", []uint{srcA})
	userB := authCreateUser(t, r, admin, "userB", "pass123456", []uint{srcB})
	userC := authCreateUser(t, r, admin, "userC", "pass123456", []uint{})
	tokenA, _ := login(t, r, "userA", "pass123456")
	tokenB, _ := login(t, r, "userB", "pass123456")
	tokenC, _ := login(t, r, "userC", "pass123456")

	sourceIDs := func(token string) []uint {
		w := authCall(r, http.MethodGet, "/api/sources/v1", token, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("读取站点失败: %d %s", w.Code, w.Body.String())
		}
		var items []struct {
			ID uint `json:"id"`
		}
		decode(t, w, &items)
		ids := []uint{}
		for _, it := range items {
			ids = append(ids, it.ID)
		}
		return ids
	}
	videoTotal := func(token string) int64 {
		w := authCall(r, http.MethodGet, "/api/videos/v1", token, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("读取影片失败: %d %s", w.Code, w.Body.String())
		}
		var out struct {
			Total int64 `json:"total"`
		}
		decode(t, w, &out)
		return out.Total
	}

	if ids := sourceIDs(admin); len(ids) != 2 {
		t.Fatalf("管理员应看到 2 个站点，实际 %v", ids)
	}
	if ids := sourceIDs(tokenA); len(ids) != 1 || ids[0] != srcA {
		t.Fatalf("userA 只应看到站点A，实际 %v", ids)
	}
	if ids := sourceIDs(tokenB); len(ids) != 1 || ids[0] != srcB {
		t.Fatalf("userB 只应看到站点B，实际 %v", ids)
	}
	if ids := sourceIDs(tokenC); len(ids) != 0 {
		t.Fatalf("userC 未授权任何站点，实际 %v", ids)
	}

	if got := videoTotal(admin); got != 4 {
		t.Fatalf("管理员应看到 4 条影片（每站 2 条），实际 %d", got)
	}
	if got := videoTotal(tokenA); got != 2 {
		t.Fatalf("userA 应只看到站点A 的 2 条影片，实际 %d", got)
	}
	if got := videoTotal(tokenB); got != 2 {
		t.Fatalf("userB 应只看到站点B 的 2 条影片，实际 %d", got)
	}
	if got := videoTotal(tokenC); got != 0 {
		t.Fatalf("userC 不应看到任何影片，实际 %d", got)
	}
	// 显式指定不可见站点也必须返回空，而不是绕过过滤
	w := authCall(r, http.MethodGet, "/api/videos/v1?source_id="+strconv.Itoa(int(srcB)), tokenA, nil)
	var forced struct {
		Total int64 `json:"total"`
	}
	decode(t, w, &forced)
	if forced.Total != 0 {
		t.Fatalf("userA 指定不可见站点应返回 0 条，实际 %d", forced.Total)
	}

	// 采集记录与分类同样按站点隔离
	jobs := authCall(r, http.MethodGet, "/api/jobs/v1?limit=50", tokenA, nil)
	var jobItems []struct {
		SourceID uint `json:"sourceId"`
	}
	decode(t, jobs, &jobItems)
	if len(jobItems) == 0 {
		t.Fatal("userA 应看到站点A 的采集记录")
	}
	for _, j := range jobItems {
		if j.SourceID != srcA {
			t.Fatalf("userA 看到了其他站点的采集记录: %d", j.SourceID)
		}
	}
	cats := authCall(r, http.MethodGet, "/api/categories/v1", tokenA, nil)
	var catItems []struct {
		SourceID uint `json:"sourceId"`
	}
	decode(t, cats, &catItems)
	if len(catItems) == 0 {
		t.Fatal("userA 应看到站点A 的分类")
	}
	for _, c := range catItems {
		if c.SourceID != srcA {
			t.Fatalf("userA 看到了其他站点的分类: %d", c.SourceID)
		}
	}

	// 越权：写操作、用户管理、不可见站点的采集
	if got := authCall(r, http.MethodPost, "/api/sources/v1", tokenA,
		map[string]interface{}{"name": "x", "apiBase": mock.URL + "/"}); got.Code != http.StatusForbidden {
		t.Fatalf("普通用户创建站点应返回 403，实际 %d", got.Code)
	}
	if got := authCall(r, http.MethodGet, "/api/users/v1", tokenA, nil); got.Code != http.StatusForbidden {
		t.Fatalf("普通用户访问用户管理应返回 403，实际 %d", got.Code)
	}
	if got := authCall(r, http.MethodPut, "/api/tasks/v1/capacity", tokenA,
		map[string]int{"maxConcurrent": 2}); got.Code != http.StatusForbidden {
		t.Fatalf("普通用户调整并发上限应返回 403，实际 %d", got.Code)
	}
	if got := authCall(r, http.MethodPost,
		"/api/sources/v1/"+strconv.Itoa(int(srcB))+"/crawls?mode=full", tokenA, nil); got.Code != http.StatusNotFound {
		t.Fatalf("普通用户采集不可见站点应返回 404，实际 %d %s", got.Code, got.Body.String())
	}
	if got := authCall(r, http.MethodPost, "/api/tasks/v1", tokenA,
		map[string]interface{}{"sourceId": srcB, "mode": "incremental"}); got.Code != http.StatusNotFound {
		t.Fatalf("普通用户异步采集不可见站点应返回 404，实际 %d", got.Code)
	}
	// 批量采集只覆盖可见站点
	all := authCall(r, http.MethodPost, "/api/crawls/v1?mode=incremental", tokenA, nil)
	if all.Code != http.StatusOK {
		t.Fatalf("批量采集失败: %d %s", all.Code, all.Body.String())
	}
	var allBody struct {
		Results []struct {
			SourceID uint `json:"sourceId"`
		} `json:"results"`
	}
	decode(t, all, &allBody)
	if len(allBody.Results) != 1 || allBody.Results[0].SourceID != srcA {
		t.Fatalf("userA 批量采集应只覆盖站点A，实际 %+v", allBody.Results)
	}

	// 授权调整后立即生效，无需重新登录
	upd := authCall(r, http.MethodPut, "/api/users/v1/"+strconv.Itoa(int(userA)), admin,
		map[string]interface{}{"sourceIds": []uint{srcA, srcB}})
	if upd.Code != http.StatusOK {
		t.Fatalf("调整授权失败: %d %s", upd.Code, upd.Body.String())
	}
	if ids := sourceIDs(tokenA); len(ids) != 2 {
		t.Fatalf("授权调整后 userA 应立即看到 2 个站点，实际 %v", ids)
	}
	if got := videoTotal(tokenA); got != 4 {
		t.Fatalf("授权调整后 userA 应看到 4 条影片，实际 %d", got)
	}

	// 停用 / 删除账号会使已有会话立即失效
	if got := authCall(r, http.MethodPut, "/api/users/v1/"+strconv.Itoa(int(userB)), admin,
		map[string]interface{}{"enabled": false}); got.Code != http.StatusOK {
		t.Fatalf("停用用户失败: %d %s", got.Code, got.Body.String())
	}
	if got := authCall(r, http.MethodGet, "/api/auth/v1/me", tokenB, nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("停用后旧令牌应失效，实际 %d", got.Code)
	}
	if got := authCall(r, http.MethodDelete, "/api/users/v1/"+strconv.Itoa(int(userC)), admin, nil); got.Code != http.StatusNoContent {
		t.Fatalf("删除用户失败: %d %s", got.Code, got.Body.String())
	}
	if got := authCall(r, http.MethodGet, "/api/auth/v1/me", tokenC, nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("删除后旧令牌应失效，实际 %d", got.Code)
	}

	// 管理员自我保护：不能删除/停用自己，系统必须保留至少一名管理员
	if got := authCall(r, http.MethodDelete, "/api/users/v1/1", admin, nil); got.Code != http.StatusBadRequest {
		t.Fatalf("不能删除自己，实际 %d", got.Code)
	}
	if got := authCall(r, http.MethodPut, "/api/users/v1/1", admin,
		map[string]interface{}{"enabled": false}); got.Code != http.StatusBadRequest {
		t.Fatalf("不能停用自己，实际 %d", got.Code)
	}
	if got := authCall(r, http.MethodPut, "/api/users/v1/1", admin,
		map[string]interface{}{"role": "user"}); got.Code != http.StatusBadRequest {
		t.Fatalf("不能降低自己的角色，实际 %d", got.Code)
	}

	// 用户自行修改密码：旧令牌失效，新令牌可用
	pwd := authCall(r, http.MethodPut, "/api/auth/v1/password", tokenA,
		map[string]string{"oldPassword": "pass123456", "newPassword": "newpass123"})
	if pwd.Code != http.StatusOK {
		t.Fatalf("修改密码失败: %d %s", pwd.Code, pwd.Body.String())
	}
	var pwdOut struct {
		Token string `json:"token"`
	}
	decode(t, pwd, &pwdOut)
	if pwdOut.Token == "" || pwdOut.Token == tokenA {
		t.Fatal("修改密码应签发新令牌")
	}
	if got := authCall(r, http.MethodGet, "/api/auth/v1/me", tokenA, nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("改密后旧令牌应失效，实际 %d", got.Code)
	}
	if got := authCall(r, http.MethodGet, "/api/auth/v1/me", pwdOut.Token, nil); got.Code != http.StatusOK {
		t.Fatalf("改密后新令牌应可用，实际 %d", got.Code)
	}

	// 登出吊销令牌
	if got := authCall(r, http.MethodDelete, "/api/auth/v1/sessions", pwdOut.Token, nil); got.Code != http.StatusOK {
		t.Fatalf("登出失败: %d %s", got.Code, got.Body.String())
	}
	if got := authCall(r, http.MethodGet, "/api/auth/v1/me", pwdOut.Token, nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("登出后令牌应失效，实际 %d", got.Code)
	}
}
