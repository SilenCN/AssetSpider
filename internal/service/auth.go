package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
	"golang.org/x/crypto/bcrypt"
)

// Principal 是一次请求的登录主体，也是 controller 传给各业务服务的可见性凭据。
// 放在 service 层是为了让 controller 不必直接依赖 model 包（四层依赖方向要求）。
type Principal struct {
	UserID    uint   `json:"userId"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	IsAdmin   bool   `json:"isAdmin"`
	TokenHash string `json:"-"`
	// SourceIDs 仅对普通用户有意义：管理员为 nil（可见全部站点），
	// 普通用户为非 nil 切片，空切片表示未授权任何站点。
	SourceIDs []uint `json:"sourceIds,omitempty"`
}

// AdminPrincipal 返回不受站点限制的主体，供服务内部调用与单元测试使用。
func AdminPrincipal() Principal { return Principal{Role: model.RoleAdmin, IsAdmin: true} }

// Visible 返回仓储层使用的站点白名单。
func (p Principal) Visible() []uint {
	if p.IsAdmin {
		return nil
	}
	if p.SourceIDs == nil {
		return []uint{}
	}
	return p.SourceIDs
}

// CanSee 判断主体能否访问某站点（管理员恒为 true）。
func (p Principal) CanSee(sourceID uint) bool {
	if p.IsAdmin {
		return true
	}
	for _, id := range p.SourceIDs {
		if id == sourceID {
			return true
		}
	}
	return false
}

// principalOf 从可选参数中取出主体；服务内部调用不传时按管理员处理，
// 这样仓储/服务层的既有单元测试无需感知认证。HTTP 路径由 controller 显式传入。
func principalOf(p ...Principal) Principal {
	if len(p) == 0 {
		return AdminPrincipal()
	}
	return p[0]
}

// ---------- DTO ----------

type StatusView struct {
	Initialized bool  `json:"initialized"`
	UserCount   int64 `json:"userCount"`
}

type LoginResult struct {
	Token     string     `json:"token"`
	ExpiresAt time.Time  `json:"expiresAt"`
	FirstUser bool       `json:"firstUser"`
	IsAdmin   bool       `json:"isAdmin"`
	User      model.User `json:"user"`
}

type MeView struct {
	User             model.User `json:"user"`
	IsAdmin          bool       `json:"isAdmin"`
	VisibleSourceIDs []uint     `json:"visibleSourceIds"`
}

// UserInput 是用户管理接口允许提交的字段。
// SourceIDs 用指针以区分「未提交（不改授权）」与「提交空数组（清空授权）」。
type UserInput struct {
	Username  string  `json:"username"`
	Password  string  `json:"password"`
	Role      string  `json:"role"`
	Enabled   *bool   `json:"enabled"`
	SourceIDs *[]uint `json:"sourceIds"`
}

// UserView 在账号信息上附加其可见站点 ID 列表。
// 嵌入的 User.PasswordHash 带 json:"-"，不会泄露给前端。
type UserView struct {
	model.User
	SourceIDs []uint `json:"sourceIds"`
}

// AuthService 承载登录、会话、账号与站点授权的全部业务规则。
// 它只依赖 UserRepository，不感知 HTTP 与 Gin。
type AuthService struct {
	users repository.UserRepository
	cfg   config.AuthConfig
}

func NewAuthService(users repository.UserRepository, cfg config.AuthConfig) *AuthService {
	return &AuthService{users: users, cfg: cfg}
}

// tokenHash 把登录令牌转成摘要，数据库只保存摘要。
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// issueToken 为账号签发一个新的会话令牌。
func (s *AuthService) issueToken(u *model.User) (string, time.Time, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", time.Time{}, err
	}
	token := hex.EncodeToString(buf)
	expires := time.Now().Add(time.Duration(s.cfg.TokenTTLHours) * time.Hour)
	sess := &model.Session{
		TokenHash: tokenHash(token),
		UserID:    u.ID,
		ExpiresAt: expires,
	}
	if err := s.users.CreateSession(sess); err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

func (s *AuthService) principalOfUser(u *model.User, hash string) (Principal, error) {
	p := Principal{
		UserID: u.ID, Username: u.Username, Role: u.Role,
		IsAdmin: u.IsAdmin(), TokenHash: hash,
	}
	if !u.IsAdmin() {
		ids, err := s.users.ListSourceIDsByUser(u.ID)
		if err != nil {
			return Principal{}, err
		}
		if ids == nil {
			ids = []uint{}
		}
		p.SourceIDs = ids
	}
	return p, nil
}

// Status 是公开接口，供登录页判断系统是否已有账号。
func (s *AuthService) Status() (StatusView, error) {
	n, err := s.users.CountUsers()
	if err != nil {
		return StatusView{}, err
	}
	return StatusView{Initialized: n > 0, UserCount: n}, nil
}

// Login 处理登录。系统还没有任何账号时，第一个登录者会带着输入的账号密码
// 自动注册为管理员；此后账号只能由管理员创建。
func (s *AuthService) Login(username, password string) (LoginResult, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return LoginResult{}, invalid("用户名和密码不能为空")
	}
	if len(username) > 64 {
		return LoginResult{}, invalid("用户名过长（最多 64 字符）")
	}

	count, err := s.users.CountUsers()
	if err != nil {
		return LoginResult{}, err
	}

	var u *model.User
	firstUser := false
	if count == 0 {
		hash, hashErr := bcrypt.GenerateFromPassword([]byte(password), s.cfg.BCryptCost)
		if hashErr != nil {
			return LoginResult{}, fmt.Errorf("密码加密失败: %w", hashErr)
		}
		candidate := &model.User{
			Username: username, PasswordHash: string(hash),
			Role: model.RoleAdmin, Enabled: true,
		}
		if err := s.users.CreateUser(candidate); err == nil {
			u, firstUser = candidate, true
		}
		// 创建失败（典型情形是并发下已有首个账号抢先注册）时回退到普通登录校验，
		// 由用户名唯一索引保证系统里不会出现第二个「首用户」。
	}

	if u == nil {
		u, err = s.users.GetUserByUsername(username)
		if err != nil {
			if errors.Is(err, repository.ErrNotFound) {
				return LoginResult{}, fmt.Errorf("%w: 用户名或密码错误", ErrUnauthorized)
			}
			return LoginResult{}, err
		}
		if !u.Enabled {
			return LoginResult{}, fmt.Errorf("%w: 账号已停用，请联系管理员", ErrForbidden)
		}
		if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
			return LoginResult{}, fmt.Errorf("%w: 用户名或密码错误", ErrUnauthorized)
		}
	}

	if err := s.users.TouchLogin(u.ID); err != nil {
		return LoginResult{}, err
	}
	_ = s.users.PurgeExpiredSessions()
	token, expires, err := s.issueToken(u)
	if err != nil {
		return LoginResult{}, fmt.Errorf("创建会话失败: %w", err)
	}
	return LoginResult{
		Token: token, ExpiresAt: expires, FirstUser: firstUser,
		IsAdmin: u.IsAdmin(), User: *u,
	}, nil
}

// Resolve 校验 Bearer 令牌并构造请求主体。每个请求都重新读取授权关系，
// 因此管理员调整站点权限后无需用户重新登录即可生效。
func (s *AuthService) Resolve(token string) (Principal, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Principal{}, fmt.Errorf("%w: 请先登录", ErrUnauthorized)
	}
	hash := tokenHash(token)
	u, err := s.users.GetSessionUser(hash)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return Principal{}, fmt.Errorf("%w: 登录已失效，请重新登录", ErrUnauthorized)
		}
		return Principal{}, err
	}
	return s.principalOfUser(u, hash)
}

func (s *AuthService) Logout(hash string) error {
	if hash == "" {
		return nil
	}
	return s.users.DeleteSession(hash)
}

func (s *AuthService) Me(p Principal) (MeView, error) {
	u, err := s.users.GetUser(p.UserID)
	if err != nil {
		return MeView{}, err
	}
	ids := []uint{}
	if !p.IsAdmin {
		ids = append(ids, p.SourceIDs...)
	}
	return MeView{User: *u, IsAdmin: p.IsAdmin, VisibleSourceIDs: ids}, nil
}

// ChangePassword 允许登录用户修改自己的密码；修改后该账号的其他会话全部失效，
// 并签发一个新令牌供当前会话继续使用。
func (s *AuthService) ChangePassword(p Principal, oldPassword, newPassword string) (LoginResult, error) {
	if len(newPassword) < 6 {
		return LoginResult{}, invalid("新密码至少 6 位")
	}
	u, err := s.users.GetUser(p.UserID)
	if err != nil {
		return LoginResult{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(oldPassword)) != nil {
		return LoginResult{}, fmt.Errorf("%w: 原密码错误", ErrUnauthorized)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), s.cfg.BCryptCost)
	if err != nil {
		return LoginResult{}, fmt.Errorf("密码加密失败: %w", err)
	}
	u.PasswordHash = string(hash)
	if err := s.users.SaveUser(u); err != nil {
		return LoginResult{}, err
	}
	_ = s.users.DeleteUserSessions(u.ID)
	token, expires, err := s.issueToken(u)
	if err != nil {
		return LoginResult{}, fmt.Errorf("创建会话失败: %w", err)
	}
	return LoginResult{
		Token: token, ExpiresAt: expires, FirstUser: false,
		IsAdmin: u.IsAdmin(), User: *u,
	}, nil
}

// ---------- 用户管理（管理员） ----------

func (s *AuthService) requireAdmin(p Principal) error {
	if !p.IsAdmin {
		return fmt.Errorf("%w: 仅管理员可执行此操作", ErrForbidden)
	}
	return nil
}

func (s *AuthService) grants() (map[uint][]uint, error) {
	return s.users.ListSourceIDsByUsers()
}

func userViewOf(u model.User, grants map[uint][]uint) UserView {
	ids := grants[u.ID]
	if ids == nil {
		ids = []uint{}
	}
	return UserView{User: u, SourceIDs: ids}
}

func validRole(role string) bool {
	return role == model.RoleAdmin || role == model.RoleUser
}

func (s *AuthService) ListUsers(p ...Principal) ([]UserView, error) {
	if err := s.requireAdmin(principalOf(p...)); err != nil {
		return nil, err
	}
	users, err := s.users.ListUsers()
	if err != nil {
		return nil, err
	}
	grants, err := s.grants()
	if err != nil {
		return nil, err
	}
	out := make([]UserView, 0, len(users))
	for _, u := range users {
		out = append(out, userViewOf(u, grants))
	}
	return out, nil
}

func (s *AuthService) CreateUser(in UserInput, p ...Principal) (UserView, error) {
	if err := s.requireAdmin(principalOf(p...)); err != nil {
		return UserView{}, err
	}
	username := strings.TrimSpace(in.Username)
	if username == "" || len(username) > 64 {
		return UserView{}, invalid("用户名不能为空且最多 64 字符")
	}
	if len(in.Password) < 6 {
		return UserView{}, invalid("密码至少 6 位")
	}
	role := in.Role
	if role == "" {
		role = model.RoleUser
	}
	if !validRole(role) {
		return UserView{}, invalid("角色仅支持 admin / user")
	}
	if _, err := s.users.GetUserByUsername(username); err == nil {
		return UserView{}, conflict("用户名已存在")
	} else if !errors.Is(err, repository.ErrNotFound) {
		return UserView{}, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), s.cfg.BCryptCost)
	if err != nil {
		return UserView{}, fmt.Errorf("密码加密失败: %w", err)
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	u := &model.User{
		Username: username, PasswordHash: string(hash),
		Role: role, Enabled: enabled,
	}
	if err := s.users.CreateUser(u); err != nil {
		return UserView{}, err
	}
	if in.SourceIDs != nil {
		if err := s.users.SetUserSources(u.ID, *in.SourceIDs); err != nil {
			return UserView{}, err
		}
	}
	grants, err := s.grants()
	if err != nil {
		return UserView{}, err
	}
	return userViewOf(*u, grants), nil
}

func (s *AuthService) UpdateUser(id uint, in UserInput, p ...Principal) (UserView, error) {
	actor := principalOf(p...)
	if err := s.requireAdmin(actor); err != nil {
		return UserView{}, err
	}
	u, err := s.users.GetUser(id)
	if err != nil {
		return UserView{}, wrapRepo(err)
	}
	isSelf := actor.UserID == id

	// 自我保护：不能把自己停用或降级，避免管理员把自己锁在系统外。
	if isSelf {
		if in.Enabled != nil && !*in.Enabled {
			return UserView{}, invalid("不能停用自己的账号")
		}
		if in.Role != "" && in.Role != model.RoleAdmin {
			return UserView{}, invalid("不能降低自己的角色")
		}
	}

	// 系统保护：始终保留至少一名启用状态的管理员。
	losingAdmin := false
	if in.Enabled != nil && !*in.Enabled && u.Role == model.RoleAdmin && u.Enabled {
		losingAdmin = true
	}
	if in.Role != "" && in.Role != model.RoleAdmin && u.Role == model.RoleAdmin && u.Enabled {
		losingAdmin = true
	}
	if losingAdmin {
		n, err := s.users.CountEnabledAdmins()
		if err != nil {
			return UserView{}, err
		}
		if n <= 1 {
			return UserView{}, invalid("系统必须保留至少一名启用状态的管理员")
		}
	}

	revokeSessions := false
	if in.Password != "" {
		if len(in.Password) < 6 {
			return UserView{}, invalid("密码至少 6 位")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), s.cfg.BCryptCost)
		if err != nil {
			return UserView{}, fmt.Errorf("密码加密失败: %w", err)
		}
		u.PasswordHash = string(hash)
		revokeSessions = true
	}
	if in.Role != "" {
		if !validRole(in.Role) {
			return UserView{}, invalid("角色仅支持 admin / user")
		}
		if in.Role != u.Role {
			revokeSessions = true
		}
		u.Role = in.Role
	}
	if in.Enabled != nil {
		if !*in.Enabled {
			revokeSessions = true
		}
		u.Enabled = *in.Enabled
	}
	if err := s.users.SaveUser(u); err != nil {
		return UserView{}, err
	}
	if in.SourceIDs != nil {
		if err := s.users.SetUserSources(id, *in.SourceIDs); err != nil {
			return UserView{}, err
		}
	}
	if revokeSessions {
		_ = s.users.DeleteUserSessions(id)
	}
	grants, err := s.grants()
	if err != nil {
		return UserView{}, err
	}
	return userViewOf(*u, grants), nil
}

func (s *AuthService) DeleteUser(id uint, p ...Principal) error {
	actor := principalOf(p...)
	if err := s.requireAdmin(actor); err != nil {
		return err
	}
	u, err := s.users.GetUser(id)
	if err != nil {
		return wrapRepo(err)
	}
	if actor.UserID == id {
		return invalid("不能删除自己的账号")
	}
	if u.Role == model.RoleAdmin && u.Enabled {
		n, err := s.users.CountEnabledAdmins()
		if err != nil {
			return err
		}
		if n <= 1 {
			return invalid("系统必须保留至少一名启用状态的管理员")
		}
	}
	return s.users.DeleteUser(id)
}
