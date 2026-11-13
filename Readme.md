# AssetSpider

视频站点采集工具。把**苹果CMS V10 资源站的 JSON 采集接口**配置进数据库，按需执行**全量**或**增量**采集，把接口返回的**全部字段**去重后写入 SQLite，并按苹果CMS 字段解析出**播放组**，提供 Vue 管理页面。

后端 Go + Gin + GORM/SQLite，前端 Vue 3 + Vite，构建产物由 Gin 直接托管。

## 技术栈

| 层 | 选型 |
| --- | --- |
| Web 框架 | Gin |
| ORM / 数据库 | GORM + SQLite（纯 Go 驱动 modernc.org/sqlite，无 CGO） |
| 前端 | Vue 3 + Vite + hls.js（m3u8 播放，按需动态加载） |
| 配置 | `conf/config.yaml`（YAML） |
| 认证 | bcrypt 密码哈希 + 随机令牌（仅存 SHA-256 摘要）+ Gin 中间件 |
| 采集协议 | 苹果CMS V10 Provide 接口 `api.php/provide/vod/?ac=videolist` JSON |

## 环境要求

- Go 1.21+（SQLite 为纯 Go 驱动，无需 gcc / CGO）
- Node.js 18+（仅重新构建前端时需要）

## 快速开始

```bash
go mod download
go run .                           # 默认读取 conf/config.yaml，监听 :8080
go run . -config conf/config.yaml  # 指定配置文件
go run . -version
go test ./...
go build -o bin/assetspider .
```

浏览器打开 http://localhost:8080 ：**第一个登录的账号会自动注册为管理员**（用户名密码自行设定，无需预先建号），之后可在「用户管理」里创建普通用户并勾选其可见站点。管理员在「站点配置」里添加 JSON 接口，点「全量」即可采集入库。数据库默认创建在 `data/assetspider.db`（父目录自动创建）。

> 生产环境：push 到 `main` 会自动部署到服务器并启动服务，详见 [部署](docs/deployment.md)。

## 能力概览

| 能力 | 说明 | 详见 |
| --- | --- | --- |
| 采集 | 苹果CMS JSON 全字段零丢失入库：全量 / 增量、断点续采、并发队列、失败重试、站点级定时增量 | [采集能力](docs/collecting.md) |
| 归档 | 分类（两级树）、标签、地域随采集自动建立与关联，任务级类目缓存 + 批量 upsert 避免逐页重复查库写入；影片上的年份等原始字段保留，这些维度仅作信息展示，不再参与资源库筛选 | [采集能力](docs/collecting.md) |
| 播放 | 播放组解析为线路 / 剧集，m3u8（hls.js）/ 直链 / 解析页 iframe 三种承载方式 | [采集能力](docs/collecting.md) |
| 站点 | 手工添加、JSON 导入 / 导出，以及**网络订阅**：填一个远程 JSON 地址，按间隔自动刷新或手动刷新 | [配置](docs/configuration.md) |
| 页面 | 资源库、站点配置、视频分类、采集记录、系统设置、用户管理（角色与站点白名单） | [前端](docs/frontend.md) |
| 运维 | 系统设置在线热更并写回 YAML；推送 `main` 自动部署，健康检查失败自动回滚 | [配置](docs/configuration.md) · [部署](docs/deployment.md) |

## 文档

| 文档 | 内容 |
| --- | --- |
| [架构与目录](docs/architecture.md) | 四层架构与依赖约束、仓储拆分、批量写入策略、完整目录结构 |
| [采集能力](docs/collecting.md) | 接口格式、全字段保留、全量 / 增量、任务调度、分类 / 标签 / 地域归档、播放组解析与地址改写 |
| [配置](docs/configuration.md) | 系统设置热更新、`conf/config.yaml`、站点字段、JSON 导入导出、检索索引、清空站点数据 |
| [API](docs/api.md) | 登录与权限模型、全部接口与调用示例 |
| [前端](docs/frontend.md) | 页面结构、交互约定、样式变量与移动端适配 |
| [测试](docs/testing.md) | 测试覆盖范围与运行方式 |
| [部署](docs/deployment.md) | GitHub Actions 自动部署、密钥与变量、首次部署、日常运维、故障排查 |

## 计划

- [ ] 支持 XML 格式采集接口
- [ ] 采集结果导出（CSV / JSON）与站点分类绑定
