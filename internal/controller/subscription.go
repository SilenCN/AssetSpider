package controller

import (
	"net/http"

	"github.com/SilenCN/AssetSpider/internal/service"
	"github.com/SilenCN/AssetSpider/internal/utils"
	"github.com/gin-gonic/gin"
)

// SubscriptionController 暴露站点配置订阅的增删改查与手动刷新。
type SubscriptionController struct {
	svc *service.SubscriptionService
}

func NewSubscriptionController(svc *service.SubscriptionService) *SubscriptionController {
	return &SubscriptionController{svc: svc}
}

func (s *SubscriptionController) List(c *gin.Context) {
	out, err := s.svc.List()
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (s *SubscriptionController) Create(c *gin.Context) {
	var in service.SubscriptionInput
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, err.Error())
		return
	}
	created, err := s.svc.Create(in)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, created)
}

func (s *SubscriptionController) Update(c *gin.Context) {
	id, err := utils.ParseUintParam(c.Param("id"))
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	var in service.SubscriptionInput
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, err.Error())
		return
	}
	updated, err := s.svc.Update(id, in)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

func (s *SubscriptionController) Delete(c *gin.Context) {
	id, err := utils.ParseUintParam(c.Param("id"))
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	if err := s.svc.Delete(id); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Refresh 手动拉取一次订阅，把里面的站点配置同步进站点表。
// 刷新失败会带着「失败原因」返回，前端直接展示，不需要再去别处查。
func (s *SubscriptionController) Refresh(c *gin.Context) {
	id, err := utils.ParseUintParam(c.Param("id"))
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	res, err := s.svc.Refresh(id)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}
