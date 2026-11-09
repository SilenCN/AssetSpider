package controller

import (
	"net/http"

	"github.com/SilenCN/AssetSpider/internal/service"
	"github.com/SilenCN/AssetSpider/internal/utils"
	"github.com/gin-gonic/gin"
)

type TagController struct {
	svc *service.TagService
}

func NewTagController(svc *service.TagService) *TagController {
	return &TagController{svc: svc}
}

// List 返回标签，可用 source_id 只看某个资源站的一套标签（含影片数）。
func (c *TagController) List(ctx *gin.Context) {
	out, err := c.svc.List(utils.ParseUintQuery(ctx.Query("source_id")), principalOf(ctx))
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, out)
}
