package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// userInput 是用户管理接口允许提交的字段。
// SourceIDs 用指针以区分「未提交（不改授权）」与「提交空数组（清空授权）」。
type userInput struct {
	Username  string  `json:"username"`
	Password  string  `json:"password"`
	Role      string  `json:"role"`
	Enabled   *bool   `json:"enabled"`
	SourceIDs *[]uint `json:"sourceIds"`
}

// userView 在账号信息上附加其可见站点 ID 列表。
// 嵌入的 User.PasswordHash 带 json:"-"，不会泄露给前端。
type userView struct {
	model.User
	SourceIDs []uint `json:"sourceIds"`
}

func (s *Server) userViewOf(u model.User, grants map[uint][]uint) userView {
	ids := grants[u.ID]
	if ids == nil {
		ids = []uint{}
	}
	return userView{User: u, SourceIDs: ids}
}

func (s *Server) grantsMap(c *gin.Context) (map[uint][]uint, bool) {
	grants, err := s.store.ListSourceIDsByUsers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return nil, false
	}
	return grants, true
}

func (s *Server) listUsers(c *gin.Context) {
	users, err := s.store.ListUsers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	grants, ok := s.grantsMap(c)
	if !ok {
		return
	}
	out := make([]userView, 0, len(users))
	for _, u := range users {
		out = append(out, s.userViewOf(u, grants))
	}
	c.JSON(http.StatusOK, out)
}

func validRole(role string) bool {
	return role == model.RoleAdmin || role == model.RoleUser
}

func (s *Server) createUser(c *gin.Context) {
	var in userInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误: " + err.Error()})
		return
	}
	username := strings.TrimSpace(in.Username)
	if username == "" || len(username) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "用户名不能为空且最多 64 字符"})
		return
	}
	if len(in.Password) < 6 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "密码至少 6 位"})
		return
	}
	role := in.Role
	if role == "" {
		role = model.RoleUser
	}
	if !validRole(role) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "角色仅支持 admin / user"})
		return
	}
	if _, err := s.store.GetUserByUsername(username); err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "用户名已存在"})
		return
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), s.auth.BCryptCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "密码加密失败"})
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	u := &model.User{
		Username: username, PasswordHash: string(hash),
		Role: role, Enabled: enabled,
	}
	if err := s.store.CreateUser(u); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if in.SourceIDs != nil {
		if err := s.store.SetUserSources(u.ID, *in.SourceIDs); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	grants, ok := s.grantsMap(c)
	if !ok {
		return
	}
	c.JSON(http.StatusCreated, s.userViewOf(*u, grants))
}

func (s *Server) updateUser(c *gin.Context) {
	id, err := parseUintParam(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	u, err := s.store.GetUser(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}
	var in userInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求格式错误: " + err.Error()})
		return
	}
	self := currentUser(c)
	isSelf := self != nil && self.ID == id

	// 自我保护：不能把自己停用或降级，避免管理员把自己锁在系统外。
	if isSelf {
		if in.Enabled != nil && !*in.Enabled {
			c.JSON(http.StatusBadRequest, gin.H{"error": "不能停用自己的账号"})
			return
		}
		if in.Role != "" && in.Role != model.RoleAdmin {
			c.JSON(http.StatusBadRequest, gin.H{"error": "不能降低自己的角色"})
			return
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
		n, err := s.store.CountEnabledAdmins()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if n <= 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "系统必须保留至少一名启用状态的管理员"})
			return
		}
	}

	revokeSessions := false
	if in.Password != "" {
		if len(in.Password) < 6 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "密码至少 6 位"})
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), s.auth.BCryptCost)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "密码加密失败"})
			return
		}
		u.PasswordHash = string(hash)
		revokeSessions = true
	}
	if in.Role != "" {
		if !validRole(in.Role) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "角色仅支持 admin / user"})
			return
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
	if err := s.store.SaveUser(u); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if in.SourceIDs != nil {
		if err := s.store.SetUserSources(id, *in.SourceIDs); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	if revokeSessions {
		_ = s.store.DeleteUserSessions(id)
	}
	grants, ok := s.grantsMap(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, s.userViewOf(*u, grants))
}

func (s *Server) deleteUser(c *gin.Context) {
	id, err := parseUintParam(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	u, err := s.store.GetUser(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}
	if self := currentUser(c); self != nil && self.ID == id {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不能删除自己的账号"})
		return
	}
	if u.Role == model.RoleAdmin && u.Enabled {
		n, err := s.store.CountEnabledAdmins()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if n <= 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "系统必须保留至少一名启用状态的管理员"})
			return
		}
	}
	if err := s.store.DeleteUser(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}
