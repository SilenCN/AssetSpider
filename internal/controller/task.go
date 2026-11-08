package controller

import (
	"net/http"

	"github.com/SilenCN/AssetSpider/internal/service"
	"github.com/SilenCN/AssetSpider/internal/utils"
	"github.com/gin-gonic/gin"
)

// TaskController 暴露采集任务的运行时管理接口：
// 创建（异步入队）、查询进度、暂停、继续，以及并发上限的读取与调整。
type TaskController struct {
	svc *service.CrawlService
}

func NewTaskController(svc *service.CrawlService) *TaskController {
	return &TaskController{svc: svc}
}

// List 返回当前任务与并发容量。
func (t *TaskController) List(c *gin.Context) {
	c.JSON(http.StatusOK, t.svc.ListTasks())
}

// Create 异步发起采集任务：body {sourceId, mode}，mode 缺省为增量。
func (t *TaskController) Create(c *gin.Context) {
	var in struct {
		SourceID uint   `json:"sourceId"`
		Mode     string `json:"mode"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, err.Error())
		return
	}
	if in.SourceID == 0 {
		badRequest(c, "sourceId 不能为空")
		return
	}
	mode, err := service.NormalizeMode(in.Mode)
	if err != nil {
		writeError(c, err)
		return
	}
	view, err := t.svc.StartTask(in.SourceID, mode)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, view)
}

// Update 更新任务状态：paused 暂停 / running 继续。
func (t *TaskController) Update(c *gin.Context) {
	id, err := utils.ParseUintParam(c.Param("id"))
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, err.Error())
		return
	}
	switch in.Status {
	case "paused":
		view, err := t.svc.PauseTask(id)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusOK, view)
	case "running":
		view, err := t.svc.ResumeTask(id)
		if err != nil {
			writeError(c, err)
			return
		}
		c.JSON(http.StatusAccepted, view)
	default:
		badRequest(c, "status 仅支持 paused（暂停）与 running（继续）")
	}
}

// Capacity 读取并发容量。
func (t *TaskController) Capacity(c *gin.Context) {
	c.JSON(http.StatusOK, t.svc.Capacity())
}

// SetCapacity 运行时调整同时运行的任务数上限。
func (t *TaskController) SetCapacity(c *gin.Context) {
	var in struct {
		MaxConcurrent int `json:"maxConcurrent"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, err.Error())
		return
	}
	out, err := t.svc.SetMaxConcurrent(in.MaxConcurrent)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
