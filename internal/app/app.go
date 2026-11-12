// Package app 是依赖装配层（composition root），不属于四层调用链。
//
// 它唯一的职责是自底向上构造 repository → service → controller → router 并把
// 下层对象注入上层；除「启动时自动恢复中断任务」这一启动钩子外，没有任何业务
// 调用，也不参与请求处理。任何依赖注入都必须触碰全部四层，因此把装配收敛在
// 单独一处，反而能保证四层内部不越级。
package app

import (
	"fmt"
	"log"
	"time"

	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/controller"
	"github.com/SilenCN/AssetSpider/internal/repository"
	"github.com/SilenCN/AssetSpider/internal/router"
	"github.com/SilenCN/AssetSpider/internal/service"
	"github.com/gin-gonic/gin"
)

// App 持有已装配完成的 HTTP 引擎与需要释放的资源。
type App struct {
	Engine    *gin.Engine
	repos     *repository.Repositories
	crawl     *service.CrawlService
	scheduler *service.ScheduleService
}

// New 按四层顺序完成构造与注入。
func New(cfg *config.Config) (*App, error) {
	// 第 4 层：数据访问，每个实体一个独立仓储对象
	repos, err := repository.Open(cfg.Database.Path)
	if err != nil {
		return nil, fmt.Errorf("初始化数据库失败: %w", err)
	}
	// 启动钩子：把各站点的影片总数与 videos 表对齐。带 video_count 列的版本首次
	// 启动时该列是默认值 0，不校准的话站点列表会显示「0 部影片」直到下次采集。
	if err := repos.Sources.RefreshVideoCounts(); err != nil {
		log.Printf("启动校准站点影片总数失败（不影响服务启动）: %v", err)
	}
	// 配置运行时存储：设置页在线改采集配置后写回配置文件、替换内存快照，
	// 采集循环 / Fetcher 每页读最新值，即时生效。
	cfgStore := config.NewStore(cfg)
	// 第 3 层：业务服务，各自只注入需要的实体仓储
	healthSvc := service.NewHealthService(repos.Sources, repos.Videos, repos.Path())
	assetSvc := service.NewAssetService(repos.Assets)
	// 采集调度先构造：站点服务在清空数据前要问它「该站点是否还在采集」。
	crawlSvc := service.NewCrawlService(repos.Sources, repos.Videos, repos.Categories, repos.Tags, repos.Areas, repos.Jobs, cfg.Collector, cfgStore)
	sourceSvc := service.NewSourceService(repos.Sources, crawlSvc)
	// 站点配置订阅：按订阅地址拉取站点 JSON 并写进站点表，可手动刷新、也可按间隔自动刷新。
	subscriptionSvc := service.NewSubscriptionService(repos.Subscriptions, repos.Sources, cfg.Collector)
	settingsSvc := service.NewSettingsService(cfgStore, crawlSvc)
	// 启动钩子：自动恢复因进程重启而中断的采集任务（用户手动暂停的任务不动）。
	// 恢复是异步的，且失败只记日志、不阻断服务启动，避免单个坏记录拖垮整个服务。
	if recovered, err := crawlSvc.RecoverInterrupted(); err != nil {
		log.Printf("启动恢复中断任务失败（不影响服务启动）: %v", err)
	} else if recovered > 0 {
		log.Printf("启动完成：已自动恢复 %d 个因重启中断的采集任务", recovered)
	}
	videoSvc := service.NewVideoService(repos.Videos, repos.Sources)
	categorySvc := service.NewCategoryService(repos.Categories, repos.Videos)
	metaSvc := service.NewMetaService()
	jobSvc := service.NewJobService(repos.Jobs)
	// 采集记录页要显示运行中任务的实时进度，这里把调度层注入为进度来源。
	jobSvc.SetLiveSource(crawlSvc)
	// 定时增量采集：按站点上的 cron 配置，到点自动发起 incremental 任务。
	// 启动即挂上后台循环，Close 时先停它再关数据库。
	schedSvc := service.NewScheduleService(repos.Sources, crawlSvc)
	schedSvc.AttachSubscriptions(subscriptionSvc)
	schedSvc.Start()
	// 认证与站点授权：同一个服务同时支撑登录控制器与用户管理控制器。
	authSvc := service.NewAuthService(repos.Users, cfg.Auth)

	// 第 2 层：HTTP 控制器（注入对应服务）
	set := controller.Set{
		Health:       controller.NewHealthController(healthSvc),
		Asset:        controller.NewAssetController(assetSvc),
		Source:       controller.NewSourceController(sourceSvc),
		Subscription: controller.NewSubscriptionController(subscriptionSvc),
		Crawl:        controller.NewCrawlController(crawlSvc),
		Task:         controller.NewTaskController(crawlSvc),
		Video:        controller.NewVideoController(videoSvc),
		Category:     controller.NewCategoryController(categorySvc),
		Meta:         controller.NewMetaController(metaSvc),
		Job:          controller.NewJobController(jobSvc),
		Auth:         controller.NewAuthController(authSvc),
		Users:        controller.NewUserController(authSvc),
		Settings:     controller.NewSettingsController(settingsSvc),
		Page:         controller.NewPageController(cfg.Web.Dir),
	}

	// 第 1 层：路由
	return &App{Engine: router.New(set), repos: repos, crawl: crawlSvc, scheduler: schedSvc}, nil
}

// Close 先等异步采集任务收尾再释放数据库连接：
// 已完成的任务把统计/状态写完整，未跑完的留给下次启动自动恢复。
func (a *App) Close() error {
	// 先停调度器：否则它可能在数据库已关闭后还去读站点配置 / 写触发时间。
	if a.scheduler != nil {
		a.scheduler.Stop()
	}
	if a.crawl != nil {
		a.crawl.WaitForTasks(3 * time.Second)
	}
	return a.repos.Close()
}
