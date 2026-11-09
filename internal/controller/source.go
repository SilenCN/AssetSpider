package controller

import (
	"net/http"

	"github.com/SilenCN/AssetSpider/internal/service"
	"github.com/SilenCN/AssetSpider/internal/utils"
	"github.com/gin-gonic/gin"
)

type SourceController struct {
	svc *service.SourceService
}

func NewSourceController(svc *service.SourceService) *SourceController {
	return &SourceController{svc: svc}
}

func (s *SourceController) List(c *gin.Context) {
	out, err := s.svc.List(principalOf(c))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (s *SourceController) Create(c *gin.Context) {
	var in service.SourceInput
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

func (s *SourceController) Update(c *gin.Context) {
	id, err := utils.ParseUintParam(c.Param("id"))
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	var in service.SourceInput
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

func (s *SourceController) Delete(c *gin.Context) {
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
