package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// EnvOverrides 记录哪些 collector 字段被环境变量覆盖（值为对应的 YAML 键名）。
// 这些字段在页面上只读：在线改了也会被下一次启动的环境变量重新盖掉，
// 与其让人误解，不如明确标出来并禁止编辑。
var EnvOverrides = map[string]string{
	"ASSETSPIDER_COLLECTOR_TIMEOUT":        "timeout_seconds",
	"ASSETSPIDER_COLLECTOR_MAX_PAGES":      "max_pages",
	"ASSETSPIDER_COLLECTOR_PAGE_SIZE":      "page_size",
	"ASSETSPIDER_COLLECTOR_DEFAULT_HOURS":  "default_incremental_hours",
	"ASSETSPIDER_COLLECTOR_MAX_CONCURRENT": "max_concurrent",
	"ASSETSPIDER_COLLECTOR_PARALLEL":       "max_concurrent", // 旧变量名
	"ASSETSPIDER_COLLECTOR_RETRY_TIMES":    "retry_times",
	"ASSETSPIDER_COLLECTOR_RETRY_BACKOFF":  "retry_backoff_seconds",
	"ASSETSPIDER_COLLECTOR_PROGRESS_SAVE":  "progress_save_seconds",
	"ASSETSPIDER_COLLECTOR_USER_AGENT":     "user_agent",
}

// Store 是运行时配置的线程安全容器。
//
// 读路径完全无锁竞争：读方法拿 RLock 复制一份 CollectorConfig 值返回；写方法在
// 校验通过后先写回配置文件（失败即整体失败，不会出现「内存改了盘上没改」），
// 再短暂持写锁替换快照。这样采集循环 / Fetcher 每页都能读到最新值，实现即时生效，
// 而重启后新值也从配置文件恢复。
type Store struct {
	mu      sync.RWMutex
	cfg     Config
	path    string          // 配置文件路径；为空表示不持久化（测试环境）
	envKeys map[string]bool // 被环境变量覆盖的 collector 键，页面只读
}

// NewStore 从已加载的配置创建运行时存储。
func NewStore(cfg *Config) *Store {
	envKeys := cfg.envKeys
	if envKeys == nil {
		envKeys = map[string]bool{}
	}
	return &Store{cfg: *cfg, path: cfg.path, envKeys: envKeys}
}

// Current 返回整份配置的快照（值拷贝，调用方可随意改动而不影响存储）。
func (s *Store) Current() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Collector 返回当前采集器配置的快照；采集热路径每页调用一次。
func (s *Store) Collector() CollectorConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Collector
}

// Path 返回配置文件路径。
func (s *Store) Path() string { return s.path }

// EnvOverridden 返回被环境变量覆盖、因而不允许在线编辑的 YAML 键列表（有序）。
func (s *Store) EnvOverridden() []string {
	out := make([]string, 0, len(s.envKeys))
	for k := range s.envKeys {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// UpdateCollector 校验并在线更新采集器配置：写回 YAML → 替换内存快照。
// 返回更新后的完整配置。被环境变量覆盖的字段若与当前值不同则拒绝修改。
func (s *Store) UpdateCollector(in CollectorConfig) (Config, error) {
	next := s.Current()

	// 1) 不让页面改动被环境变量接管的字段（改动会被下次启动覆盖，纯属误导）。
	var locked []string
	for envName, key := range EnvOverrides {
		if !s.envKeys[key] {
			continue
		}
		if !collectorFieldEqual(key, in, next.Collector) {
			locked = append(locked, fmt.Sprintf("%s（由 %s 提供）", key, envName))
		}
	}
	if len(locked) > 0 {
		sort.Strings(locked)
		return Config{}, fmt.Errorf("以下配置由环境变量控制，请在启动环境里修改，页面不可改：%s", strings.Join(locked, "、"))
	}

	// 2) 校验整份配置（只替换 collector，其余段原样保留）。
	next.Collector = in
	if err := next.Validate(); err != nil {
		return Config{}, err
	}

	// 3) 先落盘，再换内存快照：落盘失败就整体失败，避免不一致。
	if s.path != "" {
		if err := writeCollectorYAML(s.path, in); err != nil {
			return Config{}, fmt.Errorf("写回配置文件失败: %w", err)
		}
	}
	s.mu.Lock()
	s.cfg = next
	s.mu.Unlock()
	return next, nil
}

// collectorFieldEqual 比较某个 YAML 键在两份配置里的值是否一致。
func collectorFieldEqual(key string, a, b CollectorConfig) bool {
	switch key {
	case "timeout_seconds":
		return a.TimeoutSeconds == b.TimeoutSeconds
	case "max_pages":
		return a.MaxPages == b.MaxPages
	case "page_size":
		return a.PageSize == b.PageSize
	case "default_incremental_hours":
		return a.DefaultHours == b.DefaultHours
	case "max_concurrent":
		return a.MaxConcurrent == b.MaxConcurrent
	case "retry_times":
		return a.RetryTimes == b.RetryTimes
	case "retry_backoff_seconds":
		return a.RetryBackoffSeconds == b.RetryBackoffSeconds
	case "progress_save_seconds":
		return a.ProgressSaveSeconds == b.ProgressSaveSeconds
	case "user_agent":
		return a.UserAgent == b.UserAgent
	}
	return true
}

// collectorScalar 描述一个 collector 字段如何映射到 YAML 节点值。
type collectorScalar struct {
	key   string
	value string
	tag   string // !!int / !!str
	apply func(*CollectorConfig, string) error
}

func collectorScalars(cc CollectorConfig) []collectorScalar {
	return []collectorScalar{
		{"timeout_seconds", strconv.Itoa(cc.TimeoutSeconds), "!!int", func(c *CollectorConfig, v string) error { n, e := strconv.Atoi(v); c.TimeoutSeconds = n; return e }},
		{"max_pages", strconv.Itoa(cc.MaxPages), "!!int", func(c *CollectorConfig, v string) error { n, e := strconv.Atoi(v); c.MaxPages = n; return e }},
		{"page_size", strconv.Itoa(cc.PageSize), "!!int", func(c *CollectorConfig, v string) error { n, e := strconv.Atoi(v); c.PageSize = n; return e }},
		{"default_incremental_hours", strconv.Itoa(cc.DefaultHours), "!!int", func(c *CollectorConfig, v string) error { n, e := strconv.Atoi(v); c.DefaultHours = n; return e }},
		{"max_concurrent", strconv.Itoa(cc.MaxConcurrent), "!!int", func(c *CollectorConfig, v string) error { n, e := strconv.Atoi(v); c.MaxConcurrent = n; return e }},
		{"retry_times", strconv.Itoa(cc.RetryTimes), "!!int", func(c *CollectorConfig, v string) error { n, e := strconv.Atoi(v); c.RetryTimes = n; return e }},
		{"retry_backoff_seconds", strconv.Itoa(cc.RetryBackoffSeconds), "!!int", func(c *CollectorConfig, v string) error { n, e := strconv.Atoi(v); c.RetryBackoffSeconds = n; return e }},
		{"progress_save_seconds", strconv.Itoa(cc.ProgressSaveSeconds), "!!int", func(c *CollectorConfig, v string) error { n, e := strconv.Atoi(v); c.ProgressSaveSeconds = n; return e }},
		{"user_agent", cc.UserAgent, "!!str", func(c *CollectorConfig, v string) error { c.UserAgent = v; return nil }},
	}
}

// writeCollectorYAML 只改写 collector 段里我们认识的那几个键，保留其余内容、注释与顺序。
// 用 yaml.Node 逐节点更新，而不是把整份配置重新 Marshal——后者会丢掉所有注释。
func writeCollectorYAML(path string, cc CollectorConfig) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("解析配置文件失败: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return fmt.Errorf("配置文件结构异常")
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("配置文件根节点不是映射")
	}
	collector := mappingValue(root, "collector")
	if collector == nil || collector.Kind != yaml.MappingNode {
		return fmt.Errorf("配置文件缺少 collector 段")
	}

	scalars := collectorScalars(cc)
	present := map[string]bool{}
	for i := 0; i+1 < len(collector.Content); i += 2 {
		key := collector.Content[i].Value
		val := collector.Content[i+1]
		for _, sc := range scalars {
			if key == sc.key {
				val.Tag = sc.tag
				val.Value = sc.value
				val.Style = 0 // 去掉旧的引号/样式，让数字与字符串按规范输出
				present[key] = true
			}
		}
	}
	// 补齐配置文件里尚不存在的键（例如老版本配置没有 retry_times）。
	for _, sc := range scalars {
		if present[sc.key] {
			continue
		}
		collector.Content = append(collector.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: sc.key},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: sc.tag, Value: sc.value})
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	return atomicWrite(path, out)
}

// mappingValue 在映射节点里按 key 找对应的 value 节点。
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// atomicWrite 先写同目录临时文件再 rename，避免写一半时崩溃留下坏配置。
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
