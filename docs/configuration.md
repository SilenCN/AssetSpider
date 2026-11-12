# 配置

> 项目总览：[README](../Readme.md) · 相关：[采集能力](collecting.md) · [API](api.md)

## 系统设置：在线配置、即时生效

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

## 配置文件 `conf/config.yaml`

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

## 站点配置字段（存在数据库 `sources` 表）

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
| `subscriptionId` | 只读：站点来源订阅 ID，`0` 表示手动添加；刷新该订阅会覆盖这里的配置 |

`pg`、`h` 由采集器强制写入，不会被附加参数干扰。定时规则见 [定时增量采集](collecting.md#定时增量采集站点级-cron)；从远程地址批量同步站点的做法见 [站点配置订阅](collecting.md#站点配置订阅远程分发)。

## 站点配置 JSON 导入 / 导出

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

## 检索与索引

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

`vod_name` / `hash` 的单列索引已移除：关键词检索走下面的倒排索引，前置通配符的 `LIKE` 用不上 B-Tree；`hash` 只在 Go 侧比对内容指纹，SQL 里没有按它过滤的查询。历史库里已建出的这两个索引由启动时幂等的 `DROP INDEX IF EXISTS` 清理，**不提升 `schemaVersion`**——删索引不需要重算统计信息，更不该把 v6 的整表回灌 + `ANALYZE` 重新触发一遍。

**关键词索引（FTS5 + bigram）**：关键词检索原来是对 6 列做 `LIKE '%词%'`，前置通配符让索引彻底失效，每次搜索全表扫描。现在按长度分三条路径，参与检索的列始终一致：`vod_name / vod_en / vod_actor / vod_director / vod_class / vod_area`。

| 关键词长度 | 走哪套索引 | 实现 |
| --- | --- | --- |
| ≥ 3 字 | `videos_fts`（FTS5 trigram） | `ensureVideoSearch` 幂等建外部内容表（`content='videos'`，正文不复制第二份）+ 三条同步触发器；短语查询把任意子串匹配变成倒排查找 |
| = 2 字 | `video_bigrams` | 六列各自切成连续两字符片段，存 `(gram, video_id)` 主键倒排表；`gram = LOWER(?)` 直接命中主键 |
| 1 字 | 无 | 回退主表六列 `LIKE`。FTS5 是外部内容表、正文不落索引，绕它做 `LIKE` 要按 rowid 逐行回主表取值，实测反而比直接顺序扫主表慢约 30%（15 万行库 140 ms → 200 ms） |

trigram 把文本切成三字符片段，天然支持中文子串与英文子串；默认的 unicode61 按词切分，中文整句会变成一个词，搜「流浪」永远命中不了「流浪地球」。但它**物理上无法为 2 字查询建立片段**（实测 `MATCH '"测试"'` 恒返回 0 行），所以两字单独建 bigram 倒排表；单纯把阈值改成 2 会让两字搜索从「慢」变成「一个结果都搜不到」。

> **2 字索引的同步与回灌**：`video_bigrams` 由 `videos` 上的三条触发器维护，`AFTER UPDATE OF` 只限定六个检索列，刷新 `last_seen_at` 不会重算片段；`video_bigram_state` 记录回灌进度。启动时**只同步建表与触发器（毫秒级）**，历史数据由启动后的后台任务按主键分批（每批 5000 行）`INSERT OR IGNORE` 补齐，每批提交后写进度，被打断可续跑、不会从头重来。
>
> **为什么回灌必须异步**：一次性回灌在大库上要几分钟。曾把它放在 `repository.Open` 里同步执行，结果 54 万行的生产库部署时服务迟迟不监听，健康检查等满 180 秒后回滚，回滚又 kill 掉回灌——形成每次部署都失败、回灌永远做不完的死循环（与早期 v7 同类问题）。现在后台任务用**独立的 SQLite 连接**（不占主连接池的 `SetMaxOpenConns(1)`）、每批之间让出 30 ms、连接串统一带 `_pragma=busy_timeout(30000)`（写在 DSN 上，每个新建连接都生效），服务启动后立刻可响应，实测回灌期间在线读最大延迟 1 ms。完成前两字检索降级走 `LIKE`，结果始终正确；完成后查询侧动态读一次状态表即切到倒排表，无需重启。回灌只插入新表、不改写 `videos`，也不触发 `PRAGMA user_version` 升级。
>
> **评分排序索引**：`score_desc` / `score_asc` 用 `CAST(NULLIF(TRIM(vod_score),'') AS REAL)` 把字符串评分转成数值排序，表达式上用不了普通 B-Tree，原先只能全表扫描 + 临时 B-Tree 排序（15 万行约 500 ms）。现在启动时幂等创建两条**表达式索引** `idx_videos_score_desc` / `idx_videos_score_asc`，把排序值物化进索引，降到 1 ms 以内；两条分别对应降序 / 升序，次级排序仍是 `vod_time DESC, id DESC`，行为不变。索引由 SQLite 在 `CREATE INDEX` 时自行扫一遍现有数据填满、之后随写入自动维护，不需要回灌代码、状态表或 `schemaVersion` 升级。它必须与在线请求串行：`CREATE INDEX` 会改 `sqlite_master`，期间其它连接连 `prepare` 都会拿到 `SQLITE_BUSY`（`busy_timeout` 对 schema 锁不生效），所以放在 `Open` 里同步执行。本地 15 万行约 0.8 s，但 54 万行的生产库（慢盘）实测约 **165 秒**——健康检查险些撞上 180 秒窗口，因此部署的 `HEALTH_TIMEOUT` 已放宽到 600 秒（见[部署](deployment.md)）。索引是一次性的，建好后后续启动 `IF NOT EXISTS` 空转，健康检查回到秒级；这与分钟级、必须放后台的 bigram 回灌不是一回事。
>
> **边界与降级**：若底层 SQLite 未编译 FTS5，建表失败只打日志并整体退回 `LIKE`；bigram 表建立 / 回灌失败、表达式索引创建失败同样只降级（评分排序退回全表排序），都不影响启动。1 字关键词仍走 `LIKE` 全表扫描。
>
> **存储代价**：15 万条影片的库从 73 MB 涨到 105 MB（+43%，主要是 6 列文本的 trigram 倒排表）；在大文本量的真实库上，bigram 倒排表额外约 +12%（578 MB → 650 MB，272 万条片段，含按 `video_id` 反查的二级索引）。若要压缩，可只把真正会被搜索的列放进索引，代价是这几列以外的字段不再参与关键词检索。

**查询侧的两处配合**：

- `PRAGMA user_version` 记录检索层版本（`schemaVersion`，当前 6），升级时按版本跑一次性数据回灌（v3 地域、v5 分类父级），并整库 `ANALYZE` 一次，之后每次启动 `PRAGMA optimize`。**版本号先推进、`ANALYZE` 后执行**：分析只影响查询计划选择，失败不应让迁移「永远没完成」——否则下次启动会重跑全部整表回灌 + 全库分析，在大库上把带健康检查的部署拖死。曾短暂存在的 v7（物理删除 23 个已裁剪列）已整体回滚且从未成功提交，号段回收不复用。没有统计信息时计划器会误判——例如「多站点可见 + 时间倒序」会选「先按站点过滤再全量排序」，有了统计信息才改走「按时间索引倒序扫描 + 边扫边过滤 + 凑满 LIMIT 即停」。
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
| 关键词 2 字（bigram 索引） | 83 ms | **2~38 ms** |
| 评分排序 `score_desc` / `score_asc` | 约 500 ms | **约 1 ms**（表达式索引） |
| 1 字关键词（回退 LIKE） | 140 ms | **140 ms**（改走主表，避开更慢的 FTS 外部表 200 ms） |
| 默认列表第一页 | 0.5 ms | 0.5 ms |

> 关键词 2 字这一行的「优化后」在 578 MB / 15 万行的真实库上单独复测：低频词 **2 ms**、高频词（命中全库）**33 ms**，同一库上六列 `LIKE` 约 **250 ms**；`EXPLAIN QUERY PLAN` 确认走 `SEARCH video_bigrams USING PRIMARY KEY (gram=?)`。其余行沿用早期基准库，绝对值不直接横比。该库一次性回灌约 18 秒，改为后台执行后 `Open` 约 **1.1 秒**（含同步创建两条评分索引约 0.8 秒；此前同步回灌要 16 秒起步，生产库更是数分钟），完成后写入 `done` 状态，后续启动索引与回灌都直接跳过。评分排序在同一库上从约 500 ms 降到约 1 ms，`EXPLAIN QUERY PLAN` 确认 `SCAN videos USING INDEX idx_videos_score_desc / _asc`；建索引期间并发写入无 `SQLITE_BUSY`。

写入侧由行级触发器同步维护 FTS5 与 bigram 两套倒排索引、由 SQLite 自动维护评分表达式索引；批量 upsert 仍是 `CreateInBatches` / `ON CONFLICT` 的少量 SQL，触发器开销远小于逐行更新上百列本身。回灌用静默 logger，不会每批刷一条 SLOW SQL 日志。

## 清空站点数据（保留站点配置）

站点列表每行（管理员）都有**清空数据**按钮，对应 `DELETE /api/sources/v1/:id/data`。它只清空该站点名下的数据，**站点配置本身（名称、接口地址、格式、附加参数、解析/播放/图片地址、启用状态、增量小时数、最大页数、定时配置）原样保留**。

- 删除内容：该站点的**影片**、**分类**、**标签**（含 `video_tags` 关联）、**地域**（含 `video_areas` 关联）与**采集记录**，其它站点的数据不受影响。
- 同时**复位采集状态**：`last_cursor`、`last_mode`、`last_status`、`last_error` 清空，`last_sync_at` / `last_full_sync_at` 置空，`video_count` 归零。这一步很关键——影片都删了，若增量游标还停在上次采到的最大 `vod_time`，下次增量采集会认为「没有更新」而永远补不回数据；复位后下一次增量从零开始重新拉取。
- 有**排队中 / 运行中**的采集任务时拒绝执行（`409`），避免清空与采集并发互相覆盖：先暂停采集或等任务结束再清空。
- 响应返回本次删除的条数，例如 `{"videos":1234,"categories":12,"tags":37,"jobs":8}`；站点不存在返回 `404`，普通用户调用返回 `403`。
