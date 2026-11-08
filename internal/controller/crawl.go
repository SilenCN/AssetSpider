package controller

import (
	"errors"
	"net/http"

	"github.com/SilenCN/AssetSpider/internal/service"
	"github.com/SilenCN/AssetSpider/internal/utils"
	"github.com/gin-gonic/gin"
)

type CrawlController struct {
	svc *service.CrawlService
}

func NewCrawlController(svc *service.CrawlService) *CrawlController {
	return &CrawlController{svc: svc}
}

// readMode 优先从 JSON body 读取创建参数（RESTful），也兼容 query 传 mode；
// 具体取值规则交给 service.NormalizeMode 校验。
func (c *CrawlController) readMode(ctx *gin.Context) (string, bool) {
	var body struct {
		Mode string `json:"mode"`
	}
	_ = ctx.ShouldBindJSON(&body)
	raw := body.Mode
	if raw == "" {
		raw = ctx.Query("mode")
	}
	mode, err := service.NormalizeMode(raw)
	if err != nil {
		writeError(ctx, err)
		return "", false
	}
	return mode, true
}

func (c *CrawlController) RunOne(ctx *gin.Context) {
	id, err := utils.ParseUintParam(ctx.Param("id"))
	if err != nil {
		badRequest(ctx, err.Error())
		return
	}
	mode, ok := c.readMode(ctx)
	if !ok {
		return
	}
	res, err := c.svc.RunOne(ctx.Request.Context(), id, mode)
	if err != nil {
		c.writeError(ctx, err, res)
		return
	}
	ctx.JSON(http.StatusOK, res)
}

func (c *CrawlController) RunAll(ctx *gin.Context) {
	mode, ok := c.readMode(ctx)
	if !ok {
		return
	}
	results, err := c.svc.RunAll(ctx.Request.Context(), mode)
	body := gin.H{"mode": mode, "results": results}
	if err != nil {
		body["error"] = err.Error()
	}
	ctx.JSON(http.StatusOK, body)
}

// writeError 采集接口的默认失败是上游站点/抓取异常，按 502 返回；
// 参数、资源不存在、并发冲突分别映射为 400 / 404 / 409；
// 被手动暂停不是错误，按 200 返回带 paused 状态的结果。
func (c *CrawlController) writeError(ctx *gin.Context, err error, res *service.RunResult) {
	code := http.StatusBadGateway
	switch {
	case errors.Is(err, service.ErrPaused):
		code = http.StatusOK
	case errors.Is(err, service.ErrInvalid):
		code = http.StatusBadRequest
	case errors.Is(err, service.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, service.ErrConflict):
		code = http.StatusConflict
	}
	body := gin.H{"error": err.Error()}
	if res != nil {
		body["result"] = res
	}
	ctx.JSON(code, body)
}
