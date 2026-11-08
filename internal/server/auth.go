package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// gin.Context 中存放当前登录态的键。
type ctxKey string

const (
	ctxKeyUser    ctxKey = "authUser"
	ctxKeyVisible ctxKey = "visibleSources"
	ctxKeyToken   ctxKey = "tokenHash"
)

// tokenHash 把登录令牌转成摘要，数据库只保存摘要。
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func bearerToken(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// currentUser 返回中间件解析出的当前账号。
func currentUser(c *gin.Context) *model.User {
	if v, ok := c.Get(string(ctxKeyUser)); ok {
		if u, ok := v.(*model.User); ok {
			return u
		}
	}
	return nil
}

// visibleSources 返回当前用户可见的站点 ID 集合。
// nil 表示管理员（可见全部）；非 nil 的空切片表示普通用户且没有任何授权。
func visibleSources(c *gin.Context) []uint {
	v, ok := c.Get(string(ctxKeyVisible))
	if !ok || v == nil {
		return nil
	}
	ids, ok := v.([]uint)
	if !ok {
		return nil
	}
	return ids
}

// authRequired 校验 Bearer 令牌，并把账号与站点可见范围写入上下文。
// 每个请求都重新读取授权关系，因此管理员调整权限后立即生效。
func (s *Server) authRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := bearerToken(c)
		if token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "请先登录"})
			return
		}
		u, err := s.store.GetSessionUser(tokenHash(token))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized,
				gin.H{"error": "登录已失效，请重新登录"})
			return
		}
		visible := []uint(nil) // 管理员
		if !u.IsAdmin() {
			visible, err = s.store.ListSourceIDsByUser(u.ID)
			if err != nil {
				c.AbortWithStatusJSON(http.StatusInternalServerError,
					gin.H{"error": "读取站点权限失败: " + err.Error()})
				return
			}
			if visible == nil {
				visible = []uint{} // 区分「无授权」与「管理员」
			}
		}
		c.Set(string(ctxKeyUser), u)
		c.Set(string(ctxKeyVisible), visible)
		c.Set(string(ctxKeyToken), tokenHash(token))
		c.Next()
	}
}

// adminOnly 限制只有管理员可访问。
func (s *Server) adminOnly() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !currentUser(c).IsAdmin() {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "仅管理员可执行此操作"})
			return
		}
		c.Next()
	}
}

// issueToken 为账号签发一个新的会话令牌。
func (s *Server) issueToken(u *model.User) (string, time.Time, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", time.Time{}, err
	}
	token := hex.EncodeToString(buf)
	expires := time.Now().Add(time.Duration(s.auth.TokenTTLHours) * time.Hour)
	sess := &model.Session{
		TokenHash: tokenHash(token),
		UserID:    u.ID,
		ExpiresAt: expires,
	}
	if err := s.store.CreateSession(sess); err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

// authStatus 是公开接口，供登录页判断系统是否已有账号。
func (s *Server) authStatus(c *gin.Context) {
	n, err := s.store.CountUsers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"initialized": n > 0, "userCount": n})
}

type loginInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// login 处理登录。系统还没有任何账号时，第一个登录者会带着输入的账号密码
// 自动注册为管理员；此后账号只能由管理员创建。
func (s *Server) login(c *gin.Context) {
	var in loginInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误: " + err.Error()})
		return
	}
	username := strings.TrimSpace(in.Username)
	password := in.Password
	if username == "" || password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户名和密码不能为空"})
		return
	}
	if len(username) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户名过长（最多 64 字符）"})
		return
	}

	count, err := s.store.CountUsers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var u *model.User
	firstUser := false
	if count == 0 {
		hash, hashErr := bcrypt.GenerateFromPassword([]byte(password), s.auth.BCryptCost)
		if hashErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "密码加密失败"})
			return
		}
		candidate := &model.User{
			Username:     username,
			PasswordHash: string(hash),
			Role:         model.RoleAdmin,
			Enabled:      true,
		}
		if err := s.store.CreateUser(candidate); err == nil {
			u, firstUser = candidate, true
		}
		// 创建失败（典型情形是并发下已有首个账号抢先注册）时回退到普通登录校验，
		// 由用户名唯一索引保证系统里不会出现第二个「首用户」。
	}

	if u == nil {
		u, err = s.store.GetUserByUsername(username)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
			return
		}
		if !u.Enabled {
			c.JSON(http.StatusForbidden, gin.H{"error": "账号已停用，请联系管理员"})
			return
		}
		if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
			return
		}
	}

	if err := s.store.TouchLogin(u.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	_ = s.store.PurgeExpiredSessions()
	token, expires, err := s.issueToken(u)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建会话失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"token":     token,
		"expiresAt": expires,
		"firstUser": firstUser,
		"isAdmin":   u.IsAdmin(),
		"user":      u,
	})
}

func (s *Server) logout(c *gin.Context) {
	if v, ok := c.Get(string(ctxKeyToken)); ok {
		if h, ok := v.(string); ok {
			_ = s.store.DeleteSession(h)
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server) me(c *gin.Context) {
	u := currentUser(c)
	ids := []uint{}
	if !u.IsAdmin() {
		ids, _ = s.store.ListSourceIDsByUser(u.ID)
		if ids == nil {
			ids = []uint{}
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"user":             u,
		"isAdmin":          u.IsAdmin(),
		"visibleSourceIds": ids,
	})
}

// changePassword 允许登录用户修改自己的密码；修改后该账号的其他会话全部失效。
func (s *Server) changePassword(c *gin.Context) {
	var in struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误"})
		return
	}
	if len(in.NewPassword) < 6 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "新密码至少 6 位"})
		return
	}
	u := currentUser(c)
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.OldPassword)) != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "原密码错误"})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.NewPassword), s.auth.BCryptCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "密码加密失败"})
		return
	}
	u.PasswordHash = string(hash)
	if err := s.store.SaveUser(u); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	_ = s.store.DeleteUserSessions(u.ID)
	token, expires, err := s.issueToken(u)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建会话失败: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"token": token, "expiresAt": expires,
		"message": "密码已修改，其他登录会话已失效",
	})
}
