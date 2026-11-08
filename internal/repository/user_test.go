package repository

import (
	"errors"
	"testing"
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
)

func newUser(t *testing.T, r *Repositories, username, role string, enabled bool) *model.User {
	t.Helper()
	u := &model.User{
		Username: username, PasswordHash: "$2a$10$hash", Role: role, Enabled: enabled,
	}
	if err := r.Users.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestUserRepositoryCRUD(t *testing.T) {
	r := newTestRepos(t)

	if n, _ := r.Users.CountUsers(); n != 0 {
		t.Fatalf("初始账号数应为 0，实际 %d", n)
	}
	admin := newUser(t, r, "admin", model.RoleAdmin, true)
	viewer := newUser(t, r, "viewer", model.RoleUser, true)

	if n, _ := r.Users.CountUsers(); n != 2 {
		t.Fatalf("账号数应为 2，实际 %d", n)
	}
	if n, _ := r.Users.CountEnabledAdmins(); n != 1 {
		t.Fatalf("启用管理员数应为 1，实际 %d", n)
	}
	got, err := r.Users.GetUser(admin.ID)
	if err != nil || got.Username != "admin" || !got.IsAdmin() {
		t.Fatalf("GetUser 异常: %+v %v", got, err)
	}
	byName, err := r.Users.GetUserByUsername("viewer")
	if err != nil || byName.ID != viewer.ID {
		t.Fatalf("GetUserByUsername 异常: %+v %v", byName, err)
	}
	if _, err := r.Users.GetUserByUsername("nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的用户应返回 ErrNotFound，实际 %v", err)
	}
	if items, _ := r.Users.ListUsers(); len(items) != 2 || items[0].Username != "admin" {
		t.Fatalf("ListUsers 应按 ID 正序返回 2 条，实际 %+v", items)
	}

	// 停用与降级会改变启用管理员数量
	admin.Enabled = false
	if err := r.Users.SaveUser(admin); err != nil {
		t.Fatal(err)
	}
	if n, _ := r.Users.CountEnabledAdmins(); n != 0 {
		t.Fatalf("停用管理员后应为 0，实际 %d", n)
	}
	admin.Enabled = true
	_ = r.Users.SaveUser(admin)
	if err := r.Users.TouchLogin(viewer.ID); err != nil {
		t.Fatal(err)
	}
	if u, _ := r.Users.GetUser(viewer.ID); u.LastLoginAt == nil {
		t.Fatal("TouchLogin 未写入最近登录时间")
	}

	// 级联删除：会话与站点授权一并清理
	_ = r.Users.CreateSession(&model.Session{
		TokenHash: "hash-of-token", UserID: viewer.ID,
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err := r.Users.DeleteUser(viewer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Users.GetUser(viewer.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后用户应不存在，实际 %v", err)
	}
	if n, _ := r.Users.CountUsers(); n != 1 {
		t.Fatalf("删除后应剩 1 个账号，实际 %d", n)
	}
}

func TestSessionRepository(t *testing.T) {
	r := newTestRepos(t)
	u := newUser(t, r, "admin", model.RoleAdmin, true)

	valid := &model.Session{TokenHash: "valid", UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour)}
	expired := &model.Session{TokenHash: "expired", UserID: u.ID, ExpiresAt: time.Now().Add(-time.Hour)}
	for _, sess := range []*model.Session{valid, expired} {
		if err := r.Users.CreateSession(sess); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := r.Users.GetSessionUser("valid"); err != nil || got.ID != u.ID {
		t.Fatalf("有效会话应能取到用户: %+v %v", got, err)
	}
	if _, err := r.Users.GetSessionUser("expired"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("过期会话应失效，实际 %v", err)
	}
	if _, err := r.Users.GetSessionUser("unknown"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("未知令牌应失效，实际 %v", err)
	}

	// 停用账号后，即使会话未过期也不再可用
	u.Enabled = false
	if err := r.Users.SaveUser(u); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Users.GetSessionUser("valid"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("停用账号的会话应立即失效，实际 %v", err)
	}
	u.Enabled = true
	_ = r.Users.SaveUser(u)

	if err := r.Users.DeleteSession("valid"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Users.GetSessionUser("valid"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("登出后令牌应失效，实际 %v", err)
	}
	if err := r.Users.PurgeExpiredSessions(); err != nil {
		t.Fatalf("清理过期会话失败: %v", err)
	}
	var remaining int64
	if err := r.db.Model(&model.Session{}).Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("过期会话应已清理干净，实际剩余 %d 条", remaining)
	}

	_ = r.Users.CreateSession(&model.Session{
		TokenHash: "again", UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err := r.Users.DeleteUserSessions(u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Users.GetSessionUser("again"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteUserSessions 应吊销全部令牌，实际 %v", err)
	}
}

func TestUserSourceRepository(t *testing.T) {
	r := newTestRepos(t)
	u := newUser(t, r, "viewer", model.RoleUser, true)

	var ids []uint
	for _, name := range []string{"站点A", "站点B"} {
		src := &model.Source{Name: name, APIBase: "https://example.com/api", Enabled: true}
		if err := r.Sources.CreateSource(src); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, src.ID)
	}

	// 重复 ID 去重、不存在的站点忽略
	if err := r.Users.SetUserSources(u.ID, []uint{ids[0], ids[0], ids[1], 99999, 0}); err != nil {
		t.Fatal(err)
	}
	got, err := r.Users.ListSourceIDsByUser(u.ID)
	if err != nil || len(got) != 2 || got[0] != ids[0] || got[1] != ids[1] {
		t.Fatalf("授权列表异常: %v %v", got, err)
	}
	all, err := r.Users.ListSourceIDsByUsers()
	if err != nil || len(all[u.ID]) != 2 {
		t.Fatalf("全量授权映射异常: %+v %v", all, err)
	}
	// 全量替换：空数组清空授权
	if err := r.Users.SetUserSources(u.ID, []uint{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Users.ListSourceIDsByUser(u.ID); len(got) != 0 {
		t.Fatalf("清空授权后应为空，实际 %v", got)
	}
	// 删除站点时同步撤掉授权
	_ = r.Users.SetUserSources(u.ID, ids)
	if err := r.Sources.DeleteSource(ids[0]); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Users.ListSourceIDsByUser(u.ID); len(got) != 1 || got[0] != ids[1] {
		t.Fatalf("删除站点后应清理对应授权，实际 %v", got)
	}
}
