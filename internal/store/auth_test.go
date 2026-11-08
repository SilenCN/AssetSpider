package store

import (
	"errors"
	"testing"
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
	"gorm.io/gorm"
)

func TestUserAndSessionLifecycle(t *testing.T) {
	s := newTestStore(t)

	n, err := s.CountUsers()
	if err != nil || n != 0 {
		t.Fatalf("初始用户数应为 0: %d %v", n, err)
	}
	u := &model.User{
		Username: "admin", PasswordHash: "hashed",
		Role: model.RoleAdmin, Enabled: true,
	}
	if err := s.CreateUser(u); err != nil || u.ID == 0 {
		t.Fatalf("创建用户失败: %v %+v", err, u)
	}
	if n, _ := s.CountEnabledAdmins(); n != 1 {
		t.Fatalf("启用管理员数应为 1，实际 %d", n)
	}
	got, err := s.GetUserByUsername("admin")
	if err != nil || got.ID != u.ID || !got.IsAdmin() {
		t.Fatalf("按用户名查询异常: %v %+v", err, got)
	}
	if _, err := s.GetUserByUsername("missing"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("不存在的用户名应返回 RecordNotFound，实际 %v", err)
	}
	users, err := s.ListUsers()
	if err != nil || len(users) != 1 {
		t.Fatalf("用户列表异常: %v %+v", err, users)
	}

	// 有效会话可解析到账号，且账号被禁用后立即失效。
	sess := &model.Session{
		TokenHash: "hash-1", UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := s.CreateSession(sess); err != nil {
		t.Fatal(err)
	}
	su, err := s.GetSessionUser("hash-1")
	if err != nil || su.ID != u.ID {
		t.Fatalf("会话解析异常: %v %+v", err, su)
	}

	expired := &model.Session{
		TokenHash: "hash-expired", UserID: u.ID, ExpiresAt: time.Now().Add(-time.Hour),
	}
	if err := s.CreateSession(expired); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSessionUser("hash-expired"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("过期令牌不应可用，实际 %v", err)
	}
	if err := s.PurgeExpiredSessions(); err != nil {
		t.Fatal(err)
	}

	if err := s.TouchLogin(u.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetUser(u.ID)
	if got.LastLoginAt == nil {
		t.Error("TouchLogin 未写入最近登录时间")
	}

	u.Enabled = false
	if err := s.SaveUser(u); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSessionUser("hash-1"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("禁用账号后会话应立即失效，实际 %v", err)
	}

	// 删除用户级联清理会话
	if err := s.DeleteUser(u.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountUsers(); n != 0 {
		t.Errorf("删除后用户数应为 0，实际 %d", n)
	}
}

func TestUserSourceGrants(t *testing.T) {
	s := newTestStore(t)
	a := &model.Source{Name: "A", APIBase: "https://a.com/api.php", Format: "seacms_json", Enabled: true}
	b := &model.Source{Name: "B", APIBase: "https://b.com/api.php", Format: "seacms_json", Enabled: true}
	if err := s.CreateSource(a); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSource(b); err != nil {
		t.Fatal(err)
	}
	u := &model.User{Username: "viewer", PasswordHash: "h", Role: model.RoleUser, Enabled: true}
	if err := s.CreateUser(u); err != nil {
		t.Fatal(err)
	}

	// 重复 ID、不存在的站点 ID 都被正确处理
	if err := s.SetUserSources(u.ID, []uint{a.ID, a.ID, b.ID, 9999}); err != nil {
		t.Fatal(err)
	}
	ids, err := s.ListSourceIDsByUser(u.ID)
	if err != nil || len(ids) != 2 || ids[0] != a.ID || ids[1] != b.ID {
		t.Fatalf("授权列表异常: %v %+v", err, ids)
	}
	grants, err := s.ListSourceIDsByUsers()
	if err != nil || len(grants[u.ID]) != 2 {
		t.Fatalf("批量授权列表异常: %v %+v", err, grants)
	}

	// 空列表清空授权（返回非 nil 的空切片语义由调用方保证）
	if err := s.SetUserSources(u.ID, []uint{}); err != nil {
		t.Fatal(err)
	}
	ids, _ = s.ListSourceIDsByUser(u.ID)
	if len(ids) != 0 {
		t.Errorf("清空授权后应为空，实际 %+v", ids)
	}

	// 删除站点会清理关联授权
	_ = s.SetUserSources(u.ID, []uint{a.ID, b.ID})
	if err := s.DeleteSource(a.ID); err != nil {
		t.Fatal(err)
	}
	ids, _ = s.ListSourceIDsByUser(u.ID)
	if len(ids) != 1 || ids[0] != b.ID {
		t.Errorf("删除站点后授权未清理: %+v", ids)
	}

	// 删除用户会清理其授权
	_ = s.CreateSession(&model.Session{TokenHash: "h1", UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour)})
	if err := s.DeleteUser(u.ID); err != nil {
		t.Fatal(err)
	}
	grants, _ = s.ListSourceIDsByUsers()
	if len(grants[u.ID]) != 0 {
		t.Errorf("删除用户后授权未清理: %+v", grants)
	}
	if _, err := s.GetSessionUser("h1"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("删除用户后会话未清理，实际 %v", err)
	}
}
