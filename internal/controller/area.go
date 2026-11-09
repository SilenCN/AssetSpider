package controller

import (
	"net/http"

	"github.com/SilenCN/AssetSpider/internal/service"
	"github.com/SilenCN/AssetSpider/internal/utils"
	"github.com/gin-gonic/gin"
)

// AreaController 暴露地域列表（筛选项）。
// 地域由采集过程按影片的 vod_area 自动拆分维护，与标签同构，只读。
type AreaController struct {
	svc *service.AreaService
}

func NewAreaController(svc *service.AreaService) *AreaController {
	return &AreaController{svc: svc}
}

// List 返回地域，可用 source_id 只看某个资源站的一套地域（含影片数）。
func (c *AreaController) List(ctx *gin.Context) {
	out, err := c.svc.List(utils.ParseUintQuery(ctx.Query("source_id")), principalOf(ctx))
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, out)
}
