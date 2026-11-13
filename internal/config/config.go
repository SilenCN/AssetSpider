// Package config 负责加载 AssetSpider 的运行配置。
package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/SilenCN/AssetSpider/internal/utils"
	"gopkg.in/yaml.v3"
)

// Config 是应用的完整配置。
type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Database  DatabaseConfig  `yaml:"database"`
	Web       WebConfig       `yaml:"web"`
	Collector CollectorConfig `yaml:"collector"`
	Auth      AuthConfig      `yaml:"auth"`

	// 以下为非导出元数据，不参与 YAML 序列化：仅服务于运行时的「在线改配置」。
	path    string          // 配置文件路径，写回用；空表示不持久化
	envKeys map[string]bool // 被环境变量覆盖的 collector YAML 键，页面只读
}

// AuthConfig 描述登录认证与会话策略。
type AuthConfig struct {
	TokenTTLHours int `yaml:"token_ttl_hours"` // 会话令牌有效期（小时）
	BCryptCost    int `yaml:"bcrypt_cost"`     // bcrypt 哈希强度，范围 4..31
}

// ServerConfig 描述 HTTP 服务。
type ServerConfig struct {
	Addr string `yaml:"addr"`
}

// DatabaseConfig 描述数据存储后端。默认 SQLite（沿用 path 文件路径）；
// driver=mysql 时改走 DSN 连接，连接池参数仅对 MySQL 生效。
type DatabaseConfig struct {
	Driver             string `yaml:"driver"`                    // sqlite（默认）| mysql
	Path               string `yaml:"path"`                      // SQLite 数据库文件
	DSN                string `yaml:"dsn"`                       // MySQL 连接串，含账号密码，不对外展示
	MaxOpenConns       int    `yaml:"max_open_conns"`            // 连接池上限，0 用默认 16
	MaxIdleConns       int    `yaml:"max_idle_conns"`            // 空闲连接上限，0 用默认（上限的一半）
	ConnMaxLifetimeMin int    `yaml:"conn_max_lifetime_minutes"` // 连接最长存活分钟数，0 用默认 30
}

// Display 返回可安全写入页面与健康检查响应的数据库描述。
// MySQL 连接串含密码，这里只保留网络地址与库名；SQLite 直接返回文件路径。
func (d DatabaseConfig) Display() string {
	if d.Driver == "mysql" {
		s := strings.TrimSpace(d.DSN)
		if i := strings.LastIndexByte(s, '@'); i >= 0 {
			s = s[i+1:]
		}
		if i := strings.IndexByte(s, '?'); i >= 0 {
			s = s[:i]
		}
		return "mysql://" + s
	}
	return d.Path
}

// Validate 校验数据库后端配置（driver / path / dsn / 连接池）。
// 与 Config.Validate 里的数据库段规则一致，但只校验数据库段，
// 便于「测试连接」在保存前单独校验，而不必牵动采集配置。
func (d DatabaseConfig) Validate() error {
	driver := strings.ToLower(strings.TrimSpace(d.Driver))
	if driver == "" {
		driver = "sqlite"
	}
	switch driver {
	case "sqlite":
		if strings.TrimSpace(d.Path) == "" {
			return fmt.Errorf("database.path 不能为空（driver=sqlite）")
		}
	case "mysql":
		if strings.TrimSpace(d.DSN) == "" {
			return fmt.Errorf("database.dsn 不能为空（driver=mysql）")
		}
		if d.MaxOpenConns < 0 || d.MaxIdleConns < 0 || d.ConnMaxLifetimeMin < 0 {
			return fmt.Errorf("database 连接池参数不能为负")
		}
	default:
		return fmt.Errorf("database.driver 只支持 sqlite / mysql，实际 %q", d.Driver)
	}
	return nil
}

// WebConfig 描述前端静态资源。
type WebConfig struct {
	Dir string `yaml:"dir"`
}

// CollectorConfig 描述视频站点 JSON 采集器。
type CollectorConfig struct {
	TimeoutSeconds      int    `yaml:"timeout_seconds" json:"timeoutSeconds"`                    // 单次 HTTP 请求超时（秒）
	MaxPages            int    `yaml:"max_pages" json:"maxPages"`                                // 单次采集最多遍历的页数，防止失控
	PageSize            int    `yaml:"page_size" json:"pageSize"`                                // 0 表示由站点返回决定；>0 时附加苹果CMS 的 pagesize 参数（通常上限 100）
	UserAgent           string `yaml:"user_agent" json:"userAgent"`                              // 请求 UA
	DefaultHours        int    `yaml:"default_incremental_hours" json:"defaultIncrementalHours"` // 站点未配置时默认增量时间窗
	MaxConcurrent       int    `yaml:"max_concurrent" json:"maxConcurrent"`                      // 同时运行的采集任务上限，超出部分排队；运行时可通过接口调整
	RetryTimes          int    `yaml:"retry_times" json:"retryTimes"`                            // 单次采集失败后的自动重试次数，0 表示不自动重试
	RetryBackoffSeconds int    `yaml:"retry_backoff_seconds" json:"retryBackoffSeconds"`         // 首次自动重试的等待秒数，之后按 2 倍退避（上限 30 秒）
	ProgressSaveSeconds int    `yaml:"progress_save_seconds" json:"progressSaveSeconds"`         // 运行中任务把进度写库的间隔（秒），默认 60，避免重启丢进度
	PageIntervalMs      int    `yaml:"page_interval_ms" json:"pageIntervalMs"`                   // 相邻两页之间的间隔（毫秒），0 表示不限速；用于降低对站点的请求频率
}

// Default 返回开箱即用的默认配置。
func Default() *Config {
	return &Config{
		Server: ServerConfig{Addr: ":8080"},
		Database: DatabaseConfig{
			Driver: "sqlite", Path: "data/assetspider.db",
			MaxOpenConns: 16, MaxIdleConns: 8, ConnMaxLifetimeMin: 30,
		},
		Web: WebConfig{Dir: "web/dist"},
		Collector: CollectorConfig{
			TimeoutSeconds:      20,
			MaxPages:            1000,
			PageSize:            0,
			UserAgent:           "AssetSpider/1.0 (+maccms-json-collector)",
			DefaultHours:        24,
			MaxConcurrent:       3,
			RetryTimes:          2,
			RetryBackoffSeconds: 3,
			ProgressSaveSeconds: 60,
			PageIntervalMs:      0,
		},
		Auth: AuthConfig{TokenTTLHours: 168, BCryptCost: 10},
	}
}

// Load 读取 path 指定的 YAML 配置文件，未提供的字段沿用默认值，
// 随后用 ASSETSPIDER_* 环境变量覆盖，最后校验必填项。
func Load(path string) (*Config, error) {
	cfg := Default()
	cfg.path = path
	cfg.envKeys = map[string]bool{}
	// 记录哪些 collector / database 键被环境变量接管：这些字段在线不可改
	//（改了也会被下次启动覆盖）。env 变量名同时映射到两个覆盖表里。
	markEnv := func(name string) {
		if key, ok := EnvOverrides[name]; ok {
			cfg.envKeys[key] = true
			return
		}
		if key, ok := DBEnvOverrides[name]; ok {
			cfg.envKeys[key] = true
		}
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取配置文件 %q: %w", path, err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("解析配置文件 %q: %w", path, err)
		}
	}

	if v := os.Getenv("ASSETSPIDER_SERVER_ADDR"); v != "" {
		cfg.Server.Addr = v
	}
	if v := os.Getenv("ASSETSPIDER_DB_PATH"); v != "" {
		cfg.Database.Path = v
		markEnv("ASSETSPIDER_DB_PATH")
	}
	if v := os.Getenv("ASSETSPIDER_DB_DRIVER"); v != "" {
		cfg.Database.Driver = v
		markEnv("ASSETSPIDER_DB_DRIVER")
	}
	if v := os.Getenv("ASSETSPIDER_DB_DSN"); v != "" {
		cfg.Database.DSN = v
		markEnv("ASSETSPIDER_DB_DSN")
	}
	for _, item := range []struct {
		name string
		dst  *int
	}{
		{"ASSETSPIDER_DB_MAX_OPEN_CONNS", &cfg.Database.MaxOpenConns},
		{"ASSETSPIDER_DB_MAX_IDLE_CONNS", &cfg.Database.MaxIdleConns},
		{"ASSETSPIDER_DB_CONN_MAX_LIFETIME", &cfg.Database.ConnMaxLifetimeMin},
	} {
		before := *item.dst
		if err := utils.EnvInt(item.name, item.dst); err != nil {
			return nil, err
		}
		if *item.dst != before {
			markEnv(item.name)
		}
	}
	if v := os.Getenv("ASSETSPIDER_WEB_DIR"); v != "" {
		cfg.Web.Dir = v
	}
	if v := os.Getenv("ASSETSPIDER_COLLECTOR_USER_AGENT"); v != "" {
		cfg.Collector.UserAgent = v
		markEnv("ASSETSPIDER_COLLECTOR_USER_AGENT")
	}
	for _, item := range []struct {
		name string
		dst  *int
	}{
		{"ASSETSPIDER_COLLECTOR_TIMEOUT", &cfg.Collector.TimeoutSeconds},
		{"ASSETSPIDER_COLLECTOR_MAX_PAGES", &cfg.Collector.MaxPages},
		{"ASSETSPIDER_COLLECTOR_PAGE_SIZE", &cfg.Collector.PageSize},
		{"ASSETSPIDER_COLLECTOR_DEFAULT_HOURS", &cfg.Collector.DefaultHours},
		{"ASSETSPIDER_COLLECTOR_MAX_CONCURRENT", &cfg.Collector.MaxConcurrent},
		{"ASSETSPIDER_COLLECTOR_RETRY_TIMES", &cfg.Collector.RetryTimes},
		{"ASSETSPIDER_COLLECTOR_RETRY_BACKOFF", &cfg.Collector.RetryBackoffSeconds},
		{"ASSETSPIDER_COLLECTOR_PROGRESS_SAVE", &cfg.Collector.ProgressSaveSeconds},
		{"ASSETSPIDER_COLLECTOR_PAGE_INTERVAL", &cfg.Collector.PageIntervalMs},
	} {
		before := *item.dst
		if err := utils.EnvInt(item.name, item.dst); err != nil {
			return nil, err
		}
		if *item.dst != before {
			markEnv(item.name)
		}
	}
	// 兼容旧版本环境变量名：并行站点数已升级为全局任务并发上限。
	before := cfg.Collector.MaxConcurrent
	if err := utils.EnvInt("ASSETSPIDER_COLLECTOR_PARALLEL", &cfg.Collector.MaxConcurrent); err != nil {
		return nil, err
	}
	if cfg.Collector.MaxConcurrent != before {
		markEnv("ASSETSPIDER_COLLECTOR_PARALLEL")
	}
	for _, item := range []struct {
		name string
		dst  *int
	}{
		{"ASSETSPIDER_AUTH_TOKEN_TTL_HOURS", &cfg.Auth.TokenTTLHours},
		{"ASSETSPIDER_AUTH_BCRYPT_COST", &cfg.Auth.BCryptCost},
	} {
		if err := utils.EnvInt(item.name, item.dst); err != nil {
			return nil, err
		}
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate 校验必填配置项。
func (c *Config) Validate() error {
	if c.Server.Addr == "" {
		return fmt.Errorf("server.addr 不能为空")
	}
	// 先规范化驱动名：缺省 / 大小写差异都归一到 sqlite，后续判断只看一个值。
	c.Database.Driver = strings.ToLower(strings.TrimSpace(c.Database.Driver))
	if c.Database.Driver == "" {
		c.Database.Driver = "sqlite"
	}
	switch c.Database.Driver {
	case "sqlite":
		if c.Database.Path == "" {
			return fmt.Errorf("database.path 不能为空（driver=sqlite）")
		}
	case "mysql":
		if strings.TrimSpace(c.Database.DSN) == "" {
			return fmt.Errorf("database.dsn 不能为空（driver=mysql）")
		}
		if c.Database.MaxOpenConns < 0 || c.Database.MaxIdleConns < 0 || c.Database.ConnMaxLifetimeMin < 0 {
			return fmt.Errorf("database 连接池参数不能为负")
		}
	default:
		return fmt.Errorf("database.driver 只支持 sqlite / mysql，实际 %q", c.Database.Driver)
	}
	if c.Web.Dir == "" {
		return fmt.Errorf("web.dir 不能为空")
	}
	cc := c.Collector
	if cc.TimeoutSeconds <= 0 {
		return fmt.Errorf("collector.timeout_seconds 必须大于 0")
	}
	if cc.MaxPages <= 0 {
		return fmt.Errorf("collector.max_pages 必须大于 0")
	}
	if cc.PageSize < 0 {
		return fmt.Errorf("collector.page_size 不能为负")
	}
	if cc.DefaultHours <= 0 {
		return fmt.Errorf("collector.default_incremental_hours 必须大于 0")
	}
	if cc.MaxConcurrent <= 0 {
		return fmt.Errorf("collector.max_concurrent 必须大于 0")
	}
	if cc.RetryTimes < 0 {
		return fmt.Errorf("collector.retry_times 不能为负")
	}
	if cc.RetryBackoffSeconds <= 0 {
		return fmt.Errorf("collector.retry_backoff_seconds 必须大于 0")
	}
	if cc.ProgressSaveSeconds <= 0 {
		return fmt.Errorf("collector.progress_save_seconds 必须大于 0")
	}
	if cc.PageIntervalMs < 0 || cc.PageIntervalMs > 60000 {
		return fmt.Errorf("collector.page_interval_ms 需在 0..60000 之间（0 表示不限速）")
	}
	if c.Auth.TokenTTLHours <= 0 {
		return fmt.Errorf("auth.token_ttl_hours 必须大于 0")
	}
	// bcrypt 的合法强度区间为 4..31（bcrypt.MinCost / bcrypt.MaxCost）。
	if c.Auth.BCryptCost < 4 || c.Auth.BCryptCost > 31 {
		return fmt.Errorf("auth.bcrypt_cost 需在 4..31 之间")
	}
	return nil
}
