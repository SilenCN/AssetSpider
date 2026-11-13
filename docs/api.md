# API

> 项目总览：[README](../Readme.md) · 相关：[配置](configuration.md) · [前端](frontend.md)

接口统一按 **`/api/{模块名}/{版本号}`** 组织，当前版本 `v1`（`internal/router.APIVersion`）。模块名一律是复数资源名词，操作由 HTTP 动词表达，路径中不含动词；升级时新增 `registerV2` 分组即可，旧版本保持可用。

## 登录与权限

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
| subscriptions | GET | `/api/subscriptions/v1` | 站点配置订阅列表，附带按间隔现算的下次刷新时刻 `nextRefreshAt`，以及上次刷新结果（`lastStatus` / `lastCreated` / `lastUpdated` / `lastDisabled` / `lastSkipped` / `lastError`） |
| subscriptions | POST | `/api/subscriptions/v1` | 新增订阅（**仅管理员**）：`{name, url, intervalHours, enabled}`；`intervalHours=0` 表示只手动刷新 |
| subscriptions | PUT | `/api/subscriptions/v1/:id` | 修改订阅（**仅管理员**，重名返回 409），刷新状态保持不变 |
| subscriptions | DELETE | `/api/subscriptions/v1/:id` | 删除订阅（**仅管理员**）：名下站点**解绑保留**（`subscriptionId` 归 0），影片与采集记录不受影响 |
| subscriptions | POST | `/api/subscriptions/v1/:id/refreshes` | 手动刷新（**仅管理员**）：拉取订阅地址并同步站点配置，返回 `{total, created, updated, disabled, skipped, issues[]}`；地址不可达 / 内容非法时返回 5xx，`error` 里是「人话 + 技术细节」 |
| crawls | POST | `/api/crawls/v1` | 对全部启用站点批量发起**同步**采集（超上限的排队） |
| settings | GET | `/api/settings/v1` | 系统设置：可编辑的 `collector` 配置 + `fields[]` 元信息 + `envLocked` 只读字段 + 只读启动项 `static`（登录可读） |
| settings | PUT | `/api/settings/v1` | 在线更新采集配置（**仅管理员**）：校验后写回 `conf/config.yaml` 并即时生效；支持只提交部分字段，被环境变量接管的字段拒绝修改 |
| settings | POST | `/api/settings/v1/database/test` | 保存前**测试数据库连接**（**仅管理员**）：body `{database:{driver,path,dsn,maxOpenConns,maxIdleConns,connMaxLifetimeMin}}`；校验 + 建立临时连接并 Ping，不写盘、不替换运行中的连接。连接成败是预期的探测结果，统一返回 `200`：`{ok:true,message}` 或 `{ok:false,error}`；DSN 留空且为 MySQL 时沿用当前已配置的连接串 |
| settings | POST | `/api/settings/v1/restart` | **重启服务**（**仅管理员**）：先优雅停止 HTTP（等待在途请求）、停调度并等采集任务收尾、关闭数据库，再用同一可执行文件 / 参数 / 环境变量**原地重建进程**（`exec`，PID 不变，systemd 不感知退出）。接口先返回 `202` 再停服，故页面能拿到响应：接受 `{ok:true,message}`；重复触发、平台不支持或投递失败返回 `{ok:false,error}` |
| tasks | GET | `/api/tasks/v1` | 采集任务列表：`capacity`（`maxConcurrent` / `running` / `queued`）+ `items[]`（排队中、运行中、暂停中，含实时进度） |
| tasks | POST | `/api/tasks/v1` | **异步**发起采集任务，body `{sourceId, mode}`，返回 `202` 与任务视图（`jobId`）；同站点已有任务时新任务排队（`status=queued`），不返回冲突 |
| tasks | PUT | `/api/tasks/v1/:id` | 更新任务状态：`{"status":"paused"}` 暂停 / `{"status":"running"}` 继续（`:id` 为 `jobId`） |
| tasks | POST | `/api/tasks/v1/:id/retry` | 手动重试**失败**的任务：**复用原记录**从失败页续采（统计累加，不新开任务），返回 `202` 与任务视图（非 `failed` 返回 409） |
| tasks | POST | `/api/tasks/v1/:id/rerun` | **重新运行**已结束的任务：**复用原记录**忽略断点从第 1 页重来，统计与尝试次数清零，返回 `202` 与任务视图（运行中返回 409） |
| tasks | GET | `/api/tasks/v1/capacity` | 读取并发容量 |
| tasks | PUT | `/api/tasks/v1/capacity` | 调整同时运行的任务数：`{"maxConcurrent":2}`（1..64）；兼容旧入口，有配置存储时同样写回配置持久化，与设置页共用数据源 |
| videos | GET | `/api/videos/v1?source_id=&category_id=&q=&sort=&page=&size=` | 分页查询入库影片（列表只返回卡片字段，完整记录见详情接口），响应为 `{items, page, size}`（**不返回 total / hasMore**；前端按本页条数是否等于 size 判断是否继续翻页）。`q` 匹配名称/别名/演员/导演/分类/地域（≥3 字走 FTS5 trigram、2 字走 bigram 倒排索引、1 字回退 LIKE，语义一致，详见[配置](configuration.md)）；`category_id` 按自动归档的分类过滤（含子分类）；`sort` 取值 `id_desc`（默认，库内 id 倒序）/`time_desc`（更新时间↓）/`time_asc`。标签 / 地域 / 年份筛选与评分排序已移除，影片上的 `vod_tag` / `vod_area` / `vod_year` / `vod_score` 原始字段仍保留并作为卡片、详情的纯信息展示 |
| videos | GET | `/api/videos/v1/:id` | 单条影片完整记录 |
| videos | GET | `/api/videos/v1/:id/play-groups` | 该影片的播放组数组（来自苹果CMS 播放组字段，已解析为结构化 JSON） |
| categories | GET | `/api/categories/v1?source_id=` | 视频分类列表，含 `videoCount`；`source_id` 只看该资源站的一套分类 |
| categories | GET | `/api/categories/v1/tree?source_id=` | 视频分类二级树（父分类在前、子分类嵌套），按苹果CMS type_pid 模型 |
| categories | GET | `/api/categories/v1/:id` | 单个分类详情 |
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
# 站点配置订阅：远程 JSON 里维护一批站点，按间隔自动刷新，也可以手动点刷新
api -X POST http://localhost:8080/api/subscriptions/v1 \
  -H 'Content-Type: application/json' \
  -d '{"name":"公共站点列表","url":"https://example.com/assetspider-sources.json","intervalHours":6}'
api -X POST http://localhost:8080/api/subscriptions/v1/1/refreshes
api 'http://localhost:8080/api/videos/v1?page=1&size=20'
api 'http://localhost:8080/api/categories/v1'
api 'http://localhost:8080/api/videos/v1?category_id=1'
api 'http://localhost:8080/api/meta/v1/fields'
```
