package controller

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

// PageController 负责前端静态页面与 SPA 回退，属于 HTTP 适配的一部分。
type PageController struct {
	index  string
	assets string
}

func NewPageController(webDir string) *PageController {
	return &PageController{
		index:  filepath.Join(webDir, "index.html"),
		assets: filepath.Join(webDir, "assets"),
	}
}

// AssetsDir / HasAssets 供 router 判断是否需要注册静态目录。
func (p *PageController) AssetsDir() string { return p.assets }

func (p *PageController) HasAssets() bool {
	fi, err := os.Stat(p.assets)
	return err == nil && fi.IsDir()
}

func (p *PageController) Index(c *gin.Context) { p.serveIndex(c) }

func (p *PageController) Fallback(c *gin.Context) {
	if strings.HasPrefix(c.Request.URL.Path, "/api/") {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	p.serveIndex(c)
}

func (p *PageController) serveIndex(c *gin.Context) {
	if _, err := os.Stat(p.index); err != nil {
		c.String(http.StatusServiceUnavailable,
			"前端页面尚未构建，请在 web/ 下执行 npm install && npm run build")
		return
	}
	c.File(p.index)
}
