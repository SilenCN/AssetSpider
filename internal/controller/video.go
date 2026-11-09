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
	out, err := v.svc.List(service.VideoListQuery{
		SourceID:   utils.ParseUintQuery(c.Query("source_id")),
		CategoryID: utils.ParseUintQuery(c.Query("category_id")),
		TagID:      utils.ParseUintQuery(c.Query("tag_id")),
		AreaID:     utils.ParseUintQuery(c.Query("area_id")),
		Keyword:    c.Query("q"),
		Sort:       c.Query("sort"),
		Page:       utils.ParseIntQuery(c.Query("page")),
		Size:       utils.ParseIntQuery(c.Query("size")),
	}, principalOf(c))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// Get 返回单条影片的完整记录（含扩展字段与播放组）。
func (v *VideoController) Get(c *gin.Context) {
	id, err := utils.ParseUintParam(c.Param("id"))
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	item, err := v.svc.Get(id, principalOf(c))
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
	groups, err := v.svc.GetPlayGroups(id, principalOf(c))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, groups)
}
