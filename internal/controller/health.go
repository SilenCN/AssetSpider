package controller

import (
	"net/http"

	"github.com/SilenCN/AssetSpider/internal/service"
	"github.com/gin-gonic/gin"
)

type HealthController struct {
	svc *service.HealthService
}

func NewHealthController(svc *service.HealthService) *HealthController {
	return &HealthController{svc: svc}
}

func (h *HealthController) Status(c *gin.Context) {
	c.JSON(http.StatusOK, h.svc.Status())
}
