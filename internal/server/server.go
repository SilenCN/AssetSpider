// Package server 提供基于 Gin 的 HTTP 服务与 Vue 静态页面托管。
package server

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/SilenCN/AssetSpider/internal/collector"
	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/store"
	"github.com/gin-gonic/gin"
)

// Server 组合数据存储、采集编排器与前端目录。
type Server struct {
	store  *store.Store
	runner *collector.Runner
	webDir string
	auth   config.AuthConfig
}

// New 创建 Server，采集器与登录会话参数按传入配置初始化。
func New(s *store.Store, cfg *config.Config) *Server {
	return &Server{
		store:  s,
		runner: collector.NewRunner(s, cfg.Collector),
		webDir: cfg.Web.Dir,
		auth:   cfg.Auth,
	}
}

// Router 构建 Gin 路由。
//
// 权限分层：
//   - 公开：/api/health、/api/auth/status、/api/auth/login
//   - 登录用户：读取类接口（按站点可见性过滤）、对可见站点触发采集
//   - 管理员：用户管理、站点配置增删改、资产台账写入
func (s *Server) Router() *gin.Engine {
	r := gin.Default()

	api := r.Group("/api")
	{
		api.GET("/health", s.health)
		api.GET("/auth/status", s.authStatus)
		api.POST("/auth/login", s.login)

		protected := api.Group("")
		protected.Use(s.authRequired())
		{
			protected.POST("/auth/logout", s.logout)
			protected.GET("/auth/me", s.me)
			protected.POST("/auth/password", s.changePassword)

			// 资产台账（保留的通用能力）
			protected.GET("/assets", s.listAssets)

			// 采集站点：读取按权限过滤，写操作仅管理员
			protected.GET("/sources", s.listSources)

			// 批量采集、入库数据、采集记录：按权限过滤
			protected.GET("/videos", s.listVideos)
			protected.GET("/videos/:id", s.getVideo)
			protected.GET("/meta/fields", s.listVideoFields)
			protected.GET("/jobs", s.listJobs)

			// 采集触发：登录用户仅能作用于自己可见的站点
			protected.POST("/sources/:id/crawl", s.crawlSource)
			protected.POST("/crawl", s.crawlAll)

			admin := protected.Group("")
			admin.Use(s.adminOnly())
			{
				admin.POST("/assets", s.createAsset)
				admin.DELETE("/assets/:id", s.deleteAsset)

				admin.POST("/sources", s.createSource)
				admin.PUT("/sources/:id", s.updateSource)
				admin.DELETE("/sources/:id", s.deleteSource)

				admin.GET("/users", s.listUsers)
				admin.POST("/users", s.createUser)
				admin.PUT("/users/:id", s.updateUser)
				admin.DELETE("/users/:id", s.deleteUser)
			}
		}
	}

	index := filepath.Join(s.webDir, "index.html")
	assetsDir := filepath.Join(s.webDir, "assets")
	if fi, err := os.Stat(assetsDir); err == nil && fi.IsDir() {
		r.Static("/assets", assetsDir)
	}
	r.GET("/", func(c *gin.Context) { serveIndex(c, index) })
	r.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		serveIndex(c, index)
	})
	return r
}

func serveIndex(c *gin.Context, index string) {
	if _, err := os.Stat(index); err != nil {
		c.String(http.StatusServiceUnavailable,
			"前端页面尚未构建，请在 web/ 下执行 npm install && npm run build")
		return
	}
	c.File(index)
}

// health 只暴露通用状态；站点/影片数量属于受限数据，不在公开接口里返回。
func (s *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "ok", "database": s.store.Path(),
	})
}

func (s *Server) listAssets(c *gin.Context) {
	items, err := s.store.List()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, items)
}

func (s *Server) createAsset(c *gin.Context) {
	var a model.Asset
	if err := c.ShouldBindJSON(&a); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if strings.TrimSpace(a.Host) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "host 不能为空"})
		return
	}
	if err := s.store.Create(&a); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, a)
}

func (s *Server) deleteAsset(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的 id"})
		return
	}
	if err := s.store.Delete(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}
