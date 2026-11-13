package controller

import (
	"net/http"

	"github.com/SilenCN/AssetSpider/internal/service"
	"github.com/gin-gonic/gin"
)

// SettingsController 提供设置页的配置读取与在线更新。
type SettingsController struct {
	svc *service.SettingsService
}

func NewSettingsController(svc *service.SettingsService) *SettingsController {
	return &SettingsController{svc: svc}
}

// Get 返回可编辑的采集配置、字段元信息、被环境变量锁定的字段与只读启动项。
func (s *SettingsController) Get(c *gin.Context) {
	c.JSON(http.StatusOK, s.svc.Get())
}

// Update 在线更新采集配置与数据库配置：校验通过后写回配置文件并（视配置项）立即生效。
//
// 请求体为 { collector, database }；数据库后端切换在下次启动生效，采集配置即时生效。
func (s *SettingsController) Update(c *gin.Context) {
	var in service.SettingsUpdateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, err.Error())
		return
	}
	out, err := s.svc.Update(in)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
