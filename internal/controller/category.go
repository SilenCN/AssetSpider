package controller

import (
	"net/http"

	"github.com/SilenCN/AssetSpider/internal/service"
	"github.com/SilenCN/AssetSpider/internal/utils"
	"github.com/gin-gonic/gin"
)

type CategoryController struct {
	svc *service.CategoryService
}

func NewCategoryController(svc *service.CategoryService) *CategoryController {
	return &CategoryController{svc: svc}
}

// List 返回分类，可用 source_id 只看某个资源站的一套分类。
func (c *CategoryController) List(ctx *gin.Context) {
	out, err := c.svc.List(utils.ParseUintQuery(ctx.Query("source_id")))
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, out)
}

func (c *CategoryController) Get(ctx *gin.Context) {
	id, err := utils.ParseUintParam(ctx.Param("id"))
	if err != nil {
		badRequest(ctx, err.Error())
		return
	}
	item, err := c.svc.Get(id)
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, item)
}

// Tree 返回分类的二级树（父分类在前、子分类嵌套）。
func (c *CategoryController) Tree(ctx *gin.Context) {
	out, err := c.svc.Tree(utils.ParseUintQuery(ctx.Query("source_id")))
	if err != nil {
		writeError(ctx, err)
		return
	}
	ctx.JSON(http.StatusOK, out)
}
