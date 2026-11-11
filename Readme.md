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
| `Source` | `repository/source.go` | `SourceRepository` | 站点 CRUD、`ListEnabledSources`、`UpdateSourceResult`、级联删除、清空站点数据 |
| `Video` | `repository/video.go` | `VideoRepository` | `UpsertVideos`、`GetVideo`、`ListVideos`、`CountVideosBySource` / `ByCategory` |
| `Category` | `repository/category.go` | `CategoryRepository` | `EnsureForVideos`、`List`、`Get` |
| `CrawlJob` | `repository/job.go` | `JobRepository` | `CreateJob` / `FinishJob` / `ListJobs` |
| `User` / `Session` / `UserSource` | `repository/user.go` | `UserRepository` | 账号 CRUD、会话签发/校验/吊销、站点授权读写 |

`repository/store.go` 只是容器：`Repositories` 共享一条 `*gorm.DB`，负责连接、自动迁移、装配各实体对象与 `Path` / `Close`，**不承载任何实体读写方法**；`errors.go` 放各对象共用的 `ErrNotFound`，`visibility.go` 放各实体共用的「可见站点白名单」过滤规则。测试同样按实体拆为 `asset_test.go` / `source_test.go` / `video_test.go` / `job_test.go` / `user_test.go`，`store_test.go` 只测连接与装配。

**采集写入采用批量策略**（避免逐条 SQL 往返拖慢大站采集）：

- `UpsertVideos` 对整批按 `(source_id, vod_id)` **只发 1 次 SELECT** 取回已存在行，在内存里分成「新增 / 未变 / 已变」三组：新增走 `CreateInBatches`，已变走 `INSERT … ON CONFLICT DO UPDATE` 批量更新业务列（保留 `first_seen_at` / `created_at`，FTS5 行级触发器逐行同步全文索引），未变只按 `id IN` 批量刷 `last_seen_at` 并按需补历史数据缺失的 `category_id` / `vod_play_list`。全程仍在单事务内，atomic 不变；`New / Updated / Unchanged` 统计、批内重复 (后写覆盖前写)、跨站点同 `vod_id` 互不干扰等语义全部保留。
- 分类 / 标签 / 地域的 `EnsureForVideos` 同样改为「一次批量查询取回已存在 → 只把缺失项 `CreateInBatches`」，替代原先按名逐条 find-or-create。
- 实测（临时库、GORM SQL 计数）：50 条纯新增 **2 条 SQL**（旧 ~100）、50 条全未变 **2 条**（旧 ~50）、40 条影片首采归档（标签/地域/分类各 40 新值）**7 条**（旧 ~240）；5000 条批量写入约 **2341 条/秒**、全未变重复扫描约 **12141 条/秒**。SQL 语句数从 O(N) 降为 O(N/批量 + 常数)。

约束的具体落地：

- 每个 service 只注入自己需要的实体仓储**接口**（如 `AssetService` 拿 `AssetRepository`，`CrawlService` 拿 `SourceRepository` + `VideoRepository` + `JobRepository`），看不到 `*gorm.DB`；GORM 的 `ErrRecordNotFound` 在仓储层翻译为 `repository.ErrNotFound`，服务层再翻译为 `ErrInvalid / ErrUnauthorized / ErrForbidden / ErrNotFound / ErrConflict` 五个哨兵错误，控制器据此决定 400 / 401 / 403 / 404 / 409 / 500（采集接口默认 502）。
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

浏览器打开 http://localhost:8080 ：**第一个登录的账号会自动注册为管理员**（用户名密码自行设定，无需预先建号），之后可在「用户管理」里创建普通用户并勾选其可见站点。管理员在「站点配置」里添加 JSON 接口，点「全量」即可采集入库。数据库默认创建在 `data/assetspider.db`（父目录自动创建）。

> 生产环境：push 到 `main` 会自动部署到服务器并启动服务，详见下文「自动部署」章节。

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

### 系统设置：在线配置、即时生效

侧边栏「系统设置」（管理员）把原先散落的**采集运行时配置**收拢到一处，保存后**无需重启**即可生效，并写回 `conf/config.yaml`，下次启动依旧保留。可编辑项及其含义由 `GET /api/settings/v1` 的 `fields[]` 元信息驱动前端渲染：

| 配置项 | 说明 | 范围 |
| --- | --- | --- |
| `maxConcurrent` | 同时运行的任务数（原任务页并发上限），即时调整限流器 | 1..64 |
| `timeoutSeconds` | 单次 HTTP 请求超时，下一次请求即用新值 | >0 |
| `maxPages` | 单次采集最大页数，站点单独配置时以站点为准 | >0 |
| `pageSize` | 每页条数，0 表示由站点返回决定 | ≥0 |
| `defaultIncrementalHours` | 站点未配置时的增量时间窗（接口 `h` 参数） | >0 |
| `retryTimes` | 失败自动重试次数 | ≥0 |
| `retryBackoffSeconds` | 重试首次退避秒数，之后翻倍、上限 30 秒 | >0 |
| `progressSaveSeconds` | 运行中进度落库间隔 | >0 |
| `pageIntervalMs` | **每页间隔**（相邻两页之间的等待），降低对站点请求频率、避免被限流封禁；0 表示不限速（全速采集）。运行中的任务从下一页起立即生效，暂停 / 停机时立即中断等待 | 0..60000 毫秒 |
| `userAgent` | 采集接口 UA | 字符串 |

实现要点：

- **运行时热更新**：`config.Store` 持锁保存配置快照。采集循环与 Fetcher 每页 / 每请求读最新值（超时用 `context.WithTimeout` 逐请求应用，无共享 client 字段的竞争）；`maxConcurrent` 由 `CrawlService.ApplyLive` 同步到限流器；`pageIntervalMs` 每页翻页前重新读取，改完从下一页起生效，等待期间若任务被暂停 / 服务停机则通过 `ctx.Done()` 立即醒来，不会空等整个间隔。
- **持久化且不丢注释**：写回只改 `collector` 段里认识的键，用 `yaml.Node` 逐节点更新并**保留其余配置、注释与顺序**，先写临时文件再 rename；老配置缺失的键会自动补齐。
- **环境变量优先**：由 `ASSETSPIDER_COLLECTOR_*` 接管的字段在页面标为「环境变量」只读——在线改动会被下次启动的环境变量覆盖，因此直接禁止修改，请在启动环境里调整。设置页同时展示 `server` / `database` / `web` / `auth` 等**启动项只读值**（改这些需编辑配置文件并重启）。
- **接口**：`GET /api/settings/v1` 登录可读，`PUT /api/settings/v1` **仅管理员**；支持只提交部分字段（未提交字段保持原值）。校验失败返回 `400`，环境变量锁定的字段被改动时返回明确原因。

### 采集任务：并发上限、暂停 / 继续、失败重试与重新运行

采集以**异步任务**运行：发起接口立即返回任务号，抓取在后台排队执行，进度通过任务接口轮询。**前端只在「站点配置」与「采集记录」两个页签停留时轮询**（每 1.5 秒一次；前者刷任务视图与站点上次采集状态，后者刷采集记录）；停在资源库 / 视频分类 / 用户管理时不发采集类请求，页面不可见时同样暂停。切回这两个页签或浏览器标签重新可见时会立刻补刷一次，不用等下一个周期；顶部「刷新」按钮始终可以手动做一次全量刷新。

- **并发上限**：`collector.max_concurrent`（默认 3，环境变量 `ASSETSPIDER_COLLECTOR_MAX_CONCURRENT`）限制「同时运行的任务数」，超出的任务进入队列（状态 `queued`），空出槽位后自动补位。并发上限已并入「系统设置」页统一管理：在线修改会**即时调整限流器并写回配置文件**（重启后保留）；旧的 `PUT /api/tasks/v1/capacity` 仍可用，在有配置存储时同样持久化，两者共用同一份数据源。任务页头部只做运行 / 排队数的只读展示。
- **每页间隔**：`collector.page_interval_ms`（默认 0，即全速不限速）在相邻两页之间等待指定毫秒，用于降低对站点的请求频率、避免触发限流或封禁；在「系统设置」页在线可调（0..60000 毫秒），运行中的任务**从下一页起立即生效**，暂停 / 停机时立即中断等待、不空耗。
- **同站点串行**：一个资源站同一时刻只允许一个任务**运行**，重复发起的任务不再报错，而是以 `queued` 状态进入该站点的队列，前一个任务结束（成功 / 暂停 / 失败）后自动补位；排队中的任务同样可以暂停。不同站点可并行。
- **手动暂停**：`PUT /api/tasks/v1/{jobId}` + `{"status":"paused"}`。暂停是协作式的——在页边界生效并中断正在进行的 HTTP 请求，**已完成的整页数据不会回滚**，也不会写入半页数据；**「等待自动重试」期间同样可以暂停**，不必等退避结束。
- **进度保存**：全量采集把「下一次从第几页开始」写入 `crawl_jobs.resume_page`；增量采集照常推进 `last_cursor`。因此暂停不是失败，站点与记录状态都是 `paused`。
- **运行中定期落库**：任务运行时每 `collector.progress_save_seconds`（默认 60 秒，环境变量 `ASSETSPIDER_COLLECTOR_PROGRESS_SAVE`）把已完成页数、拉取/新增/更新/未变统计、`attempt` 与续采页 `resume_page` 写进采集记录。这样**进程被强杀或机器重启也不会丢进度**——重启后服务会自动复用原记录、从 `resume_page` 指向的下一页继续采集（而不是从头再来）。定期落库只写进度字段，`status` / `finished_at` / `message` 始终由任务收尾独占写入，不会把已结束的任务改回「进行中」。
- **站点影片总数**：`sources.video_count` 是站点已入库影片数的快照，让站点列表不必每次都对 `videos` 做 `COUNT(*)`。它在**同一个节拍**上维护：运行中每 `progress_save_seconds`（默认 60 秒）重算一次，任务结束（成功/暂停/失败）时再重算一次；每次都是 `COUNT(*)` 重算而不是在旧值上累加，所以重复采集既有影片不会把数字算多。服务启动时会按 `videos` 表整体校准一次，升级后历史站点不会停留在默认值 0。
- **继续**：`PUT /api/tasks/v1/{jobId}` + `{"status":"running"}`，按原站点、原模式重新入队：全量从 `resume_page` 续采，增量从游标继续，不会从头再拉一遍。**复用原任务记录**，统计与尝试次数继续累加。
- **失败自动重试**：抓取/入库失败后按指数退避自动重试，次数由 `collector.retry_times`（默认 2，即最多尝试 3 次）、首次间隔由 `collector.retry_backoff_seconds`（默认 3 秒，之后翻倍、上限 30 秒）决定，设 `0` 可关闭。重试**从上次失败的那一页**继续，已完成页面不会重复抓取；每次尝试都记在 `crawl_jobs.attempt`。等待重试期间任务视图带 `retryInSeconds` 倒计时与失败原因。
- **手动重试**：`POST /api/tasks/v1/{jobId}/retry`，仅对 `failed` 的任务有效。**复用原任务记录**（不会新开一条 `crawl_jobs`），从失败页续采，拉取/新增/更新/未变与页数在原值上继续累加，尝试次数也接着往下数；记录的 `retry_of` 保持为空。
- **重新运行**：`POST /api/tasks/v1/{jobId}/rerun`，对任何已结束（`failed` / `success` / `paused`）的任务有效，**复用原任务记录**，忽略断点从第 1 页重来，上一轮的统计与尝试次数清零、记录 ID 不变；`retry_of` 保持为空，仍在运行的任务返回 409。即**继续 / 重试 / 重新运行都不再新开记录**。
- **实时进度与统计**：`GET /api/tasks/v1` 的每个任务带 `page` / `pageCount` / `pages` / `fetched` / `new` / `updated` / `unchanged` / `percent`（-1 表示总量未知，前端展示不定态进度条）/ `itemsPerSecond` / `etaSeconds` / `attempt` / `maxAttempts` / `retryInSeconds` / `message`；**每完成一页就刷新一次**，所以拉取/新增/更新/未变都能随任务实时增长。采集记录接口对运行中的记录额外带 `live` 字段，页面优先用 `live` 里的统计，因此「采集记录」页看到的也是同一份实时数据。
- **重启自动恢复**：任务调度只存在于进程内存，服务启动时会把上次遗留的 `running` / `queued` 记录**自动复用原记录重新入队**，全量从 `resume_page` 续采、增量从 `last_cursor` 继续，统计与尝试次数接着累加（不新开记录）。启动钩子会先把它们标记为中断兜底，即使某条恢复失败（站点已删除、同站点冲突）页面也不会一直显示「进行中」；失败只记日志、不阻断服务启动。用户**手动暂停的 `paused` 任务不动**，仍由用户自行「继续」。并发上限与同站点互斥沿用现有调度器。

### 定时增量采集（站点级 cron）

每个站点都能单独配置「定时增量」：在站点编辑弹窗里勾选启用、填写一条 5 段 cron 表达式（**分 时 日 月 周**），到点由后台调度器自动发起一次 `incremental` 任务。

- 表达式支持 `*`、`*/n`、`n`、`a-b` 与逗号列表，周字段 `0`/`7` 都是周日；界面提供「每 30 分钟 / 每小时 / 每 6 小时 / 每天 03:00 / 工作日 03:00」常用预设。非法表达式在保存时直接返回 400，启用时留空也拒绝。
- **输入时实时校验**：编辑框下方即时显示「有效 / 无效 + 具体原因」、这条规则的中文含义（如「每天 03:00 自动执行一次增量采集」「每 30 分钟执行一次」）以及**按当前时间算出的接下来 3 次触发时刻**。校验与解释在前端 `web/src/cron.js` 里本地完成，不发请求，语义与后端 `internal/utils/cron.go` 一致；未启用定时、未填写时也分别给出提示。表达式无效时前端先拦下保存。
- 调度器随服务启动，**对齐到整分钟**后每分钟检查一次，只判断「当前分钟」是否命中：停机期间错过的时刻**不补跑**，避免重启时一次性灌进一堆任务。`sources.schedule_last_at` 记录上次触发的分钟，同一分钟（含进程重启）不会重复触发。
- 只有**启用**且配置了 cron 的站点会触发；该站点已有排队 / 运行中的任务时跳过本次，不排队堆积。定时任务是系统行为，不经过登录主体校验，但仍完整走任务排队、断点续采、状态与游标回写。
- 站点列表会显示定时规则与**下次执行时刻**（`nextScheduleAt`，按 cron 现算、不落库）；定时任务与手动采集在「采集记录」里形态一致，只是 `mode` 为 `incremental`。定时配置随站点 JSON 一起导入 / 导出。

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
- **父级从 `type_id_1` 推断并预建**：真实苹果CMS 二级结构里，一级分类常是**纯容器**——自身没有任何影片，其 `type_id` 只会出现在子分类的 `type_id_1` 中，永远不会作为某条影片的 `type_id` 出现。因此除了影片自身的 `type_id`，还会从每条影片的 `type_id_1` 推断并创建父级分类；否则父级永远建不出来，二级分类会沦为永久孤儿。
- **父级名称与层级有多条通用权威来源**（全程按苹果CMS 协议约定探测，**不针对任何具体站点**）：`videolist` 通常不携带父级（纯容器）分类的名称，父级创建时只能以 `type_id` 兜底展示。因此每次采集会：① 请求专用接口 `ac=typelist` 拿 `list[]`（分页，**最多尝试 3 页**，避免分类巨多或虚假 `pagecount` 拖慢采集）；② 若该接口**不可用**（HTTP 非 200 / `code != 1` / 网络或解析错误，如返回 400），**通用降级**为依次探测「站点显式配置的 `ac` → 标准 `list` → `videolist`」动作，只从响应的 `class[]` 取分类定义（成功返回空列表则视为有效应答、不降级）——苹果CMS 在列表响应里普遍附带本站全部 `class[]`（`type_id` / `type_pid` / `type_name` 是权威定义），且探测请求会移除 `h` / `pg` / `pagesize` 等增量与分页参数、保留认证参数。③ 同时每页正常采集也顺带收集影片响应自带的 `class[]`。各来源按 `type_id` 合并、`class[]` 优先；**降级来源只认 `class[]`，绝不把影片 `list[]` 当分类**。采集结束后据此重建分类树并回填名称：仅补全「尚无名称 / 仍是 `type_id` 占位」的分类，已带真实名称的不会被覆盖。全部接口不可用时属非致命，退回库内 `type_id_1` / `type_pid` 关系，采集不中断。
- **一级分类缺失会自动补齐**：只要库里存在指向某个父级 ID 的引用（`videos.type_id_1` 或 `categories.type_pid`），却没有对应的分类记录，就补建该一级分类并把二级分类挂回去。采集结束时每次都会重建；老库则在启动时（`schemaVersion` 5）按现有数据回灌一次，**无需重新采集**。补建时若无权威名称，先以 `type_id` 占位，下次采集拿到 `class[]` / `typelist` 会自动换成真实名称。
- **父级暂未采集到也安全**：若某批只采到二级（其 `type_pid` 指向的一级分类尚未出现），该分类先记为二级、父级留空；待该一级分类（经由 `type_id_1` 推断或后续直接出现）到位后，会自动回挂到正确父级，不会丢层级。
- 分类页一级分类显示的是**整棵子树的影片数**（一级分类常是纯容器，直接计数会是 0）；按一级分类筛选影片时同样包含其全部子分类。
- `categories` 表新增 `type_pid` / `parent_id` / `level` 三列（由 GORM 自动迁移），现有单级分类升级后默认 `level=1`、无父级，向后兼容。

### 标签（按 vod_tag 自动归档）

采集到视频时，还会按其自带的 `vod_tag` **自动拆分标签并建立关联**，与分类一样无需手工维护：

- `vod_tag` 的多个标签用半角/全角逗号、斜杠、竖线、顿号或空格分隔，采集时统一拆分为独立标签（去空格、去重、大小写不敏感）。
- **每个资源站一套标签**：`tags` 表用 `(source_id, name)` 复合唯一索引，不同站点即使标签同名也各建各的，互不串扰。
- 标签与影片是**多对多**关系，经 `video_tags(video_id, tag_id)` 连接表关联。每批入库后按「先删后插」回填：影片标签变化会联动更新，标签被移除时历史关联一并清除。
- 对内容指纹未变的历史影片，跑一次全量采集即可补齐标签与关联。
- 删除站点时，其 `video_tags` 关联与 `tags` 随影片和采集记录一起级联清理。
- 资源库支持**按标签筛选**：`GET /api/videos/v1?tag_id=` 命中带该标签的影片；标签列表与影片数见 `GET /api/tags/v1`。

### 地域（按 vod_area 自动归档）

地域与标签**完全同构**——原先它是 `videos.vod_area` 上的一个字符串列，筛选只能 `LIKE '%大陆%'`、下拉只能对整个影片表做 `DISTINCT`。现在拆成独立实体：

- `areas` 表 `(source_id, name)` 复合唯一，每个资源站一套；`video_areas(video_id, area_id)` 连接表承载多对多关系。
- 采集时按 `vod_area` 拆分（多值写法如「美国/英国」会拆成两个地域），与标签共用同一套分隔符规则；入库后「先删后插」回填关联。
- 资源库**按地域筛选**改为 `GET /api/videos/v1?area_id=`（走连接表，不再模糊匹配）；下拉选项与各选项影片数见 `GET /api/areas/v1`，前端按站点分组展示。
- 老库升级（`PRAGMA user_version` < 3）时按现有 `videos.vod_area` **回灌一次** `areas` + `video_areas`，不用重新采集；回灌以 `video_areas` 为空为前置条件，重复启动是空转。
- `videos.vod_area` 原文本保留（卡片与详情仍直接展示），但其单列索引在拆表后失去用途，升级时会一并 `DROP`。

### 年份（按 vod_year 筛选，不拆表）

年份的处理方式与标签 / 地域**不同**：它不拆实体，直接按 `videos.vod_year` 等值筛选。

- 原因是年份是影片上的**单值属性**——没有多值写法、不需要跨站共享的实体表、也不存在多对多关系，拆表只会白白多一次 JOIN。
- 筛选：`GET /api/videos/v1?year=2025`；下拉选项与各年份影片数见 `GET /api/videos/v1/years`，即对 `vod_year` 做 `GROUP BY`（走 `idx_videos_vod_year`），按年份倒序返回。
- 空年份（接口未返回 `vod_year`）不进入下拉，避免出现无意义的空白选项。
- 年份与站点无关：切换站点不会清掉已选年份，切换年份也不会改动站点（不参与分类 / 标签 / 地域那套站点联动）。
- 老库升级（`PRAGMA user_version` < 4）时为 `vod_year` 补建索引并重算统计信息，不做数据回灌。

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

### 播放页显示「没有播放地址」怎么排查

**先说结论：入库链路不丢字段。** `list[]` 每条记录按 `VodFields` 全字段映射，`vod_play_from` / `vod_play_url` / `vod_play_list` 三列都在写回 map 里；内容指纹变化即整行更新。播放页为空，几乎都是**接口响应里就没有播放地址数据**（或字段写法不是标准苹果CMS 键名），不是入库时丢的。

按下面顺序自查：

1. **直接看接口返回**（把域名换成自己的站点）：
   ```bash
   curl 'https://站点/api.php/provide/vod/?ac=videolist&pg=1'
   curl 'https://站点/api.php/provide/vod/?ac=detail&pg=1'
   ```
   苹果CMS 官方约定：`ac=videolist` / `ac=detail` 返回含 `vod_play_url` 的完整字段，其中 `ac=detail`（不带 `ids`）返回的是含播放地址的详情列表；**`ac=list` 通常只返回分类或精简列表，不含播放地址**。若 `list[0]` 里连 `vod_play_url` 都没有，播放器自然没有地址。
2. **检查站点配置**：接口地址或「附加参数」里不要带 `ac=list`；要指定动作就在附加参数里写 `ac=videolist` 或 `ac=detail`。显式配置了 `ac` 时系统尊重该配置，不再自动切换。
3. **重新执行一次全量采集，看采集记录的「消息」**：
   - 「已自动改用 `ac=detail` 补齐播放数据」→ 默认动作没给播放地址，系统已自动切到详情模式重新采集该页；
   - 「其中 N 条记录接口未返回播放地址」→ 按提示检查 `ac` 与接口字段写法。
   任务结果里还有 `noPlayUrl`（无播放地址条数）与 `autoAc`（自动切换的动作）两个诊断字段。

采集器已兼容资源站的常见非标准写法（**仅在标准键缺失时补齐**，原始别名键仍保留在 `extra` 便于溯源）：

| 标准键 | 兼容别名 / 形态 |
| --- | --- |
| `vod_play_url` | `vod_playurl` / `vod_url` / `v_playdata` / `play_url` / `playurl` / `vod_playurls`；也支持直接给**结构化 JSON 数组**（`[{"from":"m3u8","episodes":[{"name":"第1集","url":"..."}]}]`） |
| `vod_play_from` | `vod_playfrom` / `v_playfrom` / `play_from` / `playfrom` |
| `vod_play_list` | `vod_playlist` / `play_list` / `playlist` / `vod_play_url_list`（结构化数组会同步规范化 `vod_play_from` / `vod_play_url` 两列） |
| `vod_id` | `id` / `v_id` / `vodid` / `vodId` |
| `vod_name` / `vod_time` / `vod_pic` / `type_id` / `type_name` | `v_name` / `v_time` / `v_pic` / `tid` / `typename` 等（海洋CMS 系常见） |

接口完全不返回 `vod_id` 时，只要记录有名称或播放地址，就用内容指纹生成稳定的 `auto-*` 伪 ID 入库（同一条影片重复采集仍命中同一 ID），不再整条跳过。

> 自动探测只在**全新采集的第 1 页**发生：默认 `ac=videolist` 整页无播放地址且站点未显式指定 `ac` 时，尝试一次 `ac=detail`，只有确实拿到播放地址才切换，后续页沿用；续采 / 重试 / 重新运行都不额外请求，不改变断点续采的请求序列。探测失败保持原样并给出上述排查提示。

### 地址改写（播放地址 / 封面图片）

站点可以额外配置两组**读取期改写**规则，**只在读取时生效**——数据库里的 `vod_play_list` 与图片字段始终是采集到的原始地址，改配置不会污染已入库数据，清空配置即恢复原样。

| 站点字段 | 作用 |
| --- | --- |
| `parseApi` 视频解析地址 | 拼接在播放地址前，如 `https://jx.example.com/?url=` 得到 `https://jx.example.com/?url=<原地址>`；支持 `{url}` 占位符（如 `https://jx.example.com/play?src={url}&q=1`），未含占位符时直接前缀拼接 |
| `playDomain` 视频播放域名 | 替换原播放地址的**协议 + 域名**，保留路径、查询串与锚点；只写域名时自动补 `https://`，尾部斜杠会被去掉 |
| `picApi` 图片解析/代理地址 | 拼接在封面等图片地址前，如 `https://img.example.com/proxy?url=`；同样支持 `{url}` 占位符 |
| `picDomain` 图片域名 | 替换原图片地址的协议 + 域名，与 `playDomain` 互相独立（图片 CDN 通常与播放 CDN 不同） |

- 同一组内两者同时配置时**先替换域名、再拼接解析/代理地址**（代理服务拿到的是已换到可用域名的地址）。
- 播放地址改写应用于影片列表（`GET /api/videos/v1`，详情弹窗直接复用列表数据）、单条详情与播放组接口，结果一致。
- 图片改写作用于 `vod_pic` / `vod_pic_thumb` / `vod_pic_slide` / `vod_pic_original` 以及 `vod_pic_screenshot`（`#` 连接的截图集合逐段改写后拼回），列表与详情接口都会应用。
- **解析地址 → iframe**：拼接了 `parseApi` 的播放地址是解析服务网页，后端改写时给该集打上 `frame: true` 标记，前端据此改用 `iframe` 承载（不依赖前端站点缓存，也不会把解析页误判成 m3u8）；清空配置即恢复直链播放。
- **HLS 优先 hls.js**：`m3u8` 一律先走 `hls.js`（MediaSource），原生 `<video>` 仅在浏览器明确返回 `canPlayType(...) === 'probably'` 时兜底。Linux/Chrome 会误报 `'maybe'`，直接把 m3u8 交给 `<video>` 会报 `MEDIA_ERR_SRC_NOT_SUPPORTED`——这是此前播放组播放失败的根因。
- **无后缀地址兜底**：苹果CMS 不少站点的播放地址不带扩展名，先按 HLS 试播、失败再回退 `<video>` 直链；mp4 / webm / ogg / mov / m4v 走直链。
- **失败可见**：HLS 加载或解码错误、以及 `<video>` 的 `error.code` 都会翻译成中文提示并保留「在新窗口打开」入口；网络类 HLS 错误自动重试一次，切换线路/集数时清空错误提示。

### 数据入库

`videos` 表保存全部标准字段、扩展字段、分类关联、内容指纹与首次/最近入库时间（不再冗余保存整条原始 JSON）。每次采集生成一条 `crawl_jobs` 记录（模式、状态、页数、拉取/新增/更新/未变条数、耗时、错误信息、续采页码 `resume_page`、尝试次数 `attempt`；`retry_of` 为兼容旧数据保留的历史字段，现在继续 / 重试 / 重新运行都复用原记录，不再产生新值），站点表回写上次采集状态与游标。

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
  max_pages: 5000                # 单次采集最大页数
  page_size: 0                   # 0 = 由站点决定；>0 附加 limit
  user_agent: "AssetSpider/1.0 (+maccms-json-collector)"
  default_incremental_hours: 24  # 站点未配置时的增量时间窗
  max_concurrent: 3              # 同时运行的采集任务上限，超出的排队；可在「系统设置」页在线修改并持久化
  retry_times: 2                 # 失败后自动重试次数（0 = 不重试），从失败页续采
  retry_backoff_seconds: 3       # 首次重试等待秒数，之后翻倍，上限 30 秒
  progress_save_seconds: 60      # 运行中任务把进度与站点影片总数写库的间隔（秒）
  page_interval_ms: 0            # 相邻两页之间的间隔（毫秒），0 = 不限速；降低对站点请求频率
auth:
  token_ttl_hours: 168           # 登录令牌有效期（小时），默认 7 天
  bcrypt_cost: 10                # 密码哈希强度，4..31，越大越慢
```

环境变量覆盖：`ASSETSPIDER_SERVER_ADDR`、`ASSETSPIDER_DB_PATH`、`ASSETSPIDER_WEB_DIR`，
`ASSETSPIDER_AUTH_TOKEN_TTL_HOURS` / `ASSETSPIDER_AUTH_BCRYPT_COST`，以及
`ASSETSPIDER_COLLECTOR_TIMEOUT` / `_MAX_PAGES` / `_PAGE_SIZE` / `_DEFAULT_HOURS` / `_MAX_CONCURRENT` / `_RETRY_TIMES` / `_RETRY_BACKOFF` / `_PROGRESS_SAVE` / `_PAGE_INTERVAL` / `_USER_AGENT`
（旧变量名 `_PARALLEL` 仍兼容，等价于 `_MAX_CONCURRENT`）。
`-config` 指定其他配置文件，未填写的字段沿用默认值。
`collector` 段的全部字段都可在页面「系统设置」里在线修改：保存即写回本文件并让运行中的采集器立即用上新值；被上面的 `ASSETSPIDER_COLLECTOR_*` 环境变量接管的字段在页面标为只读，需改启动环境后重启。

### 站点配置字段（存在数据库 `sources` 表）

| 字段 | 说明 |
| --- | --- |
| `name` | 站点名称 |
| `apiBase` | JSON 接口完整地址，如 `https://site.com/api.php/provide/vod/` |
| `format` | 采集格式，现支持 `maccms_json`（苹果CMS V10 Provide，默认）/ `seacms_json`（海洋CMS） |
| `extraParams` | 附加查询参数，如 `t=1&wd=电影`；可覆盖默认 `ac=videolist` |
| `parseApi` | 视频解析地址，播放时拼在播放地址前，如 `https://jx.example.com/?url=`；支持 `{url}` 占位符，留空表示不使用解析 |
| `playDomain` | 视频播放域名，播放时替换原播放地址的协议与域名，如 `https://cdn.example.com`（只写域名时自动补 `https://`），留空表示不替换 |
| `picApi` | 图片解析/代理地址，展示封面等图片时拼在原图片地址前，如 `https://img.example.com/proxy?url=`；支持 `{url}` 占位符，留空表示不使用 |
| `picDomain` | 图片域名，展示时替换原图片地址的协议与域名，如 `https://img.example.com`；与播放域名互相独立，留空表示不替换 |
| `enabled` | 是否参与「批量采集」 |
| `incrementalHours` | 增量时间窗（对应 `h`），0 表示使用全局默认值 |
| `maxPages` | 单次采集最大页数，0 表示使用全局值 |
| `scheduleEnabled` | 是否启用该站点的定时增量采集 |
| `scheduleCron` | 5 段 cron 表达式（分 时 日 月 周），如 `0 3 * * *`；启用时必填 |
| `scheduleLastAt` | 只读：上次定时触发时刻，用于同一分钟去重 |
| `nextScheduleAt` | 只读：按 cron 现算的下次触发时刻（仅站点列表返回，不落库） |

`pg`、`h` 由采集器强制写入，不会被附加参数干扰。定时规则见上一节「定时增量采集」。

### 站点配置 JSON 导入 / 导出

用于备份、迁移或批量建站。「站点列表」右上角有**导出 JSON**（所有登录用户可用，只导出自己可见的站点）与**导入 JSON**（仅管理员）按钮。

- `GET /api/sources/v1/export` 返回 JSON 附件（`Content-Disposition` 已带文件名）；前端另存为 `assetspider-sources-<时间戳>.json`。
- `POST /api/sources/v1/import` 直接提交 JSON 文件原文（`Content-Type: application/json`，上限 2 MB）。

导出格式只含可编辑字段，**不含** ID、影片数、上次采集状态、增量游标、时间戳等运行时数据，所以文件可以直接导入另一个实例，也不会覆盖目标实例的采集状态：

```json
{
  "version": 1,
  "exportedAt": "2026-11-09T12:00:00Z",
  "sources": [
    {
      "name": "某某资源站", "apiBase": "https://site.com/api.php/provide/vod/",
      "format": "maccms_json", "extraParams": "", "enabled": true,
      "incrementalHours": 24, "maxPages": 0,
      "scheduleEnabled": false, "scheduleCron": "",
      "parseApi": "", "playDomain": "", "picApi": "", "picDomain": ""
    }
  ]
}
```

导入规则：

- 接受三种 JSON 形态：上面的导出文件（含 `sources` 数组）、裸站点数组 `[{...}, {...}]`、单条站点对象 `{...}`。
- 按**站点名称**（忽略大小写与首尾空白）匹配已有配置：命中则更新（保留原 ID、采集状态与增量游标），未命中则新建；同一文件里出现同名配置也不会产生重复站点，因此**重复导入是安全的**。
- 逐条走与手工新增/编辑相同的校验（名称、http(s) 接口地址、格式、地址规范化等）。某一条失败只记入 `issues`，其余条目照常导入；响应形如 `{"total":4,"created":1,"updated":2,"failed":1,"issues":[{"index":3,"name":"坏站点","error":"参数无效: ..."}]}`。
- 导入只允许管理员，普通用户调用返回 403；导入成功后前端会刷新站点、影片、分类等数据。

**站点名称全系统唯一**：手工「新增站点」（`POST /api/sources/v1`）与「编辑站点」（`PUT /api/sources/v1/:id`）都按名称（忽略大小写与首尾空白）查重。重名时新建返回 `409`、改名改成另一条已存在站点也返回 `409`；改名成「自身」（含仅大小写差异）则允许。导入的「重名即更新」是这条规定在批量场景下的互补——手工建站不允许重名，要改已有站点就走编辑或导入。

### 检索与索引

影片列表是读得最重的接口：一次请求要同时完成「关键词 + 站点 / 分类 / 地域 / 标签筛选 + 时间或评分排序 + 分页」。索引与查询都按这条链路设计。接口**不返回命中总数**，也就不做任何 `COUNT` 全表扫描。

**B-Tree 复合索引**（`model` 里的 GORM 标签，`AutoMigrate` 自动建）：

| 索引 | 列 | 服务的查询 |
| --- | --- | --- |
| `idx_source_vod` | (source_id, vod_id) 唯一 | 采集 upsert、vod_id 反查主键 |
| `idx_videos_source_time` | (source_id, vod_time) | 单站点按更新时间排序 / 翻页 |
| `idx_videos_source_id` | (source_id, id) | 单站点 + 默认 id 倒序 |
| `idx_videos_vod_year` | (vod_year) | 年份筛选与年份下拉的 `GROUP BY` |
| `idx_videos_category_id` | (category_id) | 分类筛选、按分类聚合影片数 |
| `idx_video_tags_tag` | (tag_id, video_id) | 按标签筛选（主键反向查询会全表扫描） |
| `idx_categories_source_type` | (source_id, type_id) | 采集时按 `type_pid` 归树找父分类 |
| `idx_jobs_source_mode` | (source_id, mode, id) | 取「本站点本模式最近一条任务」判断续采 |

SQLite 的索引末尾隐含 rowid（即 `id`），所以 `(source_id, vod_time)` 同时满足 `ORDER BY vod_time DESC, id DESC`，不必再补 id 列。

**全文索引（FTS5）**：关键词检索原来是对 6 列做 `LIKE '%词%'`，前置通配符让索引彻底失效，每次搜索全表扫描。启动时 `ensureVideoSearch` 幂等创建 FTS5 虚拟表 `videos_fts`（`content='videos'` 外部内容模式，正文不复制第二份）+ 三条同步触发器；索引列与 LIKE 覆盖的列一致：`vod_name / vod_en / vod_actor / vod_director / vod_class / vod_area`。

分词器选 `trigram`：默认的 unicode61 按词切分，中文整句会变成一个词，搜「流浪」永远命中不了「流浪地球」；trigram 把文本切成三字符片段，天然支持中文子串与英文子串。

> **边界**：trigram 无法为少于 3 个字符的关键词建立片段，所以 **1~2 字的关键词仍走 LIKE 全表扫描**（代码里 `videoSearchMatch` 返回 false 即回退）。3 字及以上走索引。若底层 SQLite 未编译 FTS5，建表失败只打日志并整体退回 LIKE，不影响启动。
>
> **存储代价**：15 万条影片的库从 73 MB 涨到 105 MB（+43%），主要来自 6 列文本的 trigram 倒排表。若要压缩，可只把真正会被搜索的列（如 `vod_name` / `vod_actor`）放进索引，代价是这几列以外的字段不再参与关键词检索。

**查询侧的两处配合**：

- `PRAGMA user_version` 记录检索层版本（`schemaVersion`，当前 6），升级时按版本跑一次性数据回灌（v3 地域、v5 分类父级、v6 站点定时列）并整库 `ANALYZE` 一次，之后每次启动 `PRAGMA optimize`。没有统计信息时计划器会误判——例如「多站点可见 + 时间倒序」会选「先按站点过滤再全量排序」，有了统计信息才改走「按时间索引倒序扫描 + 边扫边过滤 + 凑满 LIMIT 即停」。
- 列表查询**一次 SQL 拿本页数据**：`ORDER BY ... LIMIT size OFFSET (page-1)*size`，不做 `COUNT`、不多取一条、不返回 `total` / `hasMore`。前端按「本页条数 >= size」认为还有下一页，不足一页即到底；数据刚好整页时会多发一次拿到空页的请求，代价远小于一次全量 `COUNT`。健康检查里的影片总数也不再查 `videos`，改为累加站点表的 `video_count` 快照。

**实测**（15 万条影片 / 6 站点 / 400 分类 / 30 万条标签关联，取 5 次最小值）：

| 场景 | 优化前 | 优化后 |
| --- | --- | --- |
| 可见性（3 站点）+ 时间倒序 | 138 ms | **3.0 ms** |
| 站点 + 时间倒序 | 46 ms | **1.0 ms** |
| 地域 + 时间倒序 | 54 ms | **12 ms** |
| 地域下拉（原 `DISTINCT vod_area`） | 99 ms | **<1 ms**（改读 `areas` 表） |
| 标签筛选 | 25 ms | **5.7 ms** |
| 关键词 4 字「流浪地球」 | 87 ms | **2.0 ms** |
| 关键词 2 字（回退 LIKE） | 83 ms | 80 ms |
| 默认列表第一页 | 0.5 ms | 0.5 ms |

写入侧没有额外代价：500 条影片 upsert 在新旧库上都是 ~566 ms（触发器维护索引的开销远小于逐行更新上百列本身）。



### 清空站点数据（保留站点配置）

站点列表每行（管理员）都有**清空数据**按钮，对应 `DELETE /api/sources/v1/:id/data`。它只清空该站点名下的数据，**站点配置本身（名称、接口地址、格式、附加参数、解析/播放/图片地址、启用状态、增量小时数、最大页数、定时配置）原样保留**。

- 删除内容：该站点的**影片**、**分类**、**标签**（含 `video_tags` 关联）、**地域**（含 `video_areas` 关联）与**采集记录**，其它站点的数据不受影响。
- 同时**复位采集状态**：`last_cursor`、`last_mode`、`last_status`、`last_error` 清空，`last_sync_at` / `last_full_sync_at` 置空，`video_count` 归零。这一步很关键——影片都删了，若增量游标还停在上次采到的最大 `vod_time`，下次增量采集会认为「没有更新」而永远补不回数据；复位后下一次增量从零开始重新拉取。
- 有**排队中 / 运行中**的采集任务时拒绝执行（`409`），避免清空与采集并发互相覆盖：先暂停采集或等任务结束再清空。
- 响应返回本次删除的条数，例如 `{"videos":1234,"categories":12,"tags":37,"jobs":8}`；站点不存在返回 `404`，普通用户调用返回 `403`。

## API

接口统一按 **`/api/{模块名}/{版本号}`** 组织，当前版本 `v1`（`internal/router.APIVersion`）。模块名一律是复数资源名词，操作由 HTTP 动词表达，路径中不含动词；升级时新增 `registerV2` 分组即可，旧版本保持可用。

### 登录与权限

- 除 `/api/system/v1/health`、`/api/auth/v1/status`、`/api/auth/v1/sessions`（登录）外，**所有接口都要带 `Authorization: Bearer <token>`**；未登录返回 `401`，权限不足返回 `403`。
- **第一个登录者自动注册为管理员**，拥有全部权限；此后账号只能由管理员通过 `/api/users/v1` 创建。密码用 bcrypt 存储，数据库只保存令牌的 SHA-256 摘要。
- 管理员可创建/启停/删除用户、修改角色与密码，并用 `sourceIds` 分配**可见站点**。非管理员只能看见被授权站点的**站点配置、影片、分类、采集记录与任务**，且只能采集这些站点；越权访问按 `404` 处理，避免泄露站点存在性。授权变更在下一个请求即刻生效，无需重新登录。
- 站点配置的增删改、系统设置（含并发上限）、用户管理为**管理员专属**；普通用户可读取设置但不可修改。资产台账写入同样仅管理员。
- 停用、删除、改密或变更角色会立即吊销该账号的既有会话；系统会保证始终保留至少一名启用状态的管理员，且禁止管理员停用/降级/删除自己。

| 模块 | 方法 | 路径 | 说明 |
| --- | --- | --- | --- |
| auth | GET | `/api/auth/v1/status` | 公开：系统是否已初始化、账号数 |
| auth | POST | `/api/auth/v1/sessions` | 公开：登录并创建会话，返回 `token` / `expiresAt` / `firstUser` / `isAdmin` / `user` |
| auth | DELETE | `/api/auth/v1/sessions` | 登出，吊销当前令牌 |
| auth | GET | `/api/auth/v1/me` | 当前账号、角色与可见站点 ID |
| auth | PUT | `/api/auth/v1/password` | 修改自己的密码，返回新令牌并作废旧会话 |
| users | GET / POST | `/api/users/v1` | 管理员：账号列表（含 `sourceIds`）/ 创建用户 |
| users | PUT / DELETE | `/api/users/v1/:id` | 管理员：更新（角色、启停、密码、站点授权）/ 删除用户 |
| system | GET | `/api/system/v1/health` | 公开：状态、数据库路径、站点数与影片数 |
| assets | GET / POST | `/api/assets/v1` | 通用资产台账（保留能力） |
| assets | DELETE | `/api/assets/v1/:id` | 删除资产 |
| sources | GET | `/api/sources/v1` | 站点列表，附带每个站点已入库影片数 `videoCount`（采集运行中每分钟刷新、结束时刷新，启动时整体校准）与定时配置的下次触发时刻 `nextScheduleAt` |
| sources | POST | `/api/sources/v1` | 新增站点（名称全系统唯一，重名返回 409） |
| sources | PUT | `/api/sources/v1/:id` | 全量更新站点（改名成另一条已存在站点返回 409，改名成自身允许） |
| sources | DELETE | `/api/sources/v1/:id` | 删除站点，级联清理其影片与采集记录 |
| sources | DELETE | `/api/sources/v1/:id/data` | 清空站点数据（影片/分类/标签/采集记录）并复位采集状态，**站点配置保留**；返回各表删除条数 |
| sources | POST | `/api/sources/v1/:id/crawls` | 为该站点执行一次采集，**同步**返回结果摘要（脚本/命令行用） |
| sources | GET | `/api/sources/v1/export` | 导出当前主体可见站点的配置 JSON（管理员为全部），只含可编辑字段 |
| sources | POST | `/api/sources/v1/import` | 导入配置 JSON（仅管理员）：按站点名称 upsert，返回新增 / 更新 / 失败统计与逐条错误 |
| crawls | POST | `/api/crawls/v1` | 对全部启用站点批量发起**同步**采集（超上限的排队） |
| settings | GET | `/api/settings/v1` | 系统设置：可编辑的 `collector` 配置 + `fields[]` 元信息 + `envLocked` 只读字段 + 只读启动项 `static`（登录可读） |
| settings | PUT | `/api/settings/v1` | 在线更新采集配置（**仅管理员**）：校验后写回 `conf/config.yaml` 并即时生效；支持只提交部分字段，被环境变量接管的字段拒绝修改 |
| tasks | GET | `/api/tasks/v1` | 采集任务列表：`capacity`（`maxConcurrent` / `running` / `queued`）+ `items[]`（排队中、运行中、暂停中，含实时进度） |
| tasks | POST | `/api/tasks/v1` | **异步**发起采集任务，body `{sourceId, mode}`，返回 `202` 与任务视图（`jobId`）；同站点已有任务时新任务排队（`status=queued`），不返回冲突 |
| tasks | PUT | `/api/tasks/v1/:id` | 更新任务状态：`{"status":"paused"}` 暂停 / `{"status":"running"}` 继续（`:id` 为 `jobId`） |
| tasks | POST | `/api/tasks/v1/:id/retry` | 手动重试**失败**的任务：**复用原记录**从失败页续采（统计累加，不新开任务），返回 `202` 与任务视图（非 `failed` 返回 409） |
| tasks | POST | `/api/tasks/v1/:id/rerun` | **重新运行**已结束的任务：**复用原记录**忽略断点从第 1 页重来，统计与尝试次数清零，返回 `202` 与任务视图（运行中返回 409） |
| tasks | GET | `/api/tasks/v1/capacity` | 读取并发容量 |
| tasks | PUT | `/api/tasks/v1/capacity` | 调整同时运行的任务数：`{"maxConcurrent":2}`（1..64）；兼容旧入口，有配置存储时同样写回配置持久化，与设置页共用数据源 |
| videos | GET | `/api/videos/v1?source_id=&category_id=&tag_id=&area_id=&year=&q=&sort=&page=&size=` | 分页查询入库影片，响应为 `{items, page, size}`（**不返回 total / hasMore**；前端按本页条数是否等于 size 判断是否继续翻页）。`q` 匹配名称/别名/演员/导演/分类/地域；`category_id` 按自动归档的分类过滤（含子分类）；`tag_id` 按自动归档的标签过滤；`area_id` 按自动归档的地域过滤（走 `video_areas` 连接表）；`year` 按 `vod_year` 等值过滤；`sort` 取值 `time_desc`（更新时间↓）/`time_asc`/`score_desc`（评分↓）/`score_asc`，空则为 id 倒序 |
| videos | GET | `/api/videos/v1/years?source_id=` | 出现过的年份及各自影片数（年份筛选下拉），按年份倒序 |
| videos | GET | `/api/videos/v1/:id` | 单条影片完整记录 |
| videos | GET | `/api/videos/v1/:id/play-groups` | 该影片的播放组数组（来自苹果CMS 播放组字段，已解析为结构化 JSON） |
| categories | GET | `/api/categories/v1?source_id=` | 视频分类列表，含 `videoCount`；`source_id` 只看该资源站的一套分类 |
| categories | GET | `/api/categories/v1/tree?source_id=` | 视频分类二级树（父分类在前、子分类嵌套），按苹果CMS type_pid 模型 |
| categories | GET | `/api/categories/v1/:id` | 单个分类详情 |
| tags | GET | `/api/tags/v1?source_id=` | 视频标签列表，含 `videoCount`；`source_id` 只看该资源站的一套标签 |
| areas | GET | `/api/areas/v1?source_id=` | 视频地域列表（与标签同构，每个资源站一套），含 `videoCount`；`source_id` 只看该资源站的一套地域 |
| meta | GET | `/api/meta/v1/fields` | 全字段元数据：`total`、`groups`、`fields[]`（`key` 原始键、`json` 前端键、`desc` 中文含义、`group`、`label`、`long`） |
| jobs | GET | `/api/jobs/v1?source_id=&limit=` | 采集记录（含 `queued` / `running` / `paused` / `success` / `failed` 与续采页码 `resumePage`） |

采集任务用 `POST` 创建，参数 `mode`（`full` / `incremental`，缺省 `incremental`）放在 **JSON body**，同时也兼容 query：

```bash
# 1) 登录：第一个登录者自动成为管理员。之后所有请求都带这个令牌。
TOKEN=$(curl -s -X POST http://localhost:8080/api/auth/v1/sessions \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"your-password"}' \
  | sed -E 's/.*"token":"([^"]+)".*/\1/')
api() { curl -H "Authorization: Bearer $TOKEN" "$@"; }

# 2) 管理员创建普通用户，只授权站点 1、2（sourceIds 为空数组表示不授权任何站点）
api -X POST http://localhost:8080/api/users/v1 \
  -H 'Content-Type: application/json' \
  -d '{"username":"viewer","password":"pass123456","role":"user","enabled":true,"sourceIds":[1,2]}'
api http://localhost:8080/api/users/v1

# 3) 站点配置（管理员）
api -X POST http://localhost:8080/api/sources/v1 \
  -H 'Content-Type: application/json' \
  -d '{"name":"测试资源站","apiBase":"https://site.com/api.php/provide/vod/","enabled":true,"incrementalHours":24}'

# 带播放增强：解析地址拼接 + 播放域名替换（都可省略）
api -X PUT http://localhost:8080/api/sources/v1/1 \
  -H 'Content-Type: application/json' \
  -d '{"name":"测试资源站","apiBase":"https://site.com/api.php/provide/vod/","enabled":true,"incrementalHours":24,"parseApi":"https://jx.example.com/?url=","playDomain":"https://cdn.example.com","scheduleEnabled":true,"scheduleCron":"0 3 * * *"}'

# 异步发起（推荐，前端即走这条）：立即返回任务号
api -X POST http://localhost:8080/api/tasks/v1 \
  -H 'Content-Type: application/json' -d '{"sourceId":1,"mode":"full"}'
# 查看进度 / 并发容量
api http://localhost:8080/api/tasks/v1
# 暂停（进度会保存）与继续（从断点续采）
api -X PUT http://localhost:8080/api/tasks/v1/1 -H 'Content-Type: application/json' -d '{"status":"paused"}'
api -X PUT http://localhost:8080/api/tasks/v1/1 -H 'Content-Type: application/json' -d '{"status":"running"}'
# 最多同时运行 2 个任务（管理员）
api -X PUT http://localhost:8080/api/tasks/v1/capacity -H 'Content-Type: application/json' -d '{"maxConcurrent":2}'
# 失败任务手动重试（复用原记录从失败页续采）/ 已结束任务重新运行（复用原记录、统计清零、从第 1 页）
api -X POST http://localhost:8080/api/tasks/v1/3/retry
api -X POST http://localhost:8080/api/tasks/v1/3/rerun

# 同步采集（脚本用，阻塞到任务结束）
api -X POST http://localhost:8080/api/sources/v1/1/crawls \
  -H 'Content-Type: application/json' -d '{"mode":"full"}'
api -X POST http://localhost:8080/api/crawls/v1 \
  -H 'Content-Type: application/json' -d '{"mode":"incremental"}'
api 'http://localhost:8080/api/videos/v1?page=1&size=20'
api 'http://localhost:8080/api/categories/v1'
api 'http://localhost:8080/api/videos/v1?category_id=1'
api 'http://localhost:8080/api/meta/v1/fields'
```

## 前端开发

```bash
cd web
npm install
npm run dev      # Vite 开发服务器，/api 代理到 :8080
npm run build    # 产物输出到 web/dist，由 Gin 托管
```

页面在登录后才展示：**第一个登录的账号自动注册为管理员**，之后管理员可在「用户管理」新增用户、设置角色/启停、勾选该用户可见的站点；普通用户登录后只能看到被授权的站点与数据，站点增删改、系统设置与用户管理入口自动隐藏。顶部显示当前账号与角色，可修改密码或退出登录；令牌失效（401）时自动回到登录页。

页面分六块：**站点配置**（列表右上角可**导出 JSON / 导入 JSON**：导出下载带时间戳的配置文件，导入先选文件、预览站点清单再确认，按站点名称 upsert 并回报新增/更新/失败统计；列表页点「+ 新增站点」或行内「编辑」打开弹窗，支持 Esc / 点击遮罩 / 关闭按钮收起；增删改、启用开关、**定时增量（cron 表达式 + 常用预设、输入时实时校验与中文含义 / 接下来 3 次触发时刻，列表显示下次执行时刻）**；顶部「采集任务」卡片显示运行/排队数（并发上限在「系统设置」里配置），以及每个任务的**实时进度条**——百分比、当前页/总页数、已采条数、**拉取 / 新增 / 更新 / 未变**统计、速率、预估剩余时间、第几次尝试、等待重试倒计时；运行中可**暂停**、已暂停可**继续**，一键批量采集全部站点；站点行内也可直接发起全量/增量或暂停）、**资源库**（采集到的影片以海报卡片展示，每张卡片显示**资源名 + 海报图 + 更新集数**，图片加载失败自动退化占位；顶部可按**视频名称**检索、按**分类 / 地域 / 年份 / 站点**筛选、按**更新时间**或**评分**升降序排序，列表**滚到底自动加载下一页**（无限滚动，不显示页码））、**视频分类**（按资源站分组的可折叠三级树：站点 → 一级分类 → 二级分类，显示分类数 / 影片数、一键展开收起，父分类未采到的二级归入「未归类」，分类名称本身即可点击（鼠标悬停有下划线提示），直接按该分类跳到影片列表，一级分类会包含其所有子分类）、**采集记录**（每次采集的统计与状态，运行中的记录用同一份实时数据展示进度条与**拉取 / 新增 / 更新 / 未变**；`已暂停` 可「继续」，`失败` 可「重试」（**复用原记录**从失败页续采、统计累加）或「重新运行」（**复用原记录**、从第 1 页重来、统计清零），`已暂停` 可「继续」（同样复用原记录）；三者都不会多出一条记录，页面会显示尝试次数）、**系统设置**（管理员专属：在线编辑采集并发上限、单次超时、最大页数、每页条数、**每页间隔**、增量时间窗、失败重试与退避、进度落库间隔、请求 UA 等，保存即生效并写回配置文件，含逐字段范围提示与未保存标记；被环境变量接管的字段标为只读；底部展开可看监听地址、数据库路径等只读启动项）、**用户管理**（管理员专属：账号表单、角色/启停、可见站点勾选列表与账号列表）。

**采集数据按需刷新**：1.5 秒的自动轮询只在「站点配置」和「采集记录」页签运行（分别刷 `tasks` + `sources`、`jobs`），其它页签停留期间不会为了进度反复请求接口或重绘列表；切换回来会立即补一次最新数据。

资源库点开卡片进入资源详情：**顶部标题栏始终保持「资源库」**（详情是资源库的下钻页，不切换成影片名；影片名在详情页头部展示）；**打开即从页面顶部开始**（不会继承列表的中段滚动位置、无需再手动往上滚，标题与播放器一眼可见），顶部是播放器，下方是**播放组（线路）**与**剧集**选择，切换即换源；m3u8 优先用 hls.js 播放（原生 HLS 仅在明确支持的浏览器兜底），mp4/webm 直链直接播放，无后缀地址先按 HLS 试播再回退直链，播放失败给出可读原因并保留「在新窗口打开」入口。站点配置了**视频解析地址**时，播放地址已拼成解析页地址，改用 `iframe` 承载解析页面播放，地址行会标注「已拼接解析地址 / 播放域名已替换」。另有「标准字段」「扩展字段」两个页签，可按 14 个分组查看全部 91 个标准字段（支持「只看非空」）与站点自定义字段。**返回时仍回到点开影片前的原滚动位置**：点「返回资源库」或浏览器后退都恢复原处、不丢浏览进度（进入详情滚到顶部与返回恢复原位置互不干扰；搜索 / 切筛选仍照常回到顶部）。`web/dist` 已随仓库提供，clone 后无需构建前端即可运行；未知前端路由由 Gin 回退到 `index.html`。

**界面结构与样式约定**：顶部是吸顶双行栏（品牌标识 + 数据库 / 账号 / 角色操作，第二行为带计数的页签），下方四张概览卡把「可见站点、可见影片、采集任务、字段清单」四个关键指标前置；正文统一为圆角卡片，界面保持精简：不铺规则说明 / 解释性文案，只保留必要的空状态、任务与播放诊断、字段说明与只读数据（如系统设置的「启动项」仍用 `<details>` 折叠展示）。全部样式集中在 `web/src/App.vue` 的 `<style>` 段，配色与圆角由 `:root` 里的 CSS 变量（`--brand` / `--border` / `--text` / `--muted` / `--radius` / `--shadow-sm` 等）统一收口，改主题只需换这几个值；表格为圆角容器 + 粘性表头，超出高度在容器内滚动。资源库列表用「哨兵元素 + `IntersectionObserver`」做底部无限滚动（提前 600px 触发），搜索 / 切筛选时在视口中央显示转圈遮罩并回到顶部，加载下一页时底部显示「加载更多…」，到底显示「已经到底了」；筛选栏与列表底部都不展示命中总数，接口只返回 `items / page / size`，前端按「本页条数 >= 分页条数」判断是否还有下一页；连续改变筛选时用请求序号丢弃过期响应，避免旧结果覆盖新结果。

**手机端适配**：页面按 `@media (max-width: 760px)` 做响应式——手机上页签单行横滑、表单与筛选控件整行、按钮铺满宽度，任务操作区（全量 / 增量）改为整列堆叠，系统设置的字段网格收成单列，卡片内边距与字号收紧；三个表格隐藏次要列（任务 ID / 速率 / 已运行 / 接口地址 / 上次采集 / 游标 / 模式 / 开始时间 / 说明），保留名称、状态、实时进度、拉取/新增/更新/未变与操作，剩余列可在卡片内横向滑动查看；资源卡片按窄屏自动降为 2–3 列；资源详情与播放器弹窗在手机上全屏，播放器高度按 56vh 收敛。已用 390px 视口逐页截图核对无横向溢出。

## 目录结构

```
.
├── .github/workflows/deploy.yml   # push → main 自动部署工作流
├── deploy/
│   ├── deploy.sh                 # 幂等部署脚本：初始化 / 启动 / 探活 / 失败回滚
│   ├── assetspider.service       # systemd 单元模板
│   └── known_hosts               # 服务器 SSH 主机公钥（防中间人）
├── conf/config.yaml              # 运行配置
├── internal/
│   ├── router/                   # 第 1 层：按 /api/{模块}/v1 分组的路由表（只依赖 controller）
│   ├── controller/               # 第 2 层：HTTP 适配（只依赖 service / utils）
│   ├── service/                  # 第 3 层：业务逻辑（只依赖 repository）
│   │   ├── asset.go / source.go / video.go / category.go / job.go / health.go / meta.go
│   │   ├── auth.go               # Principal（登录主体/可见站点）、认证与用户管理业务规则
│   │   ├── crawl.go              # 全量 / 增量采集编排、分类归档、游标与去重
│   │   ├── scheduler.go          # 按站点 cron 到点触发增量任务
│   │   ├── settings.go           # 系统设置：采集运行时配置的读写编排
│   │   ├── errors.go             # 业务错误语义
│   │   └── collector/            # 采集协议客户端（零丢失 JSON 解析、URL 拼装、实体转换）
│   ├── repository/               # 第 4 层：每个实体一个文件 / 对象 / 接口
│   │   ├── store.go              # 连接、迁移、装配 Repositories、Path / Close
│   │   ├── errors.go             # 共用的 ErrNotFound（包装 GORM 错误）
│   │   ├── visibility.go         # 各实体共用的可见站点白名单过滤
│   │   ├── user.go               # UserRepository → User / Session / UserSource
│   │   ├── asset.go              # AssetRepository      → model.Asset
│   │   ├── source.go             # SourceRepository     → model.Source
│   │   ├── video.go              # VideoRepository      → model.Video（全字段 upsert）
│   │   ├── category.go           # CategoryRepository   → model.Category（自动归档）
│   │   ├── tag.go                # TagRepository        → model.Tag / VideoTag（自动归档、多对多）
│   │   ├── job.go                # JobRepository        → model.CrawlJob
│   │   └── *_test.go             # 每个实体各一份测试 + store_test.go
│   ├── model/                    # 横切：GORM 模型
│   │   ├── video.go              # 全字段 Video 实体（含 CategoryID）
│   │   ├── user.go               # User / Session / UserSource（认证与站点授权）
│   │   ├── category.go           # Category 实体（按站点隔离）
│   │   ├── tag.go                # Tag 实体 + VideoTag 多对多连接表
│   │   └── video_fields.go       # VodFields 字段清单（唯一数据源）
│   ├── config/                   # 横切：配置加载、校验与运行时热更新
│   │   ├── config.go             # YAML 加载、env 覆盖、Config.Validate
│   │   └── store.go              # config.Store：线程安全快照 + 在线更新写回 YAML（保留注释）
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

`go test ./...` 覆盖**登录认证与站点权限端到端**（首用户自动管理员、401/403、A/B 站隔离、授权即时生效、停用/删除/改密后会话失效、越权采集 404、bcrypt 与令牌摘要落库、三张认证表自动迁移）、**四层依赖方向守卫**、**utils 横切纯洁性守卫**、**仓储层一实体一对象守卫**、**`/api/{模块}/{版本}` 路由规范守卫**、配置加载、工具函数（含 **5 段 cron 解析 / 匹配 / 下次触发时刻**；前端 `npm test` 另覆盖 **cron 实时校验、中文含义与接下来几次触发时刻**）、站点/影片/任务/**分类** CRUD、**站点定时配置（保存 / 非法 cron 拒绝 / 下次触发时刻 / 定时调度到点触发、同分钟去重、已有任务跳过）**、**系统设置与配置热更新（在线改采集配置 / 写回 YAML 保留注释与补齐缺失键 / 重启恢复 / 非法值与并发越界拒绝 / 环境变量接管字段只读 / Fetcher 与限流器即时生效 / 设置接口管理员权限 / 前端字段校验与提交载荷纯函数单测）**、**每页间隔（默认 0 不限速、环境变量覆盖并锁定、越界与负值校验、在线改下一页即生效、暂停 / 停机时 `ctx` 取消立即中断等待、写回 YAML 与重启恢复、前端 0 合法与范围校验）**、分类自动创建与按站点隔离、分类幂等与改名、分类影片数与按分类筛选、**站点配置 JSON 导入导出（导出只含可编辑字段、三种 JSON 形态、按名称 upsert 不产生重复、部分失败逐条上报、非法 JSON / 超大文件 / 普通用户越权）**、**站点名称全系统唯一（手工新增 / 编辑重名返回 409，忽略大小写与首尾空白、改名成自身允许）**、**二级分类（苹果CMS type_pid / type_id_1 父子关联、纯容器父级从 type_id_1 推断预建（修复真实站点二级分类沦为永久孤儿）、父级缺失暂存孤儿、父级出现后自动挂接）**、**分类定义通用探测（`ac=typelist` 分页硬上限 3 页、接口不可用时降级到「站点显式 ac → list → videolist」只取 `class[]`、影片 `list[]` 不混入分类、优先站点显式 ac、探测 URL 保留认证参数并移除增量/分页参数、主路径成功不额外发请求；全程无站点特判）**、**批量写入（一次 SELECT 分组 + CreateInBatches + ON CONFLICT 批量更新；New/Updated/Unchanged 统计、批内重复、跨站点同 vod_id、批量更新字段不串位、首见时间保留；用 SQL 计数断言 50 条新增/未变各仅 2 句、40 条首采归档仅 7 句；标签/地域/分类批量归档与实体数校验）**、去重 upsert、**字段清单与实体一致性**、**全字段零丢失解析（数字/字符串/null/对象/数组/自定义字段）**、**播放组解析（多线路 `$$$`/`#`/`$` 分隔符、默认/线路N 命名、无 `$` 回退）**、**播放字段兼容（别名键 `vod_playurl`/`v_playdata` 等、结构化 JSON 数组、无 `vod_id` 稳定伪 ID）与播放地址诊断（自动切换 `ac=detail` 补齐播放数据、显式 `ac=list` 的排查提示、`noPlayUrl` 统计）**、**全字段指纹与全字段写回**、URL 拼装、**`code != 1` 拒绝解析**、全量/增量/游标/去重/失败回写，**资源检索（名称 / 地域筛选、按更新时间与评分升降序排列、地域枚举与影片数）**、**地域自动归档（按 vod_area 拆分、按站点隔离、幂等与关联替换、老库升级回灌）**、**年份筛选（等值过滤、年份聚合与可见性、空年份不进下拉）**、**采集任务调度（异步入队、并发上限运行时调整、运行中暂停、按续采页码继续、同站点互斥）**、**失败自动重试（退避等待、失败页续采、等待期间可暂停）**、**继续 / 重试 / 重新运行（三者均复用原记录、不新开任务；继续与重试的统计和尝试次数累加，重新运行清零后从第 1 页重来）**、**实时进度与统计（当前页/总页数、百分比、速率、剩余时间、重试倒计时、拉取/新增/更新/未变逐页刷新、运行中记录带 live 进度）**、**运行中定期落库（进度与续采页滚动写库、只写进度不动终态、结束后续采页归 1）**、**重启自动恢复（模拟进程重启后自动复用原记录从续采页继续、统计与尝试次数累加、手动暂停的任务不动、不新开记录）**、**地址改写（解析地址前缀拼接与 `{url}` 占位符、播放域名替换保留路径与查询串、先换域名再拼解析、列表与播放组一致改写、拼接解析地址后给分集打 frame 标记、未配置解析地址时不打标记、封面/缩略图/幻灯片/原图与 `#` 截图集合逐段图片改写、原始地址不被污染、配置校验与规范化回读）**，以及通过 HTTP 接口触发采集与字段元数据/详情/分类（含二级树）/播放组/地域与排序检索/任务管理接口的完整链路。采集测试用本地 HTTP 模拟站点，不依赖外网。

HTTP 链路测试位于 `internal/router`（装配后打真实请求，同时校验新路由结构与旧路由已下线），采集编排测试位于 `internal/service`，协议解析测试位于 `internal/service/collector`，纯工具测试位于 `internal/utils`；`internal/repository` 下按实体拆为 `asset_test.go` / `source_test.go` / `video_test.go` / `job_test.go` / `user_test.go`，另有 `store_test.go` 校验连接与各对象装配。HTTP 链路测试会自动注册首个管理员并让所有请求携带令牌，真实模拟登录后访问。SQLite 使用纯 Go 的 modernc.org/sqlite 驱动（经 github.com/glebarez/sqlite 接入 GORM），无需本机 gcc，测试直接运行即可（`CGO_ENABLED=0`）。

端到端可用模拟站点验证：91 个标准字段全部落库、`extra` 保留自定义字段与嵌套对象（不再存整条 `raw`）、只改一个冷门字段也能触发更新、全量/增量/去重/幂等/批量采集、按站点自动分类归档与 Vue 页面托管均正常。

## 自动部署（推送到 GitHub → 服务器）

**推送到 `main` 分支即可自动部署到 `140.238.43.173` 并启动服务**，全程无需登录服务器。

```
git push origin main
   └─ GitHub Actions (.github/workflows/deploy.yml)
        ├─ 检出代码，SSH 探测服务器 CPU 架构（amd64 / arm64 自适应）
        ├─ CGO_ENABLED=0 静态编译 Go 二进制 + npm ci && npm run build 构建 Vue
        ├─ 打成自包含发布包 tar.gz（二进制 / web/dist / 配置样例 / systemd 单元 / 部署脚本）
        ├─ scp 上传到服务器 /tmp
        └─ ssh 执行 deploy/deploy.sh
             ├─ 首次：建目录、生成 conf/config.yaml、安装并 enable systemd 服务
             ├─ 备份旧版本 → 原子替换二进制与前端 → 重启服务
             ├─ 轮询 /api/system/v1/health 探活（默认最多 30 秒）
             └─ 失败自动回滚到上一版本；成功清理过期备份（保留 5 份）
```

设计要点：**构建发生在 CI runner 上**，服务器不需要 Go / Node，也不需要 `git pull` 访问 GitHub（避免国内服务器拉取不稳定）。SQLite 使用纯 Go 驱动，二进制静态链接，交叉编译到 amd64 / arm64 都能直接运行。发布包自包含，手动部署同样只需一个文件。`data/`（数据库）与 `conf/config.yaml`（用户配置）**永不被部署覆盖**。

### 一次性配置

**① 生成部署专用密钥，并把公钥放到服务器**（私钥只填到 GitHub，绝不要提交进仓库）：

```bash
ssh-keygen -t ed25519 -C "assetspider-deploy" -f ~/.ssh/assetspider_deploy -N ""
ssh-copy-id -i ~/.ssh/assetspider_deploy.pub ubuntu@140.238.43.173
cat ~/.ssh/assetspider_deploy        # 全文复制，填到 Secret DEPLOY_SSH_KEY
```

**② 在仓库 Settings → Secrets and variables → Actions 添加：**

| 类型 | 名称 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| Secret | `DEPLOY_SSH_KEY` | ✅ | — | 上面生成的**私钥全文**（含 BEGIN / END 行） |
| Secret | `DEPLOY_KNOWN_HOSTS` | 可选 | `deploy/known_hosts` | 服务器 SSH 主机公钥；换服务器时必须更新，用于防中间人 |
| Variable | `DEPLOY_HOST` | 可选 | `140.238.43.173` | 服务器 IP |
| Variable | `DEPLOY_USER` | 可选 | `ubuntu` | SSH 用户：OCI Ubuntu 为 `ubuntu`，Oracle Linux 为 `opc`，也可 `root` |
| Variable | `DEPLOY_PORT` | 可选 | `22` | SSH 端口 |
| Variable | `DEPLOY_PATH` | 可选 | `/opt/assetspider` | 部署根目录 |
| Variable | `DEPLOY_MODE` | 可选 | `auto` | `auto` / `systemd` / `process`（无 systemd 时降级为 nohup 进程） |
| Variable | `HEALTH_URL` | 可选 | 自动推导 | 改了服务端口时建议显式填写完整健康检查地址 |

> 未配置 `DEPLOY_SSH_KEY` 时，工作流**只做构建校验、跳过部署**（保持绿色并给出警告）；配好后可在 Actions 页面点 **Run workflow** 手动重跑，不必空推一次。纯文档改动（`**.md`）不触发部署，`workflow_dispatch` 可随时手动触发；多次 push 串行执行，不会并发写同一台服务器。

**③ 服务器前置条件**

- systemd（Ubuntu 20.04+，脚本用 `systemctl` 管理进程并设置开机自启）
- SSH 用户具备免密 `sudo`（OCI 镜像默认满足），或直接使用 root
- 云安全组与主机防火墙放行 **22**（GitHub Actions runner 入站）与 **8080**（对外访问页面）；Oracle Cloud 需在控制台 Security List / NSG **和**主机 iptables 两处都放行
- 已安装 `curl`、`tar`（Ubuntu 默认自带）

### 首次部署与验证

配置完成后 `git push origin main`，或到 Actions →「自动部署到生产服务器」→ **Run workflow**。工作流成功即表示服务已启动并通过健康检查。

```bash
ssh ubuntu@140.238.43.173
sudo systemctl status assetspider            # active (running)
cat /opt/assetspider/VERSION                 # 当前部署的 commit sha
curl http://127.0.0.1:8080/api/system/v1/health
# 浏览器访问 http://140.238.43.173:8080 ，第一个登录的账号自动成为管理员
```

部署后的目录：

```
/opt/assetspider/
├── bin/assetspider              # 静态二进制
├── web/dist/                    # Vue 前端
├── conf/config.yaml             # 运行配置（仅首次生成，之后不会被覆盖）
├── data/assetspider.db          # SQLite 数据库（永不覆盖）
├── logs/assetspider.log         # 服务日志
├── releases/backup-*            # 最近 5 个可回滚版本
├── VERSION                      # 当前 commit sha
└── run.pid                      # 仅 process 模式
/etc/systemd/system/assetspider.service
/etc/assetspider.env             # 端口等运行参数，改完 restart 生效（不会被部署覆盖）
```

### 日常运维

```bash
sudo systemctl status assetspider                  # 状态
sudo systemctl restart assetspider                # 重启
sudo systemctl stop assetspider                   # 停止
tail -f /opt/assetspider/logs/assetspider.log     # 实时日志
sudo journalctl -u assetspider -n 100 --no-pager  # systemd 日志
cat /opt/assetspider/VERSION                      # 当前版本

# 改端口：编辑 env（部署不会覆盖它），再重启；记得同步 Variable HEALTH_URL 与防火墙
echo 'ASSETSPIDER_SERVER_ADDR=:9090' | sudo tee /etc/assetspider.env
sudo systemctl restart assetspider

# 手动回滚：从备份恢复二进制后重启
ls -1t /opt/assetspider/releases/
sudo cp /opt/assetspider/releases/backup-XXXX/assetspider /opt/assetspider/bin/assetspider
sudo systemctl restart assetspider
```

**不走 GitHub 的手动部署**：按 `deploy.yml` 里「构建自包含发布包」步骤本地打一个 tar.gz（或直接使用 CI 产物），上传到服务器执行：

```bash
scp assetspider-release.tar.gz ubuntu@140.238.43.173:/tmp/
ssh ubuntu@140.238.43.173
mkdir -p /tmp/as && tar -xzf /tmp/assetspider-release.tar.gz -C /tmp/as
bash /tmp/as/deploy/deploy.sh /tmp/assetspider-release.tar.gz
```

### 故障排查

| 现象 | 排查方向 |
| --- | --- |
| 工作流跳过部署，日志有 warning | 未配置 Secret `DEPLOY_SSH_KEY`，或变量名写错 |
| `tar: deploy/deploy.sh: Not found in archive` | 老版本工作流按成员名从发布包里挑单个文件，归档成员带 `./` 前缀导致匹配不到；已改为全量解包。若手动打包，请勿用 `tar -C <dir> .`，改用显式列出 `bin web conf deploy VERSION` |
| `Permission denied (publickey)` | 公钥没进服务器 `~/.ssh/authorized_keys`；`DEPLOY_USER` 与公钥所属用户不一致；私钥粘贴时缺 BEGIN / END 行 |
| `Host key verification failed` | 服务器重装或换 IP：更新 Secret `DEPLOY_KNOWN_HOSTS` 或仓库 `deploy/known_hosts` |
| `Connection timed out` | 云安全组 / iptables 未对 GitHub runner 放行 22 端口 |
| 部署成功但页面打不开 | 8080 未在云控制台与主机防火墙双向放行 |
| 健康检查失败并自动回滚 | 看 `logs/assetspider.log`；常见是 8080 被占用，或 `/etc/assetspider.env` 端口与 `HEALTH_URL` 不一致 |
| `sudo: a password is required` | 部署用户没有免密 sudo：改用 root，或设置 `DEPLOY_MODE=process` |
| 服务能启动但采集异常 | 检查 `conf/config.yaml` 与 `data/` 权限；两者不会被部署覆盖 |

> 安全建议：为部署单独生成密钥对并限制权限，不要复用个人私钥。`deploy/known_hosts` 是公开的主机公钥（非敏感信息）；私钥、`.env` 与数据库均已被 `.gitignore` 排除，切勿提交。

## 计划

- [ ] 支持 XML 格式采集接口
- [ ] 定时自动增量采集（cron）
- [ ] 采集结果导出（CSV / JSON）与站点分类绑定
