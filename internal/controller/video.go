package controller

import (
	"net/http"

	"github.com/SilenCN/AssetSpider/internal/service"
	"github.com/SilenCN/AssetSpider/internal/utils"
	"github.com/gin-gonic/gin"
)

type VideoController struct {
	svc *service.VideoService
}

func NewVideoController(svc *service.VideoService) *VideoController {
	return &VideoController{svc: svc}
}

func (v *VideoController) List(c *gin.Context) {
	out, err := v.svc.List(
		utils.ParseUintQuery(c.Query("source_id")),
		utils.ParseUintQuery(c.Query("category_id")),
		c.Query("q"),
		utils.ParseIntQuery(c.Query("page")),
		utils.ParseIntQuery(c.Query("size")),
	)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// Get 返回单条影片的完整记录（含原始 JSON 与扩展字段）。
func (v *VideoController) Get(c *gin.Context) {
	id, err := utils.ParseUintParam(c.Param("id"))
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	item, err := v.svc.Get(id)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, item)
}

// PlayGroups 返回单条影片按苹果CMS 字段解析出的结构化播放组。
func (v *VideoController) PlayGroups(c *gin.Context) {
	id, err := utils.ParseUintParam(c.Param("id"))
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	groups, err := v.svc.GetPlayGroups(id)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, groups)
}
