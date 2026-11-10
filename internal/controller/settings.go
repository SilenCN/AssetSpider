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

// Update 在线更新采集配置：校验通过后写回配置文件并立即生效。
//
// 先把当前配置作为默认值再绑定请求体：这样前端只提交部分字段时，
// 未提交的字段保持原值，避免被零值覆盖导致校验失败或误改。
func (s *SettingsController) Update(c *gin.Context) {
	in := s.svc.Current()
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
