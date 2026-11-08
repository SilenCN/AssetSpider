package controller

import (
	"errors"
	"net/http"
	"strings"

	"github.com/SilenCN/AssetSpider/internal/service"
	"github.com/SilenCN/AssetSpider/internal/utils"
	"github.com/gin-gonic/gin"
)

// gin.Context 中存放当前登录态的键。
type ctxKey string

const ctxKeyPrincipal ctxKey = "authPrincipal"

// bearerToken 从 Authorization 头取出 Bearer 令牌。
func bearerToken(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// principalOf 返回认证中间件解析出的登录主体。
// 若请求没有经过中间件（只可能出现在内部调用），返回零值主体：
// 非管理员且无任何站点授权，天然不会泄露数据。
func principalOf(c *gin.Context) service.Principal {
	if v, ok := c.Get(string(ctxKeyPrincipal)); ok {
		if p, ok := v.(service.Principal); ok {
			return p
		}
	}
	return service.Principal{}
}

// AuthController 提供登录、会话与当前用户接口，并产出认证/管理员中间件。
// 业务规则全部在 service.AuthService，本层只做协议适配。
type AuthController struct {
	svc *service.AuthService
}

func NewAuthController(svc *service.AuthService) *AuthController {
	return &AuthController{svc: svc}
}

// RequireAuth 校验 Bearer 令牌，并把登录主体写入上下文。
// 每个请求都重新读取站点授权，因此管理员调整权限后立即生效。
func (a *AuthController) RequireAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		p, err := a.svc.Resolve(bearerToken(c))
		if err != nil {
			if errors.Is(err, service.ErrUnauthorized) {
				c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
				return
			}
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Set(string(ctxKeyPrincipal), p)
		c.Next()
	}
}

// RequireAdmin 限制只有管理员可访问（须接在 RequireAuth 之后）。
func (a *AuthController) RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !principalOf(c).IsAdmin {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "仅管理员可执行此操作"})
			return
		}
		c.Next()
	}
}

// Status 是公开接口，供登录页判断系统是否已有账号。
func (a *AuthController) Status(c *gin.Context) {
	out, err := a.svc.Status()
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// Login 创建会话。系统还没有账号时，第一个登录者自动注册为管理员。
func (a *AuthController) Login(c *gin.Context) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, "请求格式错误: "+err.Error())
		return
	}
	out, err := a.svc.Login(in.Username, in.Password)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// Logout 吊销当前令牌。
func (a *AuthController) Logout(c *gin.Context) {
	if err := a.svc.Logout(principalOf(c).TokenHash); err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// Me 返回当前账号与其可见站点。
func (a *AuthController) Me(c *gin.Context) {
	out, err := a.svc.Me(principalOf(c))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// ChangePassword 修改当前账号密码，签发新令牌并作废旧会话。
func (a *AuthController) ChangePassword(c *gin.Context) {
	var in struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, "请求格式错误: "+err.Error())
		return
	}
	out, err := a.svc.ChangePassword(principalOf(c), in.OldPassword, in.NewPassword)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"token": out.Token, "expiresAt": out.ExpiresAt,
		"message": "密码已修改，其他登录会话已失效",
	})
}

// UserController 提供管理员的账号与站点授权管理接口。
type UserController struct {
	svc *service.AuthService
}

func NewUserController(svc *service.AuthService) *UserController {
	return &UserController{svc: svc}
}

func (u *UserController) List(c *gin.Context) {
	out, err := u.svc.ListUsers(principalOf(c))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (u *UserController) Create(c *gin.Context) {
	var in service.UserInput
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, "请求格式错误: "+err.Error())
		return
	}
	out, err := u.svc.CreateUser(in, principalOf(c))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

func (u *UserController) Update(c *gin.Context) {
	id, err := utils.ParseUintParam(c.Param("id"))
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	var in service.UserInput
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, "请求格式错误: "+err.Error())
		return
	}
	out, err := u.svc.UpdateUser(id, in, principalOf(c))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (u *UserController) Delete(c *gin.Context) {
	id, err := utils.ParseUintParam(c.Param("id"))
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	if err := u.svc.DeleteUser(id, principalOf(c)); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
