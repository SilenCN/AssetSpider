package service

import "github.com/SilenCN/AssetSpider/internal/config"

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

// SettingsView 是设置页的响应：可编辑的采集配置 + 只读的启动配置。
type SettingsView struct {
	Collector          config.CollectorConfig `json:"collector"`
	Fields             []SettingField         `json:"fields"`
	EnvLocked          []string               `json:"envLocked"`          // 被 env 接管的 YAML 键
	Static             StaticSettings         `json:"static"`             // 启动期配置，只读
	ConfigPath         string                 `json:"configPath"`         // 配置文件路径，便于排错
	MaxConcurrentLimit int                    `json:"maxConcurrentLimit"` // 在线并发上限（1..64）
}

// StaticSettings 是只能改配置文件 + 重启生效的启动项，展示给管理员参考。
type StaticSettings struct {
	ServerAddr    string `json:"serverAddr"`
	DatabasePath  string `json:"databasePath"`
	WebDir        string `json:"webDir"`
	TokenTTLHours int    `json:"tokenTtlHours"`
	BCryptCost    int    `json:"bcryptCost"`
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
	{Key: "userAgent", YAMLKey: "user_agent", Label: "请求 UA", Help: "采集接口的 User-Agent，可伪装浏览器绕过部分站点的拦截。", Unit: "字符串"},
}

// SettingsService 负责设置页的读写编排：只依赖 config.Store 与采集调度层，
// 不碰任何仓储，符合 service 层只调紧邻下一层（此处 config 为横切包）的约定。
type SettingsService struct {
	store *config.Store
	crawl *CrawlService
}

func NewSettingsService(store *config.Store, crawl *CrawlService) *SettingsService {
	return &SettingsService{store: store, crawl: crawl}
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
	return SettingsView{
		Collector: cfg.Collector,
		Fields:    fields,
		EnvLocked: s.store.EnvOverridden(),
		Static: StaticSettings{
			ServerAddr:    cfg.Server.Addr,
			DatabasePath:  cfg.Database.Path,
			WebDir:        cfg.Web.Dir,
			TokenTTLHours: cfg.Auth.TokenTTLHours,
			BCryptCost:    cfg.Auth.BCryptCost,
		},
		ConfigPath:         s.store.Path(),
		MaxConcurrentLimit: 64,
	}
}

// Update 在线更新采集配置：存储层校验 + 持久化 + 替换内存快照后，
// 同步并发限流器，让 maxConcurrent 的改动也立即生效。
func (s *SettingsService) Update(in config.CollectorConfig) (SettingsView, error) {
	// 页面可能不给 maxConcurrent 上限保护之外的额外校验，存储层 Validate 会兜底；
	// 这里再补一条与并发接口一致的上限，避免配置页绕过 1..64。
	if in.MaxConcurrent < 1 || in.MaxConcurrent > 64 {
		return SettingsView{}, invalid("max_concurrent 需在 1..64 之间")
	}
	if _, err := s.store.UpdateCollector(in); err != nil {
		return SettingsView{}, invalid("%s", err.Error())
	}
	s.crawl.ApplyLive()
	return s.Get(), nil
}
