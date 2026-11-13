# 架构与目录

> 项目总览：[README](../Readme.md) · 相关：[采集能力](collecting.md) · [配置](configuration.md)

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
| 4 仓储 | `internal/repository` | 每个实体一个文件、一个对象、一个接口；GORM 读写（SQLite / MySQL 双后端，方言分支集中在 `store.go` 与 `video_search_mysql.go`）、事务与级联清理 | 仅 `model` |

仓储层按实体严格拆分，不允许把不同实体的方法揉进同一个对象：

| 实体 | 文件 | 对象 / 对外接口 | 方法 |
| --- | --- | --- | --- |
| `Asset` | `repository/asset.go` | `AssetRepository` | `List` / `Create` / `Delete` |
| `Source` | `repository/source.go` | `SourceRepository` | 站点 CRUD、`ListEnabledSources`、`UpdateSourceResult`、级联删除、清空站点数据 |
| `Video` | `repository/video.go` | `VideoRepository` | `UpsertVideos`、`GetVideo`、`ListVideos`（返回 `model.VideoCard` 卡片投影）、`CountVideosBySource` / `ByCategory` |
| `Category` | `repository/category.go` | `CategoryRepository` | `EnsureForVideos`、`List`、`Get` |
| `CrawlJob` | `repository/job.go` | `JobRepository` | `CreateJob` / `FinishJob` / `ListJobs` |
| `User` / `Session` / `UserSource` | `repository/user.go` | `UserRepository` | 账号 CRUD、会话签发/校验/吊销、站点授权读写 |

`repository/store.go` 只是容器：`Repositories` 共享一条 `*gorm.DB`，负责连接、自动迁移、装配各实体对象与 `Path` / `Close`，**不承载任何实体读写方法**；`errors.go` 放各对象共用的 `ErrNotFound`，`visibility.go` 放各实体共用的「可见站点白名单」过滤规则，`video_search.go` / `video_bigram.go` 是 SQLite 检索层共用的索引定义、查询条件与历史数据回灌，`video_search_mysql.go` 是 MySQL 后端的 ngram FULLTEXT 检索定义与后台建索引，`dialect.go` 是方言识别 / DSN 规范化 / 连接池配置（业务代码只认 GORM 标准 API，SQLite 专有的 PRAGMA / FTS5 / bigram 与 MySQL 专有的 FULLTEXT 都收敛在此处，互不串门），`crawl_cache.go` / `category_cache.go` 是采集会话类目缓存与分类缓存归档路径（同样是包级函数 / 实体方法，不挂在容器上）；评分排序与其表达式索引已移除（`video_sort_index.go` 已删除），启动路径不再有同步 `CREATE INDEX`；`video_maintenance.go` 把分钟级的 bigram 历史回灌收敛成启动后的后台任务，保证服务立即开始监听（MySQL 后端的 FULLTEXT 建索引同样在后台异步进行）。列表走 `model.VideoCard` 卡片投影（只 SELECT 卡片字段），完整 `model.Video` 由 `GetVideo` 在用户点开影片时按 id 返回。测试同样按实体拆为 `asset_test.go` / `source_test.go` / `video_test.go` / `job_test.go` / `user_test.go`，`store_test.go` 只测连接与装配，`mysql_search_test.go` 用 DryRun 校验 MySQL 检索 SQL。

**采集写入采用批量策略**（避免逐条 SQL 往返拖慢大站采集）：

- `UpsertVideos` 对整批按 `(source_id, vod_id)` **只发 1 次 SELECT** 取回已存在行，在内存里分成「新增 / 未变 / 已变」三组：新增走 `CreateInBatches`，已变走 `INSERT … ON CONFLICT DO UPDATE` 批量更新业务列（保留 `first_seen_at` / `created_at`，FTS5 / bigram 行级触发器逐行同步两套倒排索引），未变只按 `id IN` 批量刷 `last_seen_at` 并按需补历史数据缺失的 `category_id` / `vod_play_list`。全程仍在单事务内，atomic 不变；`New / Updated / Unchanged` 统计、批内重复 (后写覆盖前写)、跨站点同 `vod_id` 互不干扰等语义全部保留。
- 分类 / 标签 / 地域的 `EnsureForVideos` 同样改为「一次批量查询取回已存在 → 只把缺失项 `CreateInBatches`」，替代原先按名逐条 find-or-create。
- 实测（临时库、GORM SQL 计数）：50 条纯新增 **2 条 SQL**（旧 ~100）、50 条全未变 **2 条**（旧 ~50）、40 条影片首采归档（标签/地域/分类各 40 新值）**7 条**（旧 ~240）；5000 条批量写入约 **2341 条/秒**、全未变重复扫描约 **12141 条/秒**。SQL 语句数从 O(N) 降为 O(N/批量 + 常数)。

**采集会话类目缓存**解决的是「页与页之间」的重复：批量化只压掉了一页内 N 条的往返，但大站采集动辄上千页，每页仍会重复查同一批分类 / 标签 / 地域、重复开事务、把没变化的分类再 UPDATE 一遍，SQLite 单写者的事务提交会成为主要开销。

- `CrawlCache`（`crawl_cache.go`）在任务开始时一次性 `List` 本源的 `categories` / `tags` / `areas` 建内存索引，之后每页的 `EnsureForVideos` 只做内存比对：**命中且无变化就 0 条 SQL、0 个事务**，只有真正新增或改名 / 补父级才批量写回并同步缓存。分类的父子归树与孤儿回挂（`category_cache.go`）也在内存里算出差异，不再每页全表扫描 + 逐个查父级。
- `UpsertVideos` 顺手把解析出的 `vod_id → 主键` 写进缓存，`MapVodIDs` 直接命中，省掉「刚入库又按 vod_id 查一次主键」的往返；`LinkVideos` 改为「读现状 → 算差异 → 只写差异」，关联没变的页不再无条件 `DELETE` + `INSERT`，语义仍是幂等、可自愈。
- 缓存生命周期是一次 `crawl`（含断点续采的每一段），按 `sourceID` 独立、不跨任务共享、无需加锁，任务结束即丢弃、下次任务重新读库。所有仓储方法都以**可选变参**接收缓存，不传就走原查库路径；`crawl_cache_test.go` 用两个独立库逐页比对，保证两条路径结果逐字一致。
- 实测（临时库、GORM SQL 计数）：类目全部命中的重复页，三次 `EnsureForVideos` **0 条 SQL**（逐页查库 4 条），整页链路 **4 条 / 9 条**。

约束的具体落地：

- 每个 service 只注入自己需要的实体仓储**接口**（如 `AssetService` 拿 `AssetRepository`，`CrawlService` 拿 `SourceRepository` + `VideoRepository` + `JobRepository`），看不到 `*gorm.DB`；GORM 的 `ErrRecordNotFound` 在仓储层翻译为 `repository.ErrNotFound`，服务层再翻译为 `ErrInvalid / ErrUnauthorized / ErrForbidden / ErrNotFound / ErrConflict` 五个哨兵错误，控制器据此决定 400 / 401 / 403 / 404 / 409 / 500（采集接口默认 502）。
- 采集协议客户端放在 `internal/service/collector`（service 层内部子包），只做 HTTP 抓取、JSON 零丢失解析与实体转换；原 `collector.Runner` 的编排逻辑（分页、去重入库、任务与游标回写）已上移到 `CrawlService`，不再直接访问数据库。
- `internal/model`（实体）、`internal/config`（配置）、`internal/utils`（工具）是横切包，不属于四层，也不参与调用链；`internal/app` 是依赖装配层（composition root），只负责自底向上 `new` 出各层对象并注入，不发起任何业务调用；`main.go` 只做配置加载、装配与启动。
- `internal/utils` 是新增的 utils 层，统一存放与业务无关的纯工具：`request.go`（路径/query 参数解析）、`json.go`（JSON 值字符串化）、`time.go`（时间归一化）、`hash.go`（内容指纹）、`env.go`（整数环境变量）。它可被任意一层引用，但自身**不允许依赖任何 `internal/*` 业务包**（由架构测试强制）。
- 根目录 `architecture_test.go` 会解析四层生产代码的 import，自动校验依赖方向、utils 的横切纯洁性、仓储层「一实体一对象」，以及「Gin 不出现在 service/repository、GORM 不出现在 service/controller/router」；`internal/router/router_test.go` 还会遍历 Gin 路由表，强制 `/api/{模块名}/{版本号}` 结构且路径里不含动词。任何违规都会让 `go test ./...` 失败。

## 文件规模

**单个源文件不超过 800 行，单个函数不超过 250 行**（`.go` / `.vue` / `.js` / `.ts`），由根目录 `size_test.go` 的两个测试强制（`node_modules` / `dist` / `vendor` 不参与）：

| 测试 | 约束 | 检查方式 |
| --- | --- | --- |
| `TestSourceFilesStaySmall` | 文件 ≤ 800 行 | 统计换行数 |
| `TestFunctionsStaySmall` | 函数 ≤ 250 行 | Go 用 `go/ast` 取 `FuncDecl` / `FuncLit` 起止行；Vue/JS 用轻量词法扫描识别 `function f(){}`、方法简写与箭头函数，跳过注释、字符串、模板串与正则 |

行数只是代理指标，真正约束的是职责：超长文件通常意味着多个关注点挤在一起，超长函数通常意味着多个步骤串在一起，改动互相牵连、review 也看不出边界。超限时按领域拆文件、按步骤拆函数，而不是压缩格式。

- Go 侧：测试按领域拆（`router_test.go` / `source_test.go` / `video_test.go` / `category_test.go` / `task_test.go`，共用的请求构造与轮询 helper 放 `helpers_test.go`）；`service/task.go` 按「运行时状态 / 执行编排 / 对外 API」拆为 `task_state.go` + `task_run.go` + `task.go`。
- Vue 侧：`App.vue` 只保留布局骨架（登录页 / 侧边栏 / 主内容区 / 全局弹窗）与装配，样式抽到 `web/src/styles/app.css`，业务按领域落到 `web/src/app/`（见下）；详情页再拆出 `app/player.js`，播放器状态与挂载逻辑不挤在详情里。

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
│   │   ├── task.go               # 任务对外 API：入队 / 暂停 / 继续 / 重试 / 重跑 / 列表 / 容量
│   │   ├── task_state.go         # crawlTask 运行时状态机（协作式暂停、进度快照）
│   │   ├── task_run.go           # 任务执行：分页抓取、失败退避重试、进度落库、站点串行闸门
│   │   ├── scheduler.go          # 按站点 cron 到点触发增量任务；顺带检查订阅是否到期
│   │   ├── subscription.go       # 站点配置订阅：增删改查、到期判定、刷新结果汇总
│   │   ├── subscription_sync.go  # 订阅同步：拉取 → 解析 → 按接口地址 upsert 站点 → 停用已移除
│   │   ├── source_bundle.go      # 站点配置 JSON 解析（文件导入与订阅共用同一套）
│   │   ├── settings.go           # 系统设置：采集运行时配置的读写编排
│   │   ├── errors.go             # 业务错误语义
│   │   ├── errtext.go            # 采集失败原因归类（超时 / DNS / 拒绝连接 / 非 JSON…）
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
│   │   ├── crawl_cache.go        # 采集会话类目缓存（分类/标签/地域一次性预读）
│   │   ├── category_cache.go     # 分类缓存归档路径（父子归树纯内存算差异）
│   │   ├── job.go                # JobRepository        → model.CrawlJob
│   │   ├── subscription.go        # SubscriptionRepository → model.Subscription
│   │   └── *_test.go             # 每个实体各一份测试 + store_test.go
│   ├── model/                    # 横切：GORM 模型
│   │   ├── video.go              # 全字段 Video 实体（含 CategoryID）
│   │   ├── user.go               # User / Session / UserSource（认证与站点授权）
│   │   ├── category.go           # Category 实体（按站点隔离）
│   │   ├── subscription.go       # Subscription 实体（订阅地址 / 间隔 / 刷新状态）
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
│   │   ├── App.vue               # 外壳：登录页 / 侧边栏 / 主内容区 / 全局弹窗（只做装配）
│   │   ├── app/                  # 业务按领域拆分的模块（共享同一个 ctx）
│   │   │   ├── index.js          #   createApp()：按顺序把各模块挂到 ctx 上
│   │   │   ├── state.js          #   共享状态（ref / computed）+ 本地存储读写
│   │   │   ├── client.js         #   fetch 封装：令牌、错误、401 登出
│   │   │   ├── lookup.js         #   纯展示工具（时间 / 状态 / 标签 / 进度格式化）
│   │   │   ├── videos.js         #   资源库：筛选联动、分页、无限滚动
│   │   │   ├── catalog.js        #   分类树 / 分类与标签下拉标签
│   │   │   ├── detail.js         #   详情页：全字段资料、播放组解析、hash 路由
│   │   │   ├── player.js         #   播放器：线路 / 选集、hls.js / iframe / 直链
│   │   │   ├── sources.js        #   站点配置、JSON 导入导出、cron 校验反馈
│   │   │   ├── subscriptions.js  #   站点订阅：新增 / 编辑 / 手动刷新 / 启停
│   │   │   ├── tasks.js          #   采集任务与采集记录
│   │   │   ├── settings.js       #   系统设置
│   │   │   ├── users.js          #   用户管理
│   │   │   ├── auth.js           #   登录 / 退出 / 改密 / 启动探测
│   │   │   └── shell.js          #   页签与 hash 路由、轮询、Esc、侧边栏
│   │   ├── views/                # 页面组件（inject('app') 取共享状态）
│   │   ├── styles/app.css        # 全局样式与 CSS 变量
│   │   └── *.js                  # 纯函数模块 + node:test 单测（cron / 筛选 / 布局…）
│   └── dist/                     # 构建产物（Gin 托管，随仓库提交）
├── main.go                       # 服务入口
├── architecture_test.go          # 分层依赖方向守卫测试
├── size_test.go                  # 文件 ≤ 800 行、函数 ≤ 250 行守卫
├── docs/                         # 分主题文档：架构 / 采集 / 配置 / API / 前端 / 测试 / 部署
├── go.mod / go.sum
└── Readme.md                     # 项目总览 + 文档导航
```
