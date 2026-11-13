// AssetSpider 是资产信息采集工具的服务入口。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/SilenCN/AssetSpider/internal/app"
	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/utils"
)

const version = "0.3.0"

// shutdownTimeout 是停止 HTTP 服务时等待在途请求的上限；超时后仍继续收尾，
// 避免个别慢请求把重启拖死。
const shutdownTimeout = 10 * time.Second

func main() {
	configPath := flag.String("config", "conf/config.yaml", "配置文件路径")
	showVersion := flag.Bool("version", false, "输出版本号")
	flag.Parse()

	if *showVersion {
		fmt.Println(banner())
		return
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	application, err := app.New(cfg)
	if err != nil {
		log.Fatalf("%v", err)
	}

	log.Printf("AssetSpider v%s 启动 | 监听 %s | 数据库 %s(%s) | 页面 %s | 采集超时 %ds / 最大 %d 页",
		version, cfg.Server.Addr, cfg.Database.Driver, cfg.Database.Display(), cfg.Web.Dir,
		cfg.Collector.TimeoutSeconds, cfg.Collector.MaxPages)

	// 显式持有 http.Server：优雅停止要调 Shutdown，而 Engine.Run 不暴露这个入口。
	srv := &http.Server{Addr: cfg.Server.Addr, Handler: application.Engine}
	serveErr := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	// 设置页的「重启服务」与系统信号走同一条收尾路径，区别只在于最后是否重建进程。
	restart := false
	select {
	case err := <-serveErr:
		log.Printf("服务启动失败: %v", err)
		closeApp(application)
		return
	case got := <-signals:
		log.Printf("收到信号 %v，开始优雅停止", got)
	case <-application.RestartRequested():
		restart = true
		log.Printf("收到重启请求，开始优雅停止")
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("HTTP 优雅停止超时: %v", err)
	}
	closeApp(application)

	if !restart {
		return
	}
	// 原地重建：Exec 成功则不返回；失败说明无法自重启，退出进程交给外部监督者拉起。
	if err := utils.RestartProcess(); err != nil {
		log.Printf("在线重启失败: %v（退出进程，交由 systemd 重新拉起）", err)
		os.Exit(1)
	}
}

// closeApp 释放连接与后台任务；收尾失败只记日志，不能阻断后面的进程重建。
func closeApp(application *app.App) {
	if err := application.Close(); err != nil {
		log.Printf("释放资源失败: %v", err)
	}
}

// banner 返回版本信息。
func banner() string {
	return fmt.Sprintf("AssetSpider v%s", version)
}
