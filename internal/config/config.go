// Package config 负责加载 AssetSpider 的运行配置。
package config

import (
	"fmt"
	"os"

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

// DatabaseConfig 描述 SQLite 数据库。
type DatabaseConfig struct {
	Path string `yaml:"path"`
}

// WebConfig 描述前端静态资源。
type WebConfig struct {
	Dir string `yaml:"dir"`
}

// CollectorConfig 描述视频站点 JSON 采集器。
type CollectorConfig struct {
	TimeoutSeconds      int    `yaml:"timeout_seconds"`           // 单次 HTTP 请求超时（秒）
	MaxPages            int    `yaml:"max_pages"`                 // 单次采集最多遍历的页数，防止失控
	PageSize            int    `yaml:"page_size"`                 // 0 表示由站点返回决定；>0 时附加苹果CMS 的 pagesize 参数（通常上限 100）
	UserAgent           string `yaml:"user_agent"`                // 请求 UA
	DefaultHours        int    `yaml:"default_incremental_hours"` // 站点未配置时默认增量时间窗
	MaxConcurrent       int    `yaml:"max_concurrent"`            // 同时运行的采集任务上限，超出部分排队；运行时可通过接口调整
	RetryTimes          int    `yaml:"retry_times"`               // 单次采集失败后的自动重试次数，0 表示不自动重试
	RetryBackoffSeconds int    `yaml:"retry_backoff_seconds"`     // 首次自动重试的等待秒数，之后按 2 倍退避（上限 30 秒）
	ProgressSaveSeconds int    `yaml:"progress_save_seconds"`     // 运行中任务把进度写库的间隔（秒），默认 60，避免重启丢进度
}

// Default 返回开箱即用的默认配置。
func Default() *Config {
	return &Config{
		Server:   ServerConfig{Addr: ":8080"},
		Database: DatabaseConfig{Path: "data/assetspider.db"},
		Web:      WebConfig{Dir: "web/dist"},
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
		},
		Auth: AuthConfig{TokenTTLHours: 168, BCryptCost: 10},
	}
}

// Load 读取 path 指定的 YAML 配置文件，未提供的字段沿用默认值，
// 随后用 ASSETSPIDER_* 环境变量覆盖，最后校验必填项。
func Load(path string) (*Config, error) {
	cfg := Default()
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
	}
	if v := os.Getenv("ASSETSPIDER_WEB_DIR"); v != "" {
		cfg.Web.Dir = v
	}
	if v := os.Getenv("ASSETSPIDER_COLLECTOR_USER_AGENT"); v != "" {
		cfg.Collector.UserAgent = v
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
	} {
		if err := utils.EnvInt(item.name, item.dst); err != nil {
			return nil, err
		}
	}
	// 兼容旧版本环境变量名：并行站点数已升级为全局任务并发上限。
	if err := utils.EnvInt("ASSETSPIDER_COLLECTOR_PARALLEL", &cfg.Collector.MaxConcurrent); err != nil {
		return nil, err
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
	if c.Database.Path == "" {
		return fmt.Errorf("database.path 不能为空")
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
	if c.Auth.TokenTTLHours <= 0 {
		return fmt.Errorf("auth.token_ttl_hours 必须大于 0")
	}
	// bcrypt 的合法强度区间为 4..31（bcrypt.MinCost / bcrypt.MaxCost）。
	if c.Auth.BCryptCost < 4 || c.Auth.BCryptCost > 31 {
		return fmt.Errorf("auth.bcrypt_cost 需在 4..31 之间")
	}
	return nil
}
