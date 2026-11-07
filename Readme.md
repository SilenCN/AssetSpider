# AssetSpider

视频站点采集工具。把**海洋CMS / 苹果CMS 系资源站的 JSON 采集接口**配置进数据库，按需执行**全量**或**增量**采集，结果去重后写入 SQLite，并提供 Vue 管理页面。

后端 Go + Gin + GORM/SQLite，前端 Vue 3 + Vite，构建产物由 Gin 直接托管。

## 技术栈

| 层 | 选型 |
| --- | --- |
| Web 框架 | Gin |
| ORM / 数据库 | GORM + SQLite（官方 CGO 驱动） |
| 前端 | Vue 3 + Vite |
| 配置 | `conf/config.yaml`（YAML） |
| 采集协议 | 海洋CMS / 苹果CMS `api.php/provide/vod/?ac=list` JSON |

## 环境要求

- Go 1.21+、gcc（SQLite 驱动依赖 CGO）
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

浏览器打开 http://localhost:8080 ，在「站点配置」里添加一个 JSON 接口，点「全量」即可采集入库。数据库默认创建在 `data/assetspider.db`（父目录自动创建）。

## 采集能力

### 支持的接口格式

标准海洋CMS / 苹果CMS 资源库 JSON 响应：

```json
{
  "code": 1, "msg": "数据列表",
  "page": 1, "pagecount": 3, "limit": "20", "total": 5,
  "list": [
    {
      "vod_id": 1001, "vod_name": "影片名", "type_id": 1, "type_name": "动作片",
      "vod_time": "2026-11-07 10:00:00", "vod_remarks": "HD",
      "vod_pic": "...", "vod_play_from": "youku", "vod_play_url": "正片$https://...",
      "vod_area": "大陆", "vod_lang": "国语", "vod_year": "2026",
      "vod_actor": "...", "vod_director": "...", "vod_content": "...",
      "vod_down_url": "...", "vod_en": "...", "vod_serial": "0"
    }
  ]
}
```

解析做了兼容：`vod_id` / `type_id` 等字段既可以是数字也可以是字符串，`limit` 可以是 `"20"`，`pagecount` 也接受 `page_count` 别名。

### 全量采集

按 `ac=list&pg=1..pagecount` 遍历所有分页（受 `collector.max_pages` 与站点 `maxPages` 限制），逐页入库。站点未返回总页数时按"拉到空页为止"处理。

### 增量采集

三道保障，避免漏采与重复：

1. **接口侧时间窗**：附加 `h=<小时数>` 参数，取「站点 `incrementalHours`」与「距上次采集经过的小时数 + 2」的较大者；
2. **本地时间游标**：站点表 `last_cursor` 记录已采到的最大 `vod_time`，增量时过滤掉游标之前的旧记录；
3. **入库去重**：`(source_id, vod_id)` 复合唯一索引 + 内容指纹，重复数据不产生新行，内容变化才更新。

> 海洋CMS 系站点更新影片会同步刷新 `vod_time`，因此游标过滤是安全的；若某站点不刷新 `vod_time`，可调大增量小时数或改用全量采集。

### 数据入库

`videos` 表保存影片名称、分类、图片、备注、播放/下载地址、演员、导演、简介、站点更新时间、内容指纹与原始 JSON，并记录首次/最近入库时间。每次采集生成一条 `crawl_jobs` 记录（模式、状态、页数、拉取/新增/更新/未变条数、耗时、错误信息），站点表回写上次采集状态与游标。

## 配置

`conf/config.yaml`：

```yaml
server:
  addr: ":8080"
database:
  path: "data/assetspider.db"   # 相对或绝对路径，可配置
web:
  dir: "web/dist"
collector:
  timeout_seconds: 20            # 单次接口请求超时
  max_pages: 1000                # 单次采集最大页数
  page_size: 0                   # 0 = 由站点决定；>0 附加 limit
  user_agent: "AssetSpider/1.0 (+seacms-json-collector)"
  default_incremental_hours: 24  # 站点未配置时的增量时间窗
  parallel: 1                    # 批量采集并发数，SQLite 建议保持 1
```

环境变量覆盖：`ASSETSPIDER_SERVER_ADDR`、`ASSETSPIDER_DB_PATH`、`ASSETSPIDER_WEB_DIR`，以及
`ASSETSPIDER_COLLECTOR_TIMEOUT` / `_MAX_PAGES` / `_PAGE_SIZE` / `_DEFAULT_HOURS` / `_PARALLEL` / `_USER_AGENT`。
`-config` 指定其他配置文件，未填写的字段沿用默认值。

### 站点配置字段（存在数据库 `sources` 表）

| 字段 | 说明 |
| --- | --- |
| `name` | 站点名称 |
| `apiBase` | JSON 接口完整地址，如 `https://site.com/api.php/provide/vod/` |
| `format` | 采集格式，目前仅 `seacms_json` |
| `extraParams` | 附加查询参数，如 `t=1&wd=电影`；可覆盖默认 `ac=list` |
| `enabled` | 是否参与「批量采集」 |
| `incrementalHours` | 增量时间窗（对应 `h`），0 表示使用全局默认值 |
| `maxPages` | 单次采集最大页数，0 表示使用全局值 |

`pg`、`h` 由采集器强制写入，不会被附加参数干扰。

## API

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/health` | 状态、数据库路径、站点数与影片数 |
| GET / POST | `/api/assets` | 通用资产台账（保留能力） |
| DELETE | `/api/assets/:id` | 删除资产 |
| GET | `/api/sources` | 站点列表，附带每个站点已入库影片数 |
| POST | `/api/sources` | 新增站点 |
| PUT | `/api/sources/:id` | 修改站点 |
| DELETE | `/api/sources/:id` | 删除站点，级联清理其影片与采集记录 |
| POST | `/api/sources/:id/crawl?mode=full\|incremental` | 单站点采集，同步返回结果摘要 |
| POST | `/api/crawl?mode=full\|incremental` | 对全部启用站点批量采集 |
| GET | `/api/videos?source_id=&q=&page=&size=` | 分页查询入库影片，支持名称搜索 |
| GET | `/api/jobs?source_id=&limit=` | 采集记录 |

`mode` 也可放在 JSON body：`{"mode":"incremental"}`；缺省为 `incremental`。示例：

```bash
curl -X POST http://localhost:8080/api/sources \
  -H 'Content-Type: application/json' \
  -d '{"name":"测试资源站","apiBase":"https://site.com/api.php/provide/vod/","enabled":true,"incrementalHours":24}'

curl -X POST 'http://localhost:8080/api/sources/1/crawl?mode=full'
curl 'http://localhost:8080/api/videos?page=1&size=20'
```

## 前端开发

```bash
cd web
npm install
npm run dev      # Vite 开发服务器，/api 代理到 :8080
npm run build    # 产物输出到 web/dist，由 Gin 托管
```

页面分三块：**站点配置**（增删改、启用开关、单站点全量/增量采集、一键批量采集）、**采集数据**（分页/搜索影片）、**采集记录**（每次采集的统计与状态）。`web/dist` 已随仓库提供，clone 后无需构建前端即可运行；未知前端路由由 Gin 回退到 `index.html`。

## 目录结构

```
.
├── conf/config.yaml          # 运行配置
├── internal/
│   ├── config/               # 配置加载与校验
│   ├── model/                # GORM 模型（Asset / Source / Video / CrawlJob）
│   ├── store/                # SQLite 数据访问与去重 upsert
│   ├── collector/            # JSON 接口解析、分页、全量/增量编排
│   └── server/               # Gin 路由、API 与静态托管
├── web/
│   ├── src/                  # Vue 3 源码
│   └── dist/                 # 构建产物（Gin 托管，随仓库提交）
├── main.go                   # 服务入口
├── go.mod / go.sum
└── Readme.md
```

## 测试

`go test ./...` 覆盖配置加载、站点/影片/任务 CRUD、去重 upsert、JSON 字段兼容解析、URL 拼装、全量/增量/游标/去重/失败回写，以及通过 HTTP 接口触发采集的完整链路。采集器测试用本地 HTTP 模拟站点，不依赖外网。

## 计划

- [ ] 支持 XML 格式采集接口
- [ ] 定时自动增量采集（cron）
- [ ] 采集结果导出（CSV / JSON）与站点分类绑定
