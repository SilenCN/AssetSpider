package controller

import (
	"log"
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
	// ids 是苹果CMS 标准参数；少数客户端点播详情用单数 id，兼容之。
	ids := c.Query("ids")
	if strings.TrimSpace(ids) == "" {
		ids = c.Query("id")
	}
	// limit 是苹果CMS 参数；影视仓等也常用 pagesize，兼容之。
	limit := c.Query("limit")
	if strings.TrimSpace(limit) == "" {
		limit = c.Query("pagesize")
	}
	req := service.VodRequest{
		SourceID: sourceID,
		Action:   strings.TrimSpace(c.Query("ac")),
		TypeID:   c.Query("t"),
		Keyword:  c.Query("wd"),
		IDs:      ids,
		Page:     clampQueryInt(c.Query("pg"), 1),
		Size:     clampQueryInt(limit, 20),
		// 二次筛选参数：key 与筛选面板里的 VodFilter.Key 一致。
		By:    strings.TrimSpace(c.Query("by")),
		Year:  strings.TrimSpace(c.Query("year")),
		Area:  strings.TrimSpace(c.Query("area")),
		Class: strings.TrimSpace(c.Query("class")),
		State: strings.TrimSpace(c.Query("state")),
	}
	out, err := t.tvbox.Vod(req, p)
	if err != nil {
		log.Printf("[tvbox] vod 失败 sourceID=%d ac=%q ids=%q wd=%q err=%v",
			sourceID, req.Action, req.IDs, req.Keyword, err)
		writeError(c, err)
		return
	}
	// 诊断日志：TVBox / 影视仓点播排障时，可据此确认请求是否到达、客户端实际
	// 拼了哪些参数、以及是否命中了影片。list 为空通常是客户端拿不到播放地址的
	// 直接原因（ids 对不上 / 站点不可见 / 库里无此片）。量可控，仅点播请求触发。
	log.Printf("[tvbox] vod sourceID=%d ac=%q ids=%q wd=%q t=%q pg=%d size=%d -> list=%d",
		sourceID, req.Action, req.IDs, req.Keyword, req.TypeID, req.Page, req.Size, len(out.List))
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
