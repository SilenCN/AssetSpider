package controller

import (
	"net/http"

	"github.com/SilenCN/AssetSpider/internal/service"
	"github.com/SilenCN/AssetSpider/internal/utils"
	"github.com/gin-gonic/gin"
)

type JobController struct {
	svc *service.JobService
}

func NewJobController(svc *service.JobService) *JobController {
	return &JobController{svc: svc}
}

func (j *JobController) List(c *gin.Context) {
	items, err := j.svc.List(
		utils.ParseUintQuery(c.Query("source_id")),
		utils.ParseIntQuery(c.Query("limit")),
	)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, items)
}
