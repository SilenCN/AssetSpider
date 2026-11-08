// Package controller 是四层结构中的第二层，负责 HTTP 协议适配。
//
// 只做三件事：绑定请求参数、调用下一层 service、把结果/错误翻译成 HTTP 响应。
// 本层不含任何业务规则，不允许出现 GORM、SQL、Gin 路由注册或数据模型构造。
// 通用的参数解析工具统一从横切包 utils 引用。
package controller

import (
	"errors"
	"net/http"

	"github.com/SilenCN/AssetSpider/internal/service"
	"github.com/gin-gonic/gin"
)

// Set 聚合全部控制器，由装配层构造后交给 router。
type Set struct {
	Health   *HealthController
	Asset    *AssetController
	Source   *SourceController
	Crawl    *CrawlController
	Task     *TaskController
	Video    *VideoController
	Category *CategoryController
	Meta     *MetaController
	Job      *JobController
	Auth     *AuthController
	Users    *UserController
	Page     *PageController
}

// writeError 把 service 层的哨兵错误翻译成 HTTP 状态码。
func writeError(c *gin.Context, err error) {
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, service.ErrInvalid):
		code = http.StatusBadRequest
	case errors.Is(err, service.ErrUnauthorized):
		code = http.StatusUnauthorized
	case errors.Is(err, service.ErrForbidden):
		code = http.StatusForbidden
	case errors.Is(err, service.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, service.ErrConflict):
		code = http.StatusConflict
	}
	c.JSON(code, gin.H{"error": err.Error()})
}

func badRequest(c *gin.Context, msg string) {
	c.JSON(http.StatusBadRequest, gin.H{"error": msg})
}
