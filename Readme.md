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
| 采集协议 | 苹果CMS V10 Provide 接口 `api.php/provide/vod/?ac=videolist` JSON |

## 四层架构

服务按严格的四层组织，**只能自顶向下调用紧邻的一层，不允许跨层**：

```
router  →  controller  →  service  →  repository
```

| 层 | 包 | 职责 | 允许依赖 |
| --- | --- | --- | --- |
| 1 路由 | `internal/router` | 注册 URL → handler、静态资源与 SPA 回退 | 仅 `controller` |
| 2 控制器 | `internal/controller` | 绑定请求参数、调用服务、把结果/错误翻译成 HTTP 响应 | 仅 `service` |
| 3 服务 | `internal/service` | 业务校验与默认值、采集编排、游标与去重、错误语义 | `repository`、`model`、`config`、本层子包 `service/collector` |
| 4 仓储 | `internal/repository` | 每个实体一个文件、一个对象、一个接口；GORM/SQLite 读写、事务与级联清理 | 仅 `model` |

仓储层按实体严格拆分，不允许把不同实体的方法揉进同一个对象：

| 实体 | 文件 | 对象 / 对外接口 | 方法 |
| --- | --- | --- | --- |
| `Asset` | `repository/asset.go` | `AssetRepository` | `List` / `Create` / `Delete` |
| `Source` | `repository/source.go` | `SourceRepository` | 站点 CRUD、`ListEnabledSources`、`UpdateSourceResult`、级联删除 |
| `Video` | `repository/video.go` | `VideoRepository` | `UpsertVideos`、`GetVideo`、`ListVideos`、`CountVideosBySource` / `ByCategory` |
| `Category` | `repository/category.go` | `CategoryRepository` | `EnsureForVideos`、`List`、`Get` |
| `CrawlJob` | `repository/job.go` | `JobRepository` | `CreateJob` / `FinishJob` / `ListJobs` |

`repository/store.go` 只是容器：`Repositories` 共享一条 `*gorm.DB`，负责连接、自动迁移、装配四个对象与 `Path` / `Close`，**不承载任何实体读写方法**；`errors.go` 放各对象共用的 `ErrNotFound`。测试同样按实体拆为 `asset_test.go` / `source_test.go` / `video_test.go` / `job_test.go`，`store_test.go` 只测连接与装配。

约束的具体落地：

- 每个 service 只注入自己需要的实体仓储**接口**（如 `AssetService` 拿 `AssetRepository`，`CrawlService` 拿 `SourceRepository` + `VideoRepository` + `JobRepository`），看不到 `*gorm.DB`；GORM 的 `ErrRecordNotFound` 在仓储层翻译为 `repository.ErrNotFound`，服务层再翻译为 `ErrInvalid / ErrNotFound / ErrConflict` 三个哨兵错误，控制器据此决定 400 / 404 / 409 / 500（采集接口默认 502）。
- 采集协议客户端放在 `internal/service/collector`（service 层内部子包），只做 HTTP 抓取、JSON 零丢失解析与实体转换；原 `collector.Runner` 的编排逻辑（分页、去重入库、任务与游标回写）已上移到 `CrawlService`，不再直接访问数据库。
- `internal/model`（实体）、`internal/config`（配置）、`internal/utils`（工具）是横切包，不属于四层，也不参与调用链；`internal/app` 是依赖装配层（composition root），只负责自底向上 `new` 出各层对象并注入，不发起任何业务调用；`main.go` 只做配置加载、装配与启动。
- `internal/utils` 是新增的 utils 层，统一存放与业务无关的纯工具：`request.go`（路径/query 参数解析）、`json.go`（JSON 值字符串化）、`time.go`（时间归一化）、`hash.go`（内容指纹）、`env.go`（整数环境变量）。它可被任意一层引用，但自身**不允许依赖任何 `internal/*` 业务包**（由架构测试强制）。
- 根目录 `architecture_test.go` 会解析四层生产代码的 import，自动校验依赖方向、utils 的横切纯洁性、仓储层「一实体一对象」，以及「Gin 不出现在 service/repository、GORM 不出现在 service/controller/router」；`internal/router/router_test.go` 还会遍历 Gin 路由表，强制 `/api/{模块名}/{版本号}` 结构且路径里不含动词。任何违规都会让 `go test ./...` 失败。

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

浏览器打开 http://localhost:8080 ，在「站点配置」里添加一个 JSON 接口，点「全量」即可采集入库。数据库默认创建在 `data/assetspider.db`（父目录自动创建）。

## 采集能力

### 支持的接口格式

标准苹果CMS V10 资源库 JSON 响应（Provide 接口 `ac=videolist`）：

```json
{
  "code": 1, "msg": "数据列表",
  "page": 1, "pagecount": 3, "limit": "20", "total": 5,
  "list": [
    {
      "vod_id": 1001, "vod_name": "影片名", "type_id": 1, "type_name": "动作片",
      "vod_time": "2026-11-07 10:00:00", "vod_remarks": "HD",
      "vod_pic": "...",
      "vod_play_from": "youku$$$qq",
      "vod_play_server": "$$$",
      "vod_play_note": "$$$",
      "vod_play_url": "第1集$https://youku/1#第2集$https://youku/2$$$第1集$https://qq/1#第2集$https://qq/2",
      "vod_down_url": "...", "vod_actor": "...", "vod_content": "...",
      "vod_score": "8.5", "vod_custom_flag": "站点私有字段"
    }
  ]
}
```

解析做了兼容：`vod_id` / `type_id` 等字段既可以是数字也可以是字符串，`limit` 可以是 `"20"`，`pagecount` 也接受 `page_count` 别名；`null` 转空串，对象与数组按紧凑 JSON 文本保留。

接口返回 `code != 1`（如 0 / -1 / -2）视为失败，直接报错并停止解析；只有 `code == 1` 才进入逐条处理（无 `ac` 时默认按 `ac=videolist` 请求，即返回含 `vod_play_url` 的完整字段）。

### 全字段保留（重点）

资源站的字段因版本、二次开发、自定义模板差异很大，因此**不使用固定结构体反序列化**（那会静默丢弃未定义字段）。每条 `list[]` 记录在解析时保留两份数据：

| 层 | 落地位置 | 说明 |
| --- | --- | --- |
| 结构化标准列 | `videos` 表的 91 个 `vod_*` 列 | 依据 MacCMS V10 官方「视频字段」表，覆盖基础信息、图片、演职、上映、属性、统计、时间、来源、密码、播放/下载、分集剧情等 14 组；其中播放组相关列（`vod_play_from` / `vod_play_server` / `vod_play_note` / `vod_play_url` / `vod_play_list`）用于承载苹果CMS 的多线路播放数据，便于 SQL 查询与前端展示 |
| 扩展字段 | `videos.extra` | 接口返回但不在标准清单内的字段（站点自定义字段、嵌套对象、数组等），按**原始 JSON 值**保存 |

> 不再保存整条记录的原始 JSON 全文（原 `videos.raw` 列）：标准列 + `extra` 已覆盖全部字段，再存一份全文只会重复占用存储，因此该列已移除。

字段清单 `internal/model/video_fields.go` 的 `VodFields` 是唯一数据源：采集映射、入库更新、字段元数据接口、前端详情展示全部由它驱动，新增字段只需追加清单 + 实体字段。`model` 包的一致性测试会校验清单与实体、列名、json tag 严格对应，避免改动时漏字段。

内容指纹由该条记录的**全部键值**（含扩展字段）排序后计算，因此任何字段变化都会触发更新；仅字段顺序或空白变化不会误判。更新时用全字段 map 写回，接口把某字段清空也能正确同步。

### 全量采集

按 `ac=videolist&pg=1..pagecount` 遍历所有分页（受 `collector.max_pages` 与站点 `maxPages` 限制），逐页入库。站点未返回总页数时按"拉到空页为止"处理。

### 增量采集

三道保障，避免漏采与重复：

1. **接口侧时间窗**：附加 `h=<小时数>` 参数，取「站点 `incrementalHours`」与「距上次采集经过的小时数 + 2」的较大者；
2. **本地时间游标**：站点表 `last_cursor` 记录已采到的最大 `vod_time`，增量时过滤掉游标之前的旧记录；
3. **入库去重**：`(source_id, vod_id)` 复合唯一索引 + 全字段内容指纹，重复数据不产生新行，内容变化才更新。

> 苹果CMS 系站点更新影片会同步刷新 `vod_time`，因此游标过滤是安全的；若某站点不刷新 `vod_time`，可调大增量小时数或改用全量采集。

### 采集任务：并发上限、暂停 / 继续、失败重试与重新运行

采集以**异步任务**运行：发起接口立即返回任务号，抓取在后台排队执行，进度通过任务接口轮询（页面每 1.5 秒刷新一次）。

- **并发上限**：`collector.max_concurrent`（默认 3，环境变量 `ASSETSPIDER_COLLECTOR_MAX_CONCURRENT`）限制「同时运行的任务数」，超出的任务进入队列（状态 `queued`），空出槽位后自动补位；运行期可用 `PUT /api/tasks/v1/capacity` 实时调整，重启后回到配置文件的值。
- **同站点互斥**：一个站点同一时刻只允许一个任务（重复发起返回 409），不同站点可并行。
- **手动暂停**：`PUT /api/tasks/v1/{jobId}` + `{"status":"paused"}`。暂停是协作式的——在页边界生效并中断正在进行的 HTTP 请求，**已完成的整页数据不会回滚**，也不会写入半页数据；**「等待自动重试」期间同样可以暂停**，不必等退避结束。
- **进度保存**：全量采集把「下一次从第几页开始」写入 `crawl_jobs.resume_page`；增量采集照常推进 `last_cursor`。因此暂停不是失败，站点与记录状态都是 `paused`。
- **继续**：`PUT /api/tasks/v1/{jobId}` + `{"status":"running"}`，按原站点、原模式重新入队：全量从 `resume_page` 续采，增量从游标继续，不会从头再拉一遍。
- **失败自动重试**：抓取/入库失败后按指数退避自动重试，次数由 `collector.retry_times`（默认 2，即最多尝试 3 次）、首次间隔由 `collector.retry_backoff_seconds`（默认 3 秒，之后翻倍、上限 30 秒）决定，设 `0` 可关闭。重试**从上次失败的那一页**继续，已完成页面不会重复抓取；每次尝试都记在 `crawl_jobs.attempt`。等待重试期间任务视图带 `retryInSeconds` 倒计时与失败原因。
- **手动重试**：`POST /api/tasks/v1/{jobId}/retry`，仅对 `failed` 的任务有效，从失败页续采并记录来源任务（`crawl_jobs.retry_of`）。
- **重新运行**：`POST /api/tasks/v1/{jobId}/rerun`，对任何已结束（`failed` / `success` / `paused`）的任务有效，**忽略断点从第 1 页重来**；仍在运行的任务返回 409。
- **实时进度**：`GET /api/tasks/v1` 的每个任务带 `page` / `pageCount` / `pages` / `percent`（-1 表示总量未知，前端展示不定态进度条）/ `itemsPerSecond` / `etaSeconds` / `attempt` / `maxAttempts` / `retryInSeconds` / `message`；采集记录接口对运行中的记录额外带 `live` 字段，因此「采集记录」页也能看到同一条实时进度。
- **重启清理**：任务调度只存在于进程内存，服务启动时会把遗留的 `running` / `queued` 记录标记为中断（`paused` 的任务保留，仍可继续）。

### 自动视频分类

采集到视频资源时，按其自带的 `type_id` / `type_name` **自动创建分类并建立关联**，无需手工维护：

- 分类键由 `utils.CategoryKey` 归一化：有 `type_id` 用 `id:<type_id>`，站点没给（或为 `0`）时回退为 `name:<分类名>`；两者都没有则该条影片不归类，也不产生空分类。
- **每个资源站一套分类**：`categories` 表用 `(source_id, cat_key)` 复合唯一索引，不同站点即使返回完全相同的 `type_id` 也各建各的，互不串扰。
- 每页入库前先在一个事务里做「有则复用、无则创建」，同一 `type_id` 改名时只更新名称；再把分类主键回填到 `Video.CategoryID` 一起写库。内容指纹未变的历史影片也会补齐分类关联，因此**对老库执行一次全量采集即可完成历史数据归档**。
- 删除站点时，其分类随影片和采集记录一起级联清理。

### 二级分类（按苹果CMS type_pid 模型）

苹果CMS 的分类支持两级（一级 / 二级），用 `type_pid`（父级分类 ID）表达层级。采集时：

- 视频的 `type_id_1` 即其所属**一级分类 ID**（苹果CMS 的 level-1 字段），`type_id` 是（通常是二级的）具体分类 ID；`type_id_1` 为空或与 `type_id` 相同则视为一级分类。
- 每批入库会先把一级分类、`type_pid` 解析出来：二级分类通过 `type_pid` 挂到对应一级分类之下，并标记 `level`（1=一级 / 2=二级）。
- **父级暂未采集到也安全**：若某批只采到二级（其 `type_pid` 指向的一级分类尚未出现），该分类先记为二级、父级留空；待该一级分类随后续采集出现后，会自动回挂到正确父级，不会丢层级。
- `categories` 表新增 `type_pid` / `parent_id` / `level` 三列（由 GORM 自动迁移），现有单级分类升级后默认 `level=1`、无父级，向后兼容。

### 播放组解析（苹果CMS 字段）

苹果CMS 用「播放组」表达同一影片在多个线路上的播放地址，靠三个字段按固定分隔符拼接：

- `vod_play_from`：各组线路名，用 `$$$` 分隔，如 `"youku$$$qq$$$iqiyi"`；
- `vod_play_server` / `vod_play_note`：各组对应的服务器与备注，同样用 `$$$` 分隔（可留空）；
- `vod_play_url`：各组播放地址，用 `$$$` 分隔；**组内**每集用 `#` 分隔，每集格式为 `集名$地址`（无 `$` 时整段作为地址、集名为空）。

解析后落库为一个结构化 JSON 数组，存进已有的 `vod_play_list` 列（不新增列）：

```json
[
  {"from":"youku","server":"","note":"","episodes":[{"name":"第1集","url":"https://youku/1"},{"name":"第2集","url":"https://youku/2"}]},
  {"from":"qq","server":"","note":"","episodes":[{"name":"第1集","url":"https://qq/1"},{"name":"第2集","url":"https://qq/2"}]}
]
```

规则要点：

- 只有 `vod_play_from` 时视为单线路，命名为「默认」；多线路时第 `i` 组缺失名称则命名为「线路N」。
- 解析出任一集才保留该组，全空则不落库（回退尝试原 `vod_play_list` 文本）。
- 单独接口 `GET /api/videos/v1/:id/play-groups` 返回该影片的播放组数组，便于前端独立展示多线路选集。

### 数据入库

`videos` 表保存全部标准字段、扩展字段、分类关联、内容指纹与首次/最近入库时间（不再冗余保存整条原始 JSON）。每次采集生成一条 `crawl_jobs` 记录（模式、状态、页数、拉取/新增/更新/未变条数、耗时、错误信息、续采页码 `resume_page`、尝试次数 `attempt`、重试来源 `retry_of`），站点表回写上次采集状态与游标。

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
  user_agent: "AssetSpider/1.0 (+maccms-json-collector)"
  default_incremental_hours: 24  # 站点未配置时的增量时间窗
  max_concurrent: 3              # 同时运行的采集任务上限，超出的排队；可在页面/接口运行时调整
  retry_times: 2                 # 失败后自动重试次数（0 = 不重试），从失败页续采
  retry_backoff_seconds: 3       # 首次重试等待秒数，之后翻倍，上限 30 秒
```

环境变量覆盖：`ASSETSPIDER_SERVER_ADDR`、`ASSETSPIDER_DB_PATH`、`ASSETSPIDER_WEB_DIR`，以及
`ASSETSPIDER_COLLECTOR_TIMEOUT` / `_MAX_PAGES` / `_PAGE_SIZE` / `_DEFAULT_HOURS` / `_MAX_CONCURRENT` / `_RETRY_TIMES` / `_RETRY_BACKOFF` / `_USER_AGENT`
（旧变量名 `_PARALLEL` 仍兼容，等价于 `_MAX_CONCURRENT`）。
`-config` 指定其他配置文件，未填写的字段沿用默认值。

### 站点配置字段（存在数据库 `sources` 表）

| 字段 | 说明 |
| --- | --- |
| `name` | 站点名称 |
| `apiBase` | JSON 接口完整地址，如 `https://site.com/api.php/provide/vod/` |
| `format` | 采集格式，现支持 `maccms_json`（苹果CMS V10 Provide，默认）/ `seacms_json`（海洋CMS） |
| `extraParams` | 附加查询参数，如 `t=1&wd=电影`；可覆盖默认 `ac=videolist` |
| `enabled` | 是否参与「批量采集」 |
| `incrementalHours` | 增量时间窗（对应 `h`），0 表示使用全局默认值 |
| `maxPages` | 单次采集最大页数，0 表示使用全局值 |

`pg`、`h` 由采集器强制写入，不会被附加参数干扰。

## API

接口统一按 **`/api/{模块名}/{版本号}`** 组织，当前版本 `v1`（`internal/router.APIVersion`）。模块名一律是复数资源名词，操作由 HTTP 动词表达，路径中不含动词；升级时新增 `registerV2` 分组即可，旧版本保持可用。

| 模块 | 方法 | 路径 | 说明 |
| --- | --- | --- | --- |
| system | GET | `/api/system/v1/health` | 状态、数据库路径、站点数与影片数 |
| assets | GET / POST | `/api/assets/v1` | 通用资产台账（保留能力） |
| assets | DELETE | `/api/assets/v1/:id` | 删除资产 |
| sources | GET | `/api/sources/v1` | 站点列表，附带每个站点已入库影片数 |
| sources | POST | `/api/sources/v1` | 新增站点 |
| sources | PUT | `/api/sources/v1/:id` | 全量更新站点 |
| sources | DELETE | `/api/sources/v1/:id` | 删除站点，级联清理其影片与采集记录 |
| sources | POST | `/api/sources/v1/:id/crawls` | 为该站点执行一次采集，**同步**返回结果摘要（脚本/命令行用） |
| crawls | POST | `/api/crawls/v1` | 对全部启用站点批量发起**同步**采集（超上限的排队） |
| tasks | GET | `/api/tasks/v1` | 采集任务列表：`capacity`（`maxConcurrent` / `running` / `queued`）+ `items[]`（排队中、运行中、暂停中，含实时进度） |
| tasks | POST | `/api/tasks/v1` | **异步**发起采集任务，body `{sourceId, mode}`，返回 `202` 与任务视图（`jobId`） |
| tasks | PUT | `/api/tasks/v1/:id` | 更新任务状态：`{"status":"paused"}` 暂停 / `{"status":"running"}` 继续（`:id` 为 `jobId`） |
| tasks | POST | `/api/tasks/v1/:id/retry` | 手动重试**失败**的任务：从失败页续采，返回 `202` 与新任务视图（非 `failed` 返回 409） |
| tasks | POST | `/api/tasks/v1/:id/rerun` | **重新运行**已结束的任务：忽略断点从第 1 页重来，返回 `202` 与新任务视图（运行中返回 409） |
| tasks | GET | `/api/tasks/v1/capacity` | 读取并发容量 |
| tasks | PUT | `/api/tasks/v1/capacity` | 调整同时运行的任务数：`{"maxConcurrent":2}`（1..64） |
| videos | GET | `/api/videos/v1?source_id=&category_id=&area=&q=&sort=&page=&size=` | 分页查询入库影片（返回全部字段、extra、categoryId）。`q` 匹配名称/别名/演员/导演/分类/地域；`category_id` 按自动归档的分类过滤（含子分类）；`area` 按地域模糊筛选；`sort` 取值 `time_desc`（更新时间↓）/`time_asc`/`score_desc`（评分↓）/`score_asc`，空则为 id 倒序 |
| videos | GET | `/api/videos/v1/areas` | 库中出现过的地域去重列表，供前端筛选下拉 |
| videos | GET | `/api/videos/v1/:id` | 单条影片完整记录 |
| videos | GET | `/api/videos/v1/:id/play-groups` | 该影片的播放组数组（来自苹果CMS 播放组字段，已解析为结构化 JSON） |
| categories | GET | `/api/categories/v1?source_id=` | 视频分类列表，含 `videoCount`；`source_id` 只看该资源站的一套分类 |
| categories | GET | `/api/categories/v1/tree?source_id=` | 视频分类二级树（父分类在前、子分类嵌套），按苹果CMS type_pid 模型 |
| categories | GET | `/api/categories/v1/:id` | 单个分类详情 |
| meta | GET | `/api/meta/v1/fields` | 全字段元数据：`total`、`groups`、`fields[]`（`key` 原始键、`json` 前端键、`desc` 中文含义、`group`、`label`、`long`） |
| jobs | GET | `/api/jobs/v1?source_id=&limit=` | 采集记录（含 `queued` / `running` / `paused` / `success` / `failed` 与续采页码 `resumePage`） |

采集任务用 `POST` 创建，参数 `mode`（`full` / `incremental`，缺省 `incremental`）放在 **JSON body**，同时也兼容 query：

```bash
curl -X POST http://localhost:8080/api/sources/v1 \
  -H 'Content-Type: application/json' \
  -d '{"name":"测试资源站","apiBase":"https://site.com/api.php/provide/vod/","enabled":true,"incrementalHours":24}'

# 异步发起（推荐，前端即走这条）：立即返回任务号
curl -X POST http://localhost:8080/api/tasks/v1 \
  -H 'Content-Type: application/json' -d '{"sourceId":1,"mode":"full"}'
# 查看进度 / 并发容量
curl http://localhost:8080/api/tasks/v1
# 暂停（进度会保存）与继续（从断点续采）
curl -X PUT http://localhost:8080/api/tasks/v1/1 -H 'Content-Type: application/json' -d '{"status":"paused"}'
curl -X PUT http://localhost:8080/api/tasks/v1/1 -H 'Content-Type: application/json' -d '{"status":"running"}'
# 最多同时运行 2 个任务
curl -X PUT http://localhost:8080/api/tasks/v1/capacity -H 'Content-Type: application/json' -d '{"maxConcurrent":2}'
# 失败任务手动重试（从失败页续采）/ 已结束任务重新运行（从第 1 页）
curl -X POST http://localhost:8080/api/tasks/v1/3/retry
curl -X POST http://localhost:8080/api/tasks/v1/3/rerun

# 同步采集（脚本用，阻塞到任务结束）
curl -X POST http://localhost:8080/api/sources/v1/1/crawls \
  -H 'Content-Type: application/json' -d '{"mode":"full"}'
curl -X POST http://localhost:8080/api/crawls/v1 \
  -H 'Content-Type: application/json' -d '{"mode":"incremental"}'
curl 'http://localhost:8080/api/videos/v1?page=1&size=20'
curl 'http://localhost:8080/api/categories/v1'
curl 'http://localhost:8080/api/videos/v1?category_id=1'
curl 'http://localhost:8080/api/meta/v1/fields'
```

## 前端开发

```bash
cd web
npm install
npm run dev      # Vite 开发服务器，/api 代理到 :8080
npm run build    # 产物输出到 web/dist，由 Gin 托管
```

页面分四块：**站点配置**（增删改、启用开关；顶部「采集任务」卡片显示并发上限、运行/排队数，以及每个任务的**实时进度条**——百分比、当前页/总页数、已采条数、速率、预估剩余时间、第几次尝试、等待重试倒计时；运行中可**暂停**、已暂停可**继续**，一键批量采集全部站点；站点行内也可直接发起全量/增量或暂停）、**资源库**（采集到的影片以海报卡片展示，每张卡片显示**资源名 + 海报图 + 更新集数**，图片加载失败自动退化占位；顶部可按**视频名称**检索、按**分类 / 地域 / 站点**筛选、按**更新时间**或**评分**升降序排序，分页浏览）、**视频分类**（按资源站列出自动生成的分类、站点分类 ID、影片数，点「查看影片」直接跳到该分类的影片列表）、**采集记录**（每次采集的统计与状态，运行中的记录直接显示实时进度；`已暂停` 可「继续」，`失败` 可「重试」（从失败页续采）或「重新运行」（从第 1 页重来），并标注来源任务 `源自 #N` 与尝试次数）。

资源库点开卡片进入资源详情：顶部是播放器，下方是**播放组（线路）**与**剧集**选择，切换即换源；m3u8 走 hls.js（Safari 原生播放），mp4/webm 直链直接播放，非直链地址给出「在新窗口打开」入口。另有「标准字段」「扩展字段」两个页签，可按 14 个分组查看全部 91 个标准字段（支持「只看非空」）与站点自定义字段。`web/dist` 已随仓库提供，clone 后无需构建前端即可运行；未知前端路由由 Gin 回退到 `index.html`。

## 目录结构

```
.
├── conf/config.yaml              # 运行配置
├── internal/
│   ├── router/                   # 第 1 层：按 /api/{模块}/v1 分组的路由表（只依赖 controller）
│   ├── controller/               # 第 2 层：HTTP 适配（只依赖 service / utils）
│   ├── service/                  # 第 3 层：业务逻辑（只依赖 repository）
│   │   ├── asset.go / source.go / video.go / category.go / job.go / health.go / meta.go
│   │   ├── crawl.go              # 全量 / 增量采集编排、分类归档、游标与去重
│   │   ├── errors.go             # 业务错误语义
│   │   └── collector/            # 采集协议客户端（零丢失 JSON 解析、URL 拼装、实体转换）
│   ├── repository/               # 第 4 层：每个实体一个文件 / 对象 / 接口
│   │   ├── store.go              # 连接、迁移、装配 Repositories、Path / Close
│   │   ├── errors.go             # 共用的 ErrNotFound（包装 GORM 错误）
│   │   ├── asset.go              # AssetRepository      → model.Asset
│   │   ├── source.go             # SourceRepository     → model.Source
│   │   ├── video.go              # VideoRepository      → model.Video（全字段 upsert）
│   │   ├── category.go           # CategoryRepository   → model.Category（自动归档）
│   │   ├── job.go                # JobRepository        → model.CrawlJob
│   │   └── *_test.go             # 每个实体各一份测试 + store_test.go
│   ├── model/                    # 横切：GORM 模型
│   │   ├── video.go              # 全字段 Video 实体（含 CategoryID）
│   │   ├── category.go           # Category 实体（按站点隔离）
│   │   └── video_fields.go       # VodFields 字段清单（唯一数据源）
│   ├── config/                   # 横切：配置加载与校验
│   ├── utils/                    # 横切 utils 层：纯工具（不依赖任何 internal 业务包）
│   │   ├── request.go            # 路径 / query 参数解析
│   │   ├── json.go               # JSON 值字符串化
│   │   ├── time.go               # 时间归一化
│   │   ├── hash.go               # 内容指纹
│   │   ├── category.go           # 分类键归一化
│   │   └── env.go                # 整数环境变量
│   └── app/                      # 装配层：repository → service → controller → router
├── web/
│   ├── src/                      # Vue 3 源码
│   └── dist/                     # 构建产物（Gin 托管，随仓库提交）
├── main.go                       # 服务入口
├── architecture_test.go          # 分层依赖方向守卫测试
├── go.mod / go.sum
└── Readme.md
```

## 测试

`go test ./...` 覆盖**四层依赖方向守卫**、**utils 横切纯洁性守卫**、**仓储层一实体一对象守卫**、**`/api/{模块}/{版本}` 路由规范守卫**、配置加载、工具函数、站点/影片/任务/**分类** CRUD、分类自动创建与按站点隔离、分类幂等与改名、分类影片数与按分类筛选、**二级分类（苹果CMS type_pid / type_id_1 父子关联、父级缺失暂存孤儿、父级出现后自动挂接）**、去重 upsert、**字段清单与实体一致性**、**全字段零丢失解析（数字/字符串/null/对象/数组/自定义字段）**、**播放组解析（多线路 `$$$`/`#`/`$` 分隔符、默认/线路N 命名、无 `$` 回退）**、**全字段指纹与全字段写回**、URL 拼装、**`code != 1` 拒绝解析**、全量/增量/游标/去重/失败回写，**资源检索（名称 / 地域筛选、按更新时间与评分升降序排列、地域枚举去重）**、**采集任务调度（异步入队、并发上限运行时调整、运行中暂停、按续采页码继续、同站点互斥）**、**失败自动重试（退避等待、失败页续采、等待期间可暂停）**、**手动重试与重新运行（断点续采 / 从第 1 页重来、来源任务与尝试次数落库）**、**实时进度（当前页/总页数、百分比、速率、剩余时间、重试倒计时、运行中记录带 live 进度）**，以及通过 HTTP 接口触发采集与字段元数据/详情/分类（含二级树）/播放组/地域与排序检索/任务管理接口的完整链路。采集测试用本地 HTTP 模拟站点，不依赖外网。

HTTP 链路测试位于 `internal/router`（装配后打真实请求，同时校验新路由结构与旧路由已下线），采集编排测试位于 `internal/service`，协议解析测试位于 `internal/service/collector`，纯工具测试位于 `internal/utils`；`internal/repository` 下按实体拆为 `asset_test.go` / `source_test.go` / `video_test.go` / `job_test.go`，另有 `store_test.go` 校验连接与四个对象装配。SQLite 使用纯 Go 的 modernc.org/sqlite 驱动（经 github.com/glebarez/sqlite 接入 GORM），无需本机 gcc，测试直接运行即可（`CGO_ENABLED=0`）。

端到端可用模拟站点验证：91 个标准字段全部落库、`extra` 保留自定义字段与嵌套对象（不再存整条 `raw`）、只改一个冷门字段也能触发更新、全量/增量/去重/幂等/批量采集、按站点自动分类归档与 Vue 页面托管均正常。

## 计划

- [ ] 支持 XML 格式采集接口
- [ ] 定时自动增量采集（cron）
- [ ] 采集结果导出（CSV / JSON）与站点分类绑定
