package service

import (
	"sort"
	"strings"
	"sync"

	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

// SettingField 描述一个可在线编辑的配置项，供前端渲染表单与提示。
type SettingField struct {
	Key      string `json:"key"`      // 前端提交时的 JSON 键（camelCase）
	YAMLKey  string `json:"yamlKey"`  // conf/config.yaml 里的键名
	Label    string `json:"label"`    // 中文名
	Help     string `json:"help"`     // 说明
	Min      int    `json:"min"`      // 整数最小值
	Max      int    `json:"max"`      // 整数最大值；0 表示不限
	Unit     string `json:"unit"`     // 单位
	ReadOnly bool   `json:"readOnly"` // 被环境变量接管，页面只读
}

// SettingsView 是设置页的响应：可编辑的采集配置 + 可编辑的数据库配置 + 只读启动配置。
type SettingsView struct {
	Collector          config.CollectorConfig `json:"collector"`
	Fields             []SettingField         `json:"fields"`
	EnvLocked          []string               `json:"envLocked"`          // 被 env 接管的 YAML 键
	Static             StaticSettings         `json:"static"`             // 启动期配置，只读
	Database           DatabaseSettingsView   `json:"database"`           // 可后台编辑的数据库配置（重启生效）
	ConfigPath         string                 `json:"configPath"`         // 配置文件路径，便于排错
	MaxConcurrentLimit int                    `json:"maxConcurrentLimit"` // 在线并发上限（1..64）
}

// DatabaseSettingsView 是设置页里可编辑的数据库配置视图。
// DSN 含密码，绝不回显明文：只暴露是否已配置（dsnConfigured）与脱敏连接目标（display）。
type DatabaseSettingsView struct {
	Driver             string   `json:"driver"`             // sqlite | mysql
	Path               string   `json:"path"`               // SQLite 文件路径
	DSNConfigured      bool     `json:"dsnConfigured"`      // MySQL 连接串是否已配置（不回显明文）
	Display            string   `json:"display"`            // 脱敏连接目标（host/db），不含密码
	MaxOpenConns       int      `json:"maxOpenConns"`       // 连接池上限，0 用默认
	MaxIdleConns       int      `json:"maxIdleConns"`       // 空闲连接上限，0 用默认
	ConnMaxLifetimeMin int      `json:"connMaxLifetimeMin"` // 连接最长存活分钟，0 用默认
	Locked             []string `json:"locked"`             // 被环境变量接管的 YAML 键（如 path），这些字段页面只读
	ApplyNote          string   `json:"applyNote"`          // 应用时机提示
}

// StaticSettings 是只能改配置文件 + 重启生效的启动项，展示给管理员参考。
type StaticSettings struct {
	ServerAddr     string `json:"serverAddr"`
	DatabaseDriver string `json:"databaseDriver"` // sqlite | mysql
	DatabasePath   string `json:"databasePath"`   // SQLite 路径或 MySQL 脱敏连接目标，绝不含密码
	WebDir         string `json:"webDir"`
	TokenTTLHours  int    `json:"tokenTtlHours"`
	BCryptCost     int    `json:"bcryptCost"`
}

// settingsFields 是可编辑字段的元信息，顺序即前端展示顺序。
var settingsFields = []SettingField{
	{Key: "maxConcurrent", YAMLKey: "max_concurrent", Label: "同时运行的任务数", Help: "超出的任务自动排队；同一站点仍串行。范围 1..64，修改立即生效。", Min: 1, Max: 64, Unit: "个"},
	{Key: "timeoutSeconds", YAMLKey: "timeout_seconds", Label: "单次请求超时", Help: "每个采集接口请求的最长等待时间，修改后下一次请求即用新值。", Min: 1, Max: 600, Unit: "秒"},
	{Key: "maxPages", YAMLKey: "max_pages", Label: "单次采集最大页数", Help: "防止站点未返回总页数时无限翻页；站点单独配置 maxPages 时以站点为准。", Min: 1, Unit: "页"},
	{Key: "pageSize", YAMLKey: "page_size", Label: "每页条数", Help: "0 表示由站点返回决定；>0 时向接口附加 pagesize 参数（多数站点上限 100）。", Min: 0, Max: 1000, Unit: "条"},
	{Key: "defaultIncrementalHours", YAMLKey: "default_incremental_hours", Label: "默认增量时间窗", Help: "站点未单独配置增量小时数时的时间窗，对应接口 h 参数。", Min: 1, Max: 24 * 30, Unit: "小时"},
	{Key: "retryTimes", YAMLKey: "retry_times", Label: "失败自动重试次数", Help: "0 表示不自动重试；重试从失败页续采，不会从头再来。", Min: 0, Max: 10, Unit: "次"},
	{Key: "retryBackoffSeconds", YAMLKey: "retry_backoff_seconds", Label: "重试首次退避", Help: "首次失败后的等待秒数，之后按 2 倍退避、上限 30 秒。", Min: 1, Max: 30, Unit: "秒"},
	{Key: "progressSaveSeconds", YAMLKey: "progress_save_seconds", Label: "进度落库间隔", Help: "运行中把已完成页数与统计写库的频率，越小越不易丢进度但写库更频繁。", Min: 1, Max: 3600, Unit: "秒"},
	{Key: "pageIntervalMs", YAMLKey: "page_interval_ms", Label: "每页间隔", Help: "相邻两页之间的等待时间，用于降低对站点的请求频率、避免被限流或封禁。0 表示不限速（全速采集）；修改后运行中的任务从下一页起立即生效。范围 0..60000。", Min: 0, Max: 60000, Unit: "毫秒"},
	{Key: "userAgent", YAMLKey: "user_agent", Label: "请求 UA", Help: "采集接口的 User-Agent，可伪装浏览器绕过部分站点的拦截。", Unit: "字符串"},
}

// SettingsService 负责设置页的读写编排：只依赖 config.Store 与采集调度层，
// 不碰任何仓储，符合 service 层只调紧邻下一层（此处 config 为横切包）的约定。
type SettingsService struct {
	store *config.Store
	crawl *CrawlService

	// 重启能力由装配层注入：service 只投递请求，不自己关数据库或重建进程。
	mu         sync.Mutex
	restart    func() error
	restarting bool
}

func NewSettingsService(store *config.Store, crawl *CrawlService) *SettingsService {
	return &SettingsService{store: store, crawl: crawl}
}

// SetRestartHook 注入「请求重启进程」的回调，由应用装配层在构造完成后提供。
// 未注入（例如单元测试直接构造服务）时重启接口会返回「不支持」。
func (s *SettingsService) SetRestartHook(fn func() error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restart = fn
}

// Current 返回当前采集配置快照，供 controller 作为局部更新的默认值。
func (s *SettingsService) Current() config.CollectorConfig {
	return s.store.Collector()
}

// Get 组装设置页视图，并把被环境变量接管的字段标为只读。
func (s *SettingsService) Get() SettingsView {
	cfg := s.store.Current()
	locked := make(map[string]bool)
	for _, k := range s.store.EnvOverridden() {
		locked[k] = true
	}
	fields := make([]SettingField, 0, len(settingsFields))
	for _, f := range settingsFields {
		if locked[f.YAMLKey] {
			f.ReadOnly = true
			f.Help += "（当前由环境变量控制，页面只读；请修改启动环境后重启。）"
		}
		fields = append(fields, f)
	}
	// 数据库段：按字段粒度标记只读——只锁被环境变量接管的那个字段，
	// 其余字段仍可在线编辑（例如部署设置了 ASSETSPIDER_DB_PATH，仅 path 只读，
	// driver / dsn / 连接池依然可改）。
	envLocked := map[string]bool{}
	for _, k := range s.store.EnvOverridden() {
		envLocked[k] = true
	}
	seen := map[string]bool{}
	dbLocked := make([]string, 0, len(config.DBEnvOverrides))
	for _, yk := range config.DBEnvOverrides {
		if envLocked[yk] && !seen[yk] {
			seen[yk] = true
			dbLocked = append(dbLocked, yk)
		}
	}
	sort.Strings(dbLocked)
	db := cfg.Database
	return SettingsView{
		Collector: cfg.Collector,
		Fields:    fields,
		EnvLocked: s.store.EnvOverridden(),
		Static: StaticSettings{
			ServerAddr:     cfg.Server.Addr,
			DatabaseDriver: cfg.Database.Driver,
			DatabasePath:   cfg.Database.Display(),
			WebDir:         cfg.Web.Dir,
			TokenTTLHours:  cfg.Auth.TokenTTLHours,
			BCryptCost:     cfg.Auth.BCryptCost,
		},
		Database: DatabaseSettingsView{
			Driver:             db.Driver,
			Path:               db.Path,
			DSNConfigured:      strings.TrimSpace(db.DSN) != "",
			Display:            db.Display(),
			MaxOpenConns:       db.MaxOpenConns,
			MaxIdleConns:       db.MaxIdleConns,
			ConnMaxLifetimeMin: db.ConnMaxLifetimeMin,
			Locked:             dbLocked,
			ApplyNote:          "修改后需重启服务生效；切换数据库后端不会热重载连接，重启后新配置才生效。",
		},
		ConfigPath:         s.store.Path(),
		MaxConcurrentLimit: 64,
	}
}

// SettingsUpdateInput 是 PUT /api/settings/v1 的请求体：采集配置与数据库配置一起提交。
type SettingsUpdateInput struct {
	Collector config.CollectorConfig `json:"collector"`
	Database  config.DatabaseConfig  `json:"database"`
}

// databaseUnchanged 判断数据库子对象是否为「全零」——前端只改采集配置时数据库段
// 会原样回传默认值，此时跳过数据库写盘，避免把零值覆盖进配置文件。
func databaseUnchanged(db config.DatabaseConfig) bool {
	return db.Driver == "" && db.Path == "" && db.DSN == "" &&
		db.MaxOpenConns == 0 && db.MaxIdleConns == 0 && db.ConnMaxLifetimeMin == 0
}

// Update 在线更新采集配置与数据库配置：存储层校验 + 持久化 + 替换内存快照后，
// 同步并发限流器，让 maxConcurrent 的改动也立即生效。数据库后端切换在下次启动时生效。
func (s *SettingsService) Update(in SettingsUpdateInput) (SettingsView, error) {
	// 页面可能不给 maxConcurrent 上限保护之外的额外校验，存储层 Validate 会兜底；
	// 这里再补一条与并发接口一致的上限，避免配置页绕过 1..64。
	if in.Collector.MaxConcurrent < 1 || in.Collector.MaxConcurrent > 64 {
		return SettingsView{}, invalid("max_concurrent 需在 1..64 之间")
	}
	if _, err := s.store.UpdateCollector(in.Collector); err != nil {
		return SettingsView{}, invalid("%s", err.Error())
	}
	// 数据库配置为空结构体说明前端未携带（只改了采集配置），跳过以免用零值覆盖。
	if !databaseUnchanged(in.Database) {
		if _, err := s.store.UpdateDatabase(in.Database); err != nil {
			return SettingsView{}, invalid("%s", err.Error())
		}
	}
	s.crawl.ApplyLive()
	return s.Get(), nil
}

// DatabaseTestResult 是「测试连接」的返回：成功返回 ok=true 与说明，失败返回 ok=false 与原因。
type DatabaseTestResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"` // 成功时的说明
	Error   string `json:"error,omitempty"`   // 失败时的原因（校验失败或连接失败）
}

// DatabaseTestInput 是「测试连接」的请求载荷，与保存接口的数据库段结构一致。
type DatabaseTestInput struct {
	Driver             string `json:"driver"`
	Path               string `json:"path"`
	DSN                string `json:"dsn"`
	MaxOpenConns       int    `json:"maxOpenConns"`
	MaxIdleConns       int    `json:"maxIdleConns"`
	ConnMaxLifetimeMin int    `json:"connMaxLifetimeMin"`
}

// TestDatabase 在保存前探测给定数据库配置是否可达：以当前已保存配置为基准叠加页面提交的值，
// 校验通过后建立一个临时连接并 Ping，验证完即关闭。
//
// 不写盘、不替换运行中的连接（与「保存后重启生效」语义一致）。DSN 留空且为 MySQL 时沿用
// 当前已配置的连接串，这样「保持现有连接串」也能被实测；连接池参数仅用于建连，不影响可达性判断。
func (s *SettingsService) TestDatabase(in DatabaseTestInput) DatabaseTestResult {
	submitted := config.DatabaseConfig{
		Driver:             in.Driver,
		Path:               in.Path,
		DSN:                in.DSN,
		MaxOpenConns:       in.MaxOpenConns,
		MaxIdleConns:       in.MaxIdleConns,
		ConnMaxLifetimeMin: in.ConnMaxLifetimeMin,
	}
	base := s.store.Current().Database
	merged := base
	if d := strings.ToLower(strings.TrimSpace(submitted.Driver)); d != "" {
		merged.Driver = d
	}
	if p := strings.TrimSpace(submitted.Path); p != "" {
		merged.Path = p
	}
	// DSN 留空表示「保留现有连接串」；只有显式填写时才覆盖（避免误清空已保存的密码）。
	if merged.Driver == "mysql" && strings.TrimSpace(submitted.DSN) == "" {
		merged.DSN = base.DSN
	} else {
		merged.DSN = strings.TrimSpace(submitted.DSN)
	}
	if submitted.MaxOpenConns != 0 {
		merged.MaxOpenConns = submitted.MaxOpenConns
	}
	if submitted.MaxIdleConns != 0 {
		merged.MaxIdleConns = submitted.MaxIdleConns
	}
	if submitted.ConnMaxLifetimeMin != 0 {
		merged.ConnMaxLifetimeMin = submitted.ConnMaxLifetimeMin
	}

	if err := merged.Validate(); err != nil {
		return DatabaseTestResult{OK: false, Error: err.Error()}
	}
	if err := repository.TestConnection(repository.Options{
		Driver:             merged.Driver,
		Path:               merged.Path,
		DSN:                merged.DSN,
		MaxOpenConns:       merged.MaxOpenConns,
		MaxIdleConns:       merged.MaxIdleConns,
		ConnMaxLifetimeMin: merged.ConnMaxLifetimeMin,
	}); err != nil {
		return DatabaseTestResult{OK: false, Error: err.Error()}
	}
	return DatabaseTestResult{OK: true, Message: "连接成功：" + merged.Display()}
}

// RestartResult 是「重启服务」的返回：请求被接受返回 ok=true，
// 不支持重启或已在重启中返回 ok=false 与原因。
type RestartResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"` // 接受后的说明
	Error   string `json:"error,omitempty"`   // 拒绝的原因
}

// Restart 请求重启当前服务进程。
//
// 真正的收尾（停调度、等任务、关数据库）与进程重建由 main 完成——这里只投递请求，
// 因此接口能先正常返回响应，页面才看得到「重启中」而不是连接被掐断。
// 同一进程生命周期内只接受一次：进程即将被替换，重复请求没有意义。
func (s *SettingsService) Restart() RestartResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.restart == nil {
		return RestartResult{OK: false, Error: "当前运行方式不支持在线重启，请手动重启服务"}
	}
	if s.restarting {
		return RestartResult{OK: false, Error: "服务正在重启，请稍候"}
	}
	s.restarting = true
	if err := s.restart(); err != nil {
		s.restarting = false
		return RestartResult{OK: false, Error: err.Error()}
	}
	return RestartResult{OK: true, Message: "正在重启服务，稍后自动恢复"}
}
