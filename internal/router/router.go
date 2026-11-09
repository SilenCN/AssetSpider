// Package router 是四层结构的最上层，只负责把 URL 映射到 controller。
//
// 本包对 internal 的依赖只有 controller：不 import service / repository / model，
// 更不直接访问数据库或执行业务规则。
//
// 接口统一按 /api/{模块名}/{版本号} 组织，模块名为复数资源名词，
// 操作一律由 HTTP 动词表达（GET 查询 / POST 创建 / PUT 更新 / DELETE 删除），
// 路径中不出现动词。新增版本时只需新增一个 registerVx 分组，旧版本保持不动。
//
// 认证分层：登录状态查询与创建会话（登录）公开；其余接口统一要求 Bearer 令牌；
// 账号管理、站点配置写操作与并发上限调整再叠加管理员限制。
package router

import (
	"github.com/SilenCN/AssetSpider/internal/controller"
	"github.com/gin-gonic/gin"
)

// APIVersion 是当前对外接口版本。
const APIVersion = "v1"

// New 构建 Gin 引擎：/api 为版本化接口，其余交给前端页面控制器。
func New(set controller.Set) *gin.Engine {
	r := gin.Default()
	registerV1(r.Group("/api"), set)

	if set.Page.HasAssets() {
		r.Static("/assets", set.Page.AssetsDir())
	}
	r.GET("/", set.Page.Index)
	r.NoRoute(set.Page.Fallback)
	return r
}

// registerV1 按模块注册 v1 接口，每个模块一个 Group。
func registerV1(api *gin.RouterGroup, set controller.Set) {
	// ---------- 公开接口 ----------
	api.GET("/system/"+APIVersion+"/health", set.Health.Status)
	authPublic := api.Group("/auth/" + APIVersion)
	authPublic.GET("/status", set.Auth.Status)
	authPublic.POST("/sessions", set.Auth.Login)

	// ---------- 需要登录 ----------
	secured := api.Group("", set.Auth.RequireAuth())
	auth := secured.Group("/auth/" + APIVersion)
	auth.DELETE("/sessions", set.Auth.Logout)
	auth.GET("/me", set.Auth.Me)
	auth.PUT("/password", set.Auth.ChangePassword)

	// ---------- 需要管理员（在登录校验之上叠加） ----------
	admin := api.Group("", set.Auth.RequireAuth(), set.Auth.RequireAdmin())

	// 通用资产台账：查询登录即可，写入仅管理员
	assets := secured.Group("/assets/" + APIVersion)
	assets.GET("", set.Asset.List)
	adminAssets := admin.Group("/assets/" + APIVersion)
	adminAssets.POST("", set.Asset.Create)
	adminAssets.DELETE("/:id", set.Asset.Delete)

	// 采集站点配置；采集动作是站点的子资源 crawls
	// 静态路径 export / import 注册在 :id 通配路径之前。
	sources := secured.Group("/sources/" + APIVersion)
	sources.GET("", set.Source.List)
	sources.GET("/export", set.Source.Export)
	sources.POST("/:id/crawls", set.Crawl.RunOne)
	adminSources := admin.Group("/sources/" + APIVersion)
	adminSources.POST("", set.Source.Create)
	adminSources.POST("/import", set.Source.Import)
	adminSources.PUT("/:id", set.Source.Update)
	adminSources.DELETE("/:id", set.Source.Delete)
	// 只清空站点名下的数据（影片/分类/标签/采集记录），站点配置保留。
	adminSources.DELETE("/:id/data", set.Source.Clear)

	// 批量采集：POST 创建一次覆盖全部可见启用站点的采集任务
	crawls := secured.Group("/crawls/" + APIVersion)
	crawls.POST("", set.Crawl.RunAll)

	// 采集任务：异步入队、查询进度、暂停/继续/重试/重新运行，以及并发上限配置。
	// 静态路径 capacity 必须注册在 :id 之前，否则 Gin 会报通配符冲突。
	tasks := secured.Group("/tasks/" + APIVersion)
	tasks.GET("", set.Task.List)
	tasks.POST("", set.Task.Create)
	tasks.GET("/capacity", set.Task.Capacity)
	admin.PUT("/tasks/"+APIVersion+"/capacity", set.Task.SetCapacity)
	tasks.PUT("/:id", set.Task.Update)
	tasks.POST("/:id/retry", set.Task.Retry) // 失败任务：从失败页续采
	tasks.POST("/:id/rerun", set.Task.Rerun) // 已结束任务：从第 1 页重新运行

	// 入库影片；category_id 可按自动归档的分类过滤
	videos := secured.Group("/videos/" + APIVersion)
	videos.GET("", set.Video.List)
	videos.GET("/areas", set.Video.Areas) // 地域筛选项
	videos.GET("/:id", set.Video.Get)
	videos.GET("/:id/play-groups", set.Video.PlayGroups) // 按苹果CMS 字段解析出的播放组

	// 视频分类（由采集自动维护，每个资源站一套）
	categories := secured.Group("/categories/" + APIVersion)
	categories.GET("", set.Category.List)
	categories.GET("/tree", set.Category.Tree)
	categories.GET("/:id", set.Category.Get)

	// 视频标签（由采集按 vod_tag 自动维护，每个资源站一套）
	tags := secured.Group("/tags/" + APIVersion)
	tags.GET("", set.Tag.List)

	// 字段元数据
	meta := secured.Group("/meta/" + APIVersion)
	meta.GET("/fields", set.Meta.Fields)

	// 采集记录
	jobs := secured.Group("/jobs/" + APIVersion)
	jobs.GET("", set.Job.List)

	// 用户与站点可见性授权
	users := admin.Group("/users/" + APIVersion)
	users.GET("", set.Users.List)
	users.POST("", set.Users.Create)
	users.PUT("/:id", set.Users.Update)
	users.DELETE("/:id", set.Users.Delete)
}
