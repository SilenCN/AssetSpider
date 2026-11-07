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
}

// New 创建 Server，采集器按传入配置初始化。
func New(s *store.Store, webDir string, cc config.CollectorConfig) *Server {
	return &Server{
		store:  s,
		runner: collector.NewRunner(s, cc),
		webDir: webDir,
	}
}

// Router 构建 Gin 路由：/api 为接口，其余交给 Vue 页面。
func (s *Server) Router() *gin.Engine {
	r := gin.Default()

	api := r.Group("/api")
	{
		api.GET("/health", s.health)

		// 资产台账（保留的通用能力）
		api.GET("/assets", s.listAssets)
		api.POST("/assets", s.createAsset)
		api.DELETE("/assets/:id", s.deleteAsset)

		// 采集站点配置
		api.GET("/sources", s.listSources)
		api.POST("/sources", s.createSource)
		api.PUT("/sources/:id", s.updateSource)
		api.DELETE("/sources/:id", s.deleteSource)
		api.POST("/sources/:id/crawl", s.crawlSource)

		// 批量采集、入库数据、采集记录
		api.POST("/crawl", s.crawlAll)
		api.GET("/videos", s.listVideos)
		api.GET("/videos/:id", s.getVideo)
		api.GET("/meta/fields", s.listVideoFields)
		api.GET("/jobs", s.listJobs)
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

func (s *Server) health(c *gin.Context) {
	sources, _ := s.store.ListSources()
	_, total, _ := s.store.ListVideos(0, "", 1, 1)
	c.JSON(http.StatusOK, gin.H{
		"status": "ok", "database": s.store.Path(),
		"sources": len(sources), "videos": total,
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
