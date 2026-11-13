// AssetSpider 是资产信息采集工具的服务入口。
package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/SilenCN/AssetSpider/internal/app"
	"github.com/SilenCN/AssetSpider/internal/config"
)

const version = "0.3.0"

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
	defer application.Close()

	log.Printf("AssetSpider v%s 启动 | 监听 %s | 数据库 %s(%s) | 页面 %s | 采集超时 %ds / 最大 %d 页",
		version, cfg.Server.Addr, cfg.Database.Driver, cfg.Database.Display(), cfg.Web.Dir,
		cfg.Collector.TimeoutSeconds, cfg.Collector.MaxPages)
	// 不用 Fatalf：保证上面的 defer 能正常关闭数据库。
	if err := application.Engine.Run(cfg.Server.Addr); err != nil {
		log.Printf("服务启动失败: %v", err)
	}
}

// banner 返回版本信息。
func banner() string {
	return fmt.Sprintf("AssetSpider v%s", version)
}
