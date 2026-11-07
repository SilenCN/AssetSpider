// AssetSpider 是资产信息采集工具的命令行入口。
package main

import (
	"flag"
	"fmt"
	"os"
)

// version 是当前版本号，发布时按需更新。
const version = "0.1.0"

func main() {
	target := flag.String("target", "", "采集目标，例如域名或 IP 段")
	showVersion := flag.Bool("version", false, "输出版本号")
	flag.Parse()

	if *showVersion {
		fmt.Println(banner(""))
		return
	}
	if *target == "" {
		fmt.Fprintln(os.Stderr, "请通过 -target 指定采集目标")
		flag.Usage()
		os.Exit(2)
	}

	fmt.Println(banner(*target))
	// TODO: 在这里接入实际的采集逻辑
}

// banner 返回启动提示信息，便于在采集逻辑尚未实现时验证程序运行。
func banner(target string) string {
	if target == "" {
		return fmt.Sprintf("AssetSpider v%s", version)
	}
	return fmt.Sprintf("AssetSpider v%s | target: %s (采集逻辑待实现)", version, target)
}
