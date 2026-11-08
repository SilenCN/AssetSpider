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
	TimeoutSeconds int    `yaml:"timeout_seconds"`           // 单次 HTTP 请求超时（秒）
	MaxPages       int    `yaml:"max_pages"`                 // 单次采集最多遍历的页数，防止失控
	PageSize       int    `yaml:"page_size"`                 // 0 表示由站点返回决定；>0 时附加苹果CMS 的 pagesize 参数（通常上限 100）
	UserAgent      string `yaml:"user_agent"`                // 请求 UA
	DefaultHours   int    `yaml:"default_incremental_hours"` // 站点未配置时默认增量时间窗
	Parallel       int    `yaml:"parallel"`                  // 批量采集的并发站点数，建议保持 1
}

// Default 返回开箱即用的默认配置。
func Default() *Config {
	return &Config{
		Server:   ServerConfig{Addr: ":8080"},
		Database: DatabaseConfig{Path: "data/assetspider.db"},
		Web:      WebConfig{Dir: "web/dist"},
		Collector: CollectorConfig{
			TimeoutSeconds: 20,
			MaxPages:       1000,
			PageSize:       0,
			UserAgent:      "AssetSpider/1.0 (+maccms-json-collector)",
			DefaultHours:   24,
			Parallel:       1,
		},
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
		{"ASSETSPIDER_COLLECTOR_PARALLEL", &cfg.Collector.Parallel},
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
	if cc.Parallel <= 0 {
		return fmt.Errorf("collector.parallel 必须大于 0")
	}
	return nil
}
