// Package app 是依赖装配层（composition root），不属于四层调用链。
//
// 它唯一的职责是自底向上构造 repository → service → controller → router 并把
// 下层对象注入上层；这里没有任何业务调用，也不参与请求处理。任何依赖注入都
// 必须触碰全部四层，因此把装配收敛在单独一处，反而能保证四层内部不越级。
package app

import (
	"fmt"

	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/controller"
	"github.com/SilenCN/AssetSpider/internal/repository"
	"github.com/SilenCN/AssetSpider/internal/router"
	"github.com/SilenCN/AssetSpider/internal/service"
	"github.com/gin-gonic/gin"
)

// App 持有已装配完成的 HTTP 引擎与需要释放的资源。
type App struct {
	Engine *gin.Engine
	repos  *repository.Repositories
}

// New 按四层顺序完成构造与注入。
func New(cfg *config.Config) (*App, error) {
	// 第 4 层：数据访问，每个实体一个独立仓储对象
	repos, err := repository.Open(cfg.Database.Path)
	if err != nil {
		return nil, fmt.Errorf("初始化数据库失败: %w", err)
	}
	// 任务调度状态只存在于进程内存，重启后遗留的 running/queued 记录无法再推进，
	// 统一标记为中断，避免页面一直显示「进行中」（paused 的任务仍可继续）。
	if _, err := repos.Jobs.ResetInterrupted("服务重启，任务已中断（进度已定期保存，可重试从断点续采）"); err != nil {
		_ = repos.Close()
		return nil, fmt.Errorf("清理中断任务失败: %w", err)
	}

	// 第 3 层：业务服务，各自只注入需要的实体仓储
	healthSvc := service.NewHealthService(repos.Sources, repos.Videos, repos.Path())
	assetSvc := service.NewAssetService(repos.Assets)
	sourceSvc := service.NewSourceService(repos.Sources, repos.Videos)
	crawlSvc := service.NewCrawlService(repos.Sources, repos.Videos, repos.Categories, repos.Jobs, cfg.Collector)
	videoSvc := service.NewVideoService(repos.Videos, repos.Sources)
	categorySvc := service.NewCategoryService(repos.Categories, repos.Videos)
	metaSvc := service.NewMetaService()
	jobSvc := service.NewJobService(repos.Jobs)
	// 采集记录页要显示运行中任务的实时进度，这里把调度层注入为进度来源。
	jobSvc.SetLiveSource(crawlSvc)
	// 认证与站点授权：同一个服务同时支撑登录控制器与用户管理控制器。
	authSvc := service.NewAuthService(repos.Users, cfg.Auth)

	// 第 2 层：HTTP 控制器（注入对应服务）
	set := controller.Set{
		Health:   controller.NewHealthController(healthSvc),
		Asset:    controller.NewAssetController(assetSvc),
		Source:   controller.NewSourceController(sourceSvc),
		Crawl:    controller.NewCrawlController(crawlSvc),
		Task:     controller.NewTaskController(crawlSvc),
		Video:    controller.NewVideoController(videoSvc),
		Category: controller.NewCategoryController(categorySvc),
		Meta:     controller.NewMetaController(metaSvc),
		Job:      controller.NewJobController(jobSvc),
		Auth:     controller.NewAuthController(authSvc),
		Users:    controller.NewUserController(authSvc),
		Page:     controller.NewPageController(cfg.Web.Dir),
	}

	// 第 1 层：路由
	return &App{Engine: router.New(set), repos: repos}, nil
}

// Close 释放数据库连接。
func (a *App) Close() error { return a.repos.Close() }
