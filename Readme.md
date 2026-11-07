# AssetSpider

资产信息采集工具，基于 Go 开发，使用 Go Modules 管理依赖。

## 环境要求

- Go 1.21 或更高版本

## 快速开始

```bash
go mod download                 # 拉取依赖（当前无第三方依赖）
go run . -version               # 查看版本号
go run . -target example.com    # 指定采集目标
go build -o bin/assetspider .   # 构建可执行文件
go test ./...                   # 运行测试
```

## 目录结构

```
.
├── go.mod          # 模块定义
├── main.go         # CLI 入口
├── main_test.go    # 基础测试
├── .gitignore      # 忽略规则
└── Readme.md
```

## 当前状态

Go 模块骨架与 CLI 入口已就绪，采集逻辑待实现。

## 计划

- [ ] 明确采集目标与数据源
- [ ] 实现采集与去重逻辑
- [ ] 补充结果导出与使用文档
