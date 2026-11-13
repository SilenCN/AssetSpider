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

// TestDatabase 在保存前探测数据库配置是否可达（管理员）。
//
// 请求体为 { database: { driver, path, dsn, maxOpenConns, maxIdleConns, connMaxLifetimeMin } }，
// 与保存接口的数据库载荷共用同一份结构。连接成败是预期的探测结果而非接口异常，
// 因此统一返回 200：成功 {ok:true,message}，失败 {ok:false,error}。
func (s *SettingsController) TestDatabase(c *gin.Context) {
	var req struct {
		Database service.DatabaseTestInput `json:"database"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		badRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, s.svc.TestDatabase(req.Database))
}

// Restart 重启服务进程（管理员）。
//
// 请求被接受后返回 202：接口先正常响应，进程随后才优雅停止并原地重建，
// 因此页面能拿到「重启中」而不是连接被掐断。不支持重启时返回 ok=false 与原因。
func (s *SettingsController) Restart(c *gin.Context) {
	c.JSON(http.StatusAccepted, s.svc.Restart())
}
