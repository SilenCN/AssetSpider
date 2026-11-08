# AssetSpider

视频站点采集工具。把**海洋CMS / 苹果CMS 系资源站的 JSON 采集接口**配置进数据库，按需执行**全量**或**增量**采集，把接口返回的**全部字段**去重后写入 SQLite，并提供带**登录与站点级权限**的 Vue 管理页面。

后端 Go + Gin + GORM/SQLite，前端 Vue 3 + Vite，构建产物由 Gin 直接托管。

## 技术栈

| 层 | 选型 |
| --- | --- |
| Web 框架 | Gin |
| ORM / 数据库 | GORM + SQLite（官方 CGO 驱动） |
| 认证 | bcrypt 密码哈希 + 数据库会话令牌（SHA-256 摘要存储） |
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

浏览器打开 http://localhost:8080 。**系统初始没有任何账号**：第一个在登录页输入用户名密码并登录的人，会自动注册为**管理员**并拥有全部权限。之后由管理员在「用户管理」里创建账号、分配可见站点。数据库默认创建在 `data/assetspider.db`（父目录自动创建）。

## 登录与权限（重点）

### 首个登录用户自动成为管理员

`POST /api/auth/login` 在账号表为空时，会用本次提交的用户名/密码直接创建账号，`role=admin`、`enabled=true`，响应里 `firstUser=true`。一旦有了账号，陌生用户名不再自动注册（返回 401），新账号只能由管理员创建。用户名唯一索引兜住并发下的重复注册。

### 角色与权限矩阵

| 能力 | 管理员 | 普通用户 |
| --- | --- | --- |
| 登录 / 查看自己 / 修改密码 / 登出 | ✓ | ✓ |
| 用户管理（创建、编辑、删除、分配可见站点） | ✓ | ✗ `403` |
| 站点配置增删改、启停、资产台账写入 | ✓ | ✗ `403` |
| 查看站点列表 | 全部站点 | **仅有权限的站点** |
| 查看影片、单条详情、采集记录、字段元数据 | 全部 | **仅有权限站点的** |
| 触发单站点采集 | 任意站点 | **仅有权限站点**（否则 `404`） |
| 批量采集 | 全部启用站点 | **仅有权限的启用站点** |

> 「只能看见」在数据层严格生效：`sources` / `videos` / `videos/:id` / `jobs` 全部按当前用户的可见站点 ID 集合过滤；未授权站点返回 404 而非 403，避免暴露其存在性。批量采集会由编排器按白名单裁剪，不会触及无权限站点。

### 站点可见性授权

`user_sources` 表记录「普通用户 ↔ 可见站点」的多对多关系。管理员在「用户管理」里勾选站点即可授权，**每个请求都会重新读取授权关系，因此调整后无需用户重新登录即生效**。管理员不需要授权记录，默认可见全部站点。站点被删除时会级联清理对应授权。

### 会话安全

- 密码用 **bcrypt** 哈希存储（强度 `auth.bcrypt_cost` 可配），响应体永不返回 `password_hash`。
- 登录令牌是 32 字节随机数，**数据库只保存其 SHA-256 摘要**；令牌有效期由 `auth.token_ttl_hours` 配置（默认 168 小时）。
- 令牌过期、账号被**停用/删除/改密/变更角色**时，相关会话立即失效（`GetSessionUser` 会校验 `users.enabled` 与 `sessions.expires_at`）。
- 系统保证始终保留**至少一名启用状态的管理员**：不能删除自己，也不能停用或降级唯一的管理员。

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
      "vod_down_url": "...", "vod_actor": "...", "vod_content": "...",
      "vod_score": "8.5", "vod_custom_flag": "站点私有字段"
    }
  ]
}
```

解析做了兼容：`vod_id` / `type_id` 等字段既可以是数字也可以是字符串，`limit` 可以是 `"20"`，`pagecount` 也接受 `page_count` 别名；`null` 转空串，对象与数组按紧凑 JSON 文本保留。

### 全字段保留（重点）

资源站的字段因版本、二次开发、自定义模板差异很大，因此**不使用固定结构体反序列化**（那会静默丢弃未定义字段）。每条 `list[]` 记录在解析时同时保留三份数据：

| 层 | 落地位置 | 说明 |
| --- | --- | --- |
| 结构化标准列 | `videos` 表的 91 个 `vod_*` 列 | 依据 MacCMS V10 官方「视频字段」表，覆盖基础信息、图片、演职、上映、属性、统计、时间、来源、密码、播放/下载、分集剧情等 14 组；便于 SQL 查询与前端展示 |
| 原始记录全文 | `videos.raw` | 接口返回的该条记录原始 JSON 全文（紧凑化、未裁剪），可还原任意字段 |
| 扩展字段 | `videos.extra` | 接口返回但不在标准清单内的字段（站点自定义字段、嵌套对象、数组等），按**原始 JSON 值**保存 |

字段清单 `internal/model/video_fields.go` 的 `VodFields` 是唯一数据源：采集映射、入库更新、字段元数据接口、前端详情展示全部由它驱动，新增字段只需追加清单 + 实体字段。`model` 包的一致性测试会校验清单与实体、列名、json tag 严格对应，避免改动时漏字段。

内容指纹由该条记录的**全部键值**（含扩展字段）排序后计算，因此任何字段变化都会触发更新；仅字段顺序或空白变化不会误判。更新时用全字段 map 写回，接口把某字段清空也能正确同步。

### 全量采集

按 `ac=list&pg=1..pagecount` 遍历所有分页（受 `collector.max_pages` 与站点 `maxPages` 限制），逐页入库。站点未返回总页数时按"拉到空页为止"处理。

### 增量采集

三道保障，避免漏采与重复：

1. **接口侧时间窗**：附加 `h=<小时数>` 参数，取「站点 `incrementalHours`」与「距上次采集经过的小时数 + 2」的较大者；
2. **本地时间游标**：站点表 `last_cursor` 记录已采到的最大 `vod_time`，增量时过滤掉游标之前的旧记录；
3. **入库去重**：`(source_id, vod_id)` 复合唯一索引 + 全字段内容指纹，重复数据不产生新行，内容变化才更新。

> 海洋CMS 系站点更新影片会同步刷新 `vod_time`，因此游标过滤是安全的；若某站点不刷新 `vod_time`，可调大增量小时数或改用全量采集。

### 数据入库

`videos` 表保存全部标准字段、原始 JSON、扩展字段、内容指纹与首次/最近入库时间。每次采集生成一条 `crawl_jobs` 记录（模式、状态、页数、拉取/新增/更新/未变条数、耗时、错误信息），站点表回写上次采集状态与游标。

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
auth:
  token_ttl_hours: 168           # 登录令牌有效期（小时）
  bcrypt_cost: 10                # 密码哈希强度（4..31）
```

环境变量覆盖：`ASSETSPIDER_SERVER_ADDR`、`ASSETSPIDER_DB_PATH`、`ASSETSPIDER_WEB_DIR`，
`ASSETSPIDER_COLLECTOR_TIMEOUT` / `_MAX_PAGES` / `_PAGE_SIZE` / `_DEFAULT_HOURS` / `_PARALLEL` / `_USER_AGENT`，
以及 `ASSETSPIDER_AUTH_TOKEN_TTL_HOURS` / `ASSETSPIDER_AUTH_BCRYPT_COST`。
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

除标注为「公开」的接口外，所有 `/api/*` 请求都需带 `Authorization: Bearer <token>`；`401` 表示未登录/会话失效，`403` 表示非管理员越权。

### 认证与用户（用户管理仅管理员）

| 方法 | 路径 | 权限 | 说明 |
| --- | --- | --- | --- |
| GET | `/api/auth/status` | 公开 | `{initialized, userCount}`，登录页据此提示「首用户将成为管理员」 |
| POST | `/api/auth/login` | 公开 | `{username,password}` → `{token,expiresAt,firstUser,isAdmin,user}`；账号表为空时自动注册管理员 |
| POST | `/api/auth/logout` | 登录 | 吊销当前令牌 |
| GET | `/api/auth/me` | 登录 | 当前账号、是否管理员、可见站点 ID |
| POST | `/api/auth/password` | 登录 | `{oldPassword,newPassword}` 改自己密码，返回新令牌并吊销该账号其他会话 |
| GET | `/api/users` | 管理员 | 用户列表，含每个用户的 `sourceIds` |
| POST | `/api/users` | 管理员 | 创建用户：`username/password/role/enabled/sourceIds` |
| PUT | `/api/users/:id` | 管理员 | 修改密码、角色、启用状态、可见站点；字段缺省即不修改 |
| DELETE | `/api/users/:id` | 管理员 | 删除用户，级联清理会话与站点授权 |

### 采集与数据（读取按可见站点过滤；站点写操作仅管理员）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/health` | 公开探活，仅返回状态与数据库路径，不泄露站点/影片数量 |
| GET | `/api/sources` | 站点列表（按权限过滤），附带每个站点已入库影片数 |
| POST / PUT / DELETE | `/api/sources`、`/api/sources/:id` | 仅管理员：新增 / 修改 / 删除站点（删除级联清理影片、采集记录与用户授权） |
| POST | `/api/sources/:id/crawl?mode=full\|incremental` | 单站点采集；非管理员仅可采集可见站点（否则 404） |
| POST | `/api/crawl?mode=full\|incremental` | 批量采集；非管理员自动限定在可见的启用站点 |
| GET | `/api/videos?source_id=&q=&page=&size=` | 分页查询影片（按权限过滤，返回全部字段、raw、extra） |
| GET | `/api/videos/:id` | 单条影片完整记录（越权返回 404） |
| GET | `/api/meta/fields` | 全字段元数据：`total`、`groups`、`fields[]` |
| GET | `/api/jobs?source_id=&limit=` | 采集记录（按权限过滤） |
| GET | `/api/assets` | 通用资产台账（登录用户） |
| POST / DELETE | `/api/assets`、`/api/assets/:id` | 仅管理员：资产台账写入 |

`mode` 也可放在 JSON body：`{"mode":"incremental"}`；缺省为 `incremental`。示例：

```bash
# 1. 首次登录（自动注册为管理员）
TOKEN=$(curl -s -X POST http://localhost:8080/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"admin123"}' | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')

# 2. 带令牌调用业务接口
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/sources

# 3. 创建用户并授权站点 1、2
curl -X POST http://localhost:8080/api/users -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"username":"viewer","password":"viewer123","role":"user","sourceIds":[1,2]}'
```

## 前端开发

```bash
cd web
npm install
npm run dev      # Vite 开发服务器，/api 代理到 :8080
npm run build    # 产物输出到 web/dist，由 Gin 托管
```

未登录时显示**登录页**（首用户会看到「将自动注册为管理员」的提示）；登录后顶部显示账号、角色、修改密码与退出登录。页面分四块：

- **站点配置**：管理员可增删改、启停、单站点/批量全量与增量采集；普通用户只读，且仅能看到被授权的站点（仍可对其采集）。
- **采集数据**：分页/搜索影片，点「详情」按 14 个分组查看全部标准字段，并可切换查看扩展字段与原始 JSON。
- **采集记录**：每次采集的统计与状态（仅可见站点）。
- **用户管理**（仅管理员）：创建用户、勾选可见站点、改角色/密码/启用状态、删除；系统会阻止删除自己与停用/降级唯一管理员。

令牌保存在浏览器 `localStorage`，`401` 时自动清除登录态并回到登录页。`web/dist` 已随仓库提供，clone 后无需构建前端即可运行；未知前端路由由 Gin 回退到 `index.html`。

## 目录结构

```
.
├── conf/config.yaml          # 运行配置（含 auth 段）
├── internal/
│   ├── config/               # 配置加载与校验（含 AuthConfig）
│   ├── model/                # GORM 模型
│   │   ├── video.go          # 全字段 Video 实体
│   │   ├── video_fields.go   # VodFields 字段清单（唯一数据源）
│   │   └── user.go           # User / Session / UserSource
│   ├── store/                # SQLite 数据访问
│   │   ├── auth.go           # 用户、会话、站点授权
│   │   ├── scope.go          # 按可见站点集合过滤的查询
│   │   └── crawl.go          # 站点/影片/任务 CRUD 与全字段去重 upsert
│   ├── collector/            # 零丢失 JSON 解析、分页、全量/增量编排
│   └── server/               # Gin 路由与 API
│       ├── auth.go           # 登录、令牌、鉴权中间件
│       ├── users.go          # 管理员用户管理
│       ├── crawl.go          # 采集与数据接口（权限过滤）
│       └── server.go         # 路由分组与静态托管
├── web/
│   ├── src/                  # Vue 3 源码（登录、用户管理、数据视图）
│   └── dist/                 # 构建产物（Gin 托管，随仓库提交）
├── main.go                   # 服务入口
├── go.mod / go.sum
└── Readme.md
```

## 测试

`go test ./...` 覆盖配置加载、站点/影片/任务 CRUD、去重 upsert、**字段清单与实体一致性**、**全字段零丢失解析**、**全字段指纹与全字段写回**、URL 拼装、全量/增量/游标/失败回写，以及**认证与权限**：首用户自动注册管理员、错误密码/陌生账号拒绝、401/403 拦截、可见站点隔离（列表/影片/详情/记录/批量采集）、授权变更即时生效、管理员保护、改密/停用/删除后会话失效、登出。采集器与接口测试用本地 HTTP 模拟站点，不依赖外网。

端到端可用模拟站点验证：91 个标准字段全部落库（`videos` 表 100 列）、`raw`/`extra` 零丢失、只改一个冷门字段也能触发更新；登录链路可验证「管理员建两个站点 → 创建只授权 A 的普通用户 → 该用户看不到 B 的任何数据且越权操作被拒 → 改授权即时切换 → 停用/删除即时失效」，并直接查 SQLite 确认 `users` / `sessions` / `user_sources` 三张表与 bcrypt 哈希落盘。

## 计划

- [x] 用户登录、管理员与站点可见性权限
- [ ] 支持 XML 格式采集接口
- [ ] 定时自动增量采集（cron）
- [ ] 采集结果导出（CSV / JSON）与站点分类绑定
