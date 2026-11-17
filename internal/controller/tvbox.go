package controller

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/SilenCN/AssetSpider/internal/service"
	"github.com/SilenCN/AssetSpider/internal/utils"
	"github.com/gin-gonic/gin"
)

// TVBoxController 提供 TVBox / 影视仓可直接消费的公开接口。
// 鉴权不用登录会话（盒子无法填 Bearer），而用 URL 路径里的 api key：
// 每个 key 背后是一个用户，解析出的 Principal 决定了可见站点范围。
type TVBoxController struct {
	auth  *service.AuthService
	tvbox *service.TVBoxService
}

func NewTVBoxController(auth *service.AuthService, tvbox *service.TVBoxService) *TVBoxController {
	return &TVBoxController{auth: auth, tvbox: tvbox}
}

// resolve 从路径参数解析接口键对应的用户主体；失败时写入 401 并中断。
func (t *TVBoxController) resolve(c *gin.Context) (service.Principal, bool) {
	p, err := t.auth.ResolveByApiKey(c.Param("key"))
	if err != nil {
		writeError(c, err)
		return service.Principal{}, false
	}
	return p, true
}

// Config 返回 TVBox 配置文件：当前 key 可见的每个站点一个 type=1 采集源。
func (t *TVBoxController) Config(c *gin.Context) {
	p, ok := t.resolve(c)
	if !ok {
		return
	}
	out, err := t.tvbox.Config(requestBaseURL(c), c.Param("key"), p)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// Vod 实现苹果CMS V10 provide/vod 协议（ac=list / videolist / detail）。
// 站点 ID 在路径里，TVBox 追加的 ?ac=&pg=&t=&wd= 等参数不会被它干扰。
func (t *TVBoxController) Vod(c *gin.Context) {
	p, ok := t.resolve(c)
	if !ok {
		return
	}
	sourceID, err := utils.ParseUintParam(c.Param("sourceID"))
	if err != nil {
		badRequest(c, "站点编号非法")
		return
	}
	req := service.VodRequest{
		SourceID: sourceID,
		Action:   strings.TrimSpace(c.Query("ac")),
		TypeID:   c.Query("t"),
		Keyword:  c.Query("wd"),
		IDs:      c.Query("ids"),
		Page:     clampQueryInt(c.Query("pg"), 1),
		Size:     clampQueryInt(c.Query("limit"), 20),
	}
	out, err := t.tvbox.Vod(req, p)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// requestBaseURL 从请求还原对外根地址（scheme + host），供配置里的 api 拼绝对 URL。
// 反向代理场景优先取 X-Forwarded-Proto / X-Forwarded-Host。
func requestBaseURL(c *gin.Context) string {
	scheme := c.GetHeader("X-Forwarded-Proto")
	if scheme == "" {
		if c.Request.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	scheme = strings.ToLower(strings.TrimSpace(strings.Split(scheme, ",")[0]))
	host := c.GetHeader("X-Forwarded-Host")
	if host == "" {
		host = c.Request.Host
	}
	return scheme + "://" + strings.TrimSpace(strings.Split(host, ",")[0])
}

// clampQueryInt 解析查询里的整数，失败或越界时回退默认值。
func clampQueryInt(raw string, def int) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 {
		return def
	}
	if n > 100000 {
		return 100000
	}
	return n
}
