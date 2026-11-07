// Package config 负责加载 AssetSpider 的运行配置。
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config 是应用的完整配置。
type Config struct {
	Server   ServerConfig   `yaml:"server"`
	Database DatabaseConfig `yaml:"database"`
	Web      WebConfig      `yaml:"web"`
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

// Default 返回开箱即用的默认配置。
func Default() *Config {
	return &Config{
		Server:   ServerConfig{Addr: ":8080"},
		Database: DatabaseConfig{Path: "data/assetspider.db"},
		Web:      WebConfig{Dir: "web/dist"},
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
	return nil
}
