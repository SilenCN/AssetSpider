# AssetSpider

资产信息采集工具。后端 Go + Gin + GORM/SQLite，前端 Vue 3，构建产物由 Gin 直接托管。

## 技术栈

| 层 | 选型 |
| --- | --- |
| Web 框架 | Gin |
| ORM / 数据库 | GORM + SQLite（官方 CGO 驱动） |
| 前端 | Vue 3 + Vite |
| 配置 | `conf/config.yaml`（YAML） |

## 环境要求

- Go 1.21+、gcc（SQLite 驱动依赖 CGO）
- Node.js 18+（仅重新构建前端时需要）

## 快速开始

```bash
go mod download
go run .                          # 默认读取 conf/config.yaml，监听 :8080
go run . -config conf/config.yaml  # 指定配置文件
go run . -version
go test ./...
go build -o bin/assetspider .
```

浏览器打开 http://localhost:8080 。数据库默认创建在 `data/assetspider.db`（父目录自动创建）。

## 配置

`conf/config.yaml`：

```yaml
server:
  addr: ":8080"
database:
  path: "data/assetspider.db"
web:
  dir: "web/dist"
```

- `database.path` 支持相对路径或绝对路径，改这里即可更换数据库位置；
- 环境变量覆盖：`ASSETSPIDER_SERVER_ADDR`、`ASSETSPIDER_DB_PATH`、`ASSETSPIDER_WEB_DIR`；
- `-config` 指定其他配置文件，未填写的字段沿用默认值。

## API

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/health` | 健康检查，返回当前数据库路径 |
| GET | `/api/assets` | 资产列表 |
| POST | `/api/assets` | 新增资产 `{"host":"...","ip":"...","category":"..."}` |
| DELETE | `/api/assets/:id` | 删除资产 |

## 前端开发

```bash
cd web
npm install
npm run dev      # Vite 开发服务器，/api 代理到 :8080
npm run build    # 产物输出到 web/dist，由 Gin 托管
```

`web/dist` 已随仓库提供，后端无需先构建前端即可托管页面；未知前端路由由 Gin 回退到 `index.html`。

## 目录结构

```
.
├── conf/config.yaml        # 运行配置
├── internal/
│   ├── config/             # 配置加载与校验
│   ├── model/              # GORM 模型
│   ├── store/              # SQLite 数据访问
│   └── server/             # Gin 路由与静态托管
├── web/                    # Vue 3 源码
│   ├── src/
│   └── dist/               # 构建产物（Gin 托管）
├── main.go                 # 服务入口
├── go.mod / go.sum
└── Readme.md
```

## 计划

- [ ] 明确采集目标与数据源
- [ ] 实现采集与去重逻辑，结果写入 SQLite
- [ ] 补充结果导出与使用文档
