// AssetSpider 是资产信息采集工具的服务入口。
package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/server"
	"github.com/SilenCN/AssetSpider/internal/store"
)

const version = "0.2.0"

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
	st, err := store.Open(cfg.Database.Path)
	if err != nil {
		log.Fatalf("初始化数据库失败: %v", err)
	}
	defer st.Close()

	log.Printf("AssetSpider v%s 启动 | 监听 %s | 数据库 %s | 页面 %s",
		version, cfg.Server.Addr, cfg.Database.Path, cfg.Web.Dir)
	if err := server.New(st, cfg.Web.Dir).Router().Run(cfg.Server.Addr); err != nil {
		log.Fatalf("服务启动失败: %v", err)
	}
}

// banner 返回版本信息。
func banner() string {
	return fmt.Sprintf("AssetSpider v%s", version)
}
