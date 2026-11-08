package controller

import (
	"net/http"

	"github.com/SilenCN/AssetSpider/internal/service"
	"github.com/gin-gonic/gin"
)

type MetaController struct {
	svc *service.MetaService
}

func NewMetaController(svc *service.MetaService) *MetaController {
	return &MetaController{svc: svc}
}

func (m *MetaController) Fields(c *gin.Context) {
	c.JSON(http.StatusOK, m.svc.Fields())
}
