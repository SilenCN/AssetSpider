// Package router 是四层结构的最上层，只负责把 URL 映射到 controller。
//
// 本包对 internal 的依赖只有 controller：不 import service / repository / model，
// 更不直接访问数据库或执行业务规则。
//
// 接口统一按 /api/{模块名}/{版本号} 组织，模块名为复数资源名词，
// 操作一律由 HTTP 动词表达（GET 查询 / POST 创建 / PUT 更新 / DELETE 删除），
// 路径中不出现动词。新增版本时只需新增一个 registerVx 分组，旧版本保持不动。
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
	// 系统运行态
	system := api.Group("/system/" + APIVersion)
	system.GET("/health", set.Health.Status)

	// 通用资产台账
	assets := api.Group("/assets/" + APIVersion)
	assets.GET("", set.Asset.List)
	assets.POST("", set.Asset.Create)
	assets.DELETE("/:id", set.Asset.Delete)

	// 采集站点配置；采集动作是站点的子资源 crawls
	sources := api.Group("/sources/" + APIVersion)
	sources.GET("", set.Source.List)
	sources.POST("", set.Source.Create)
	sources.PUT("/:id", set.Source.Update)
	sources.DELETE("/:id", set.Source.Delete)
	sources.POST("/:id/crawls", set.Crawl.RunOne)

	// 批量采集：POST 创建一次覆盖全部启用站点的采集任务
	crawls := api.Group("/crawls/" + APIVersion)
	crawls.POST("", set.Crawl.RunAll)

	// 入库影片；category_id 可按自动归档的分类过滤
	videos := api.Group("/videos/" + APIVersion)
	videos.GET("", set.Video.List)
	videos.GET("/:id", set.Video.Get)
	videos.GET("/:id/play-groups", set.Video.PlayGroups) // 按苹果CMS 字段解析出的播放组

	// 视频分类（由采集自动维护，每个资源站一套）
	categories := api.Group("/categories/" + APIVersion)
	categories.GET("", set.Category.List)
	categories.GET("/tree", set.Category.Tree)
	categories.GET("/:id", set.Category.Get)

	// 字段元数据
	meta := api.Group("/meta/" + APIVersion)
	meta.GET("/fields", set.Meta.Fields)

	// 采集记录
	jobs := api.Group("/jobs/" + APIVersion)
	jobs.GET("", set.Job.List)
}
