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
	"ASSETSPIDER_COLLECTOR_PAGE_INTERVAL":  "page_interval_ms",
	"ASSETSPIDER_COLLECTOR_USER_AGENT":     "user_agent",
}

// DBEnvOverrides 记录哪些 database 字段被环境变量覆盖（值为对应的 YAML 键名）。
// 与 EnvOverrides 同样的逻辑：被环境变量接管的字段在设置页只读。
var DBEnvOverrides = map[string]string{
	"ASSETSPIDER_DB_DRIVER":            "driver",
	"ASSETSPIDER_DB_PATH":              "path",
	"ASSETSPIDER_DB_DSN":               "dsn",
	"ASSETSPIDER_DB_MAX_OPEN_CONNS":    "max_open_conns",
	"ASSETSPIDER_DB_MAX_IDLE_CONNS":    "max_idle_conns",
	"ASSETSPIDER_DB_CONN_MAX_LIFETIME": "conn_max_lifetime_minutes",
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
	case "page_interval_ms":
		return a.PageIntervalMs == b.PageIntervalMs
	case "user_agent":
		return a.UserAgent == b.UserAgent
	}
	return true
}

// dbFieldEqual 比较某个 YAML 键在两份数据库配置里的值是否一致。
func dbFieldEqual(key string, a, b DatabaseConfig) bool {
	switch key {
	case "driver":
		return a.Driver == b.Driver
	case "path":
		return a.Path == b.Path
	case "dsn":
		return a.DSN == b.DSN
	case "max_open_conns":
		return a.MaxOpenConns == b.MaxOpenConns
	case "max_idle_conns":
		return a.MaxIdleConns == b.MaxIdleConns
	case "conn_max_lifetime_minutes":
		return a.ConnMaxLifetimeMin == b.ConnMaxLifetimeMin
	}
	return true
}

// UpdateDatabase 校验并在线更新数据库配置：写回 YAML → 替换内存快照。
//
// 数据库后端在下次启动时由 repository.Open 按新配置重建连接，因此此处只持久化与内存
// 快照，不热重载连接（与「静态启动项」语义一致）。被环境变量覆盖的字段若与当前值不同
// 则拒绝修改。DSN 含密码，页面不回显明文：留空且为 MySQL 时沿用现有连接串，避免误清空。
func (s *Store) UpdateDatabase(in DatabaseConfig) (Config, error) {
	next := s.Current()

	// 1) 不让页面改动被环境变量接管的字段（改动会被下次启动覆盖）。
	var locked []string
	for envName, key := range DBEnvOverrides {
		if !s.envKeys[key] {
			continue
		}
		if !dbFieldEqual(key, in, next.Database) {
			locked = append(locked, fmt.Sprintf("%s（由 %s 提供）", key, envName))
		}
	}
	if len(locked) > 0 {
		sort.Strings(locked)
		return Config{}, fmt.Errorf("以下数据库配置由环境变量控制，请在启动环境里修改，页面不可改：%s", strings.Join(locked, "、"))
	}

	// 2) DSN 处理：SQLite 不需要 DSN，统一清空；MySQL 留空则沿用现有连接串。
	if in.Driver == "sqlite" {
		in.DSN = ""
	} else if in.DSN == "" {
		in.DSN = next.Database.DSN
	}

	// 3) 校验整份配置（只替换 database，其余段原样保留）。
	next.Database = in
	if err := next.Validate(); err != nil {
		return Config{}, err
	}

	// 4) 先落盘，再换内存快照：落盘失败就整体失败，避免不一致。
	if s.path != "" {
		if err := writeDatabaseYAML(s.path, in); err != nil {
			return Config{}, fmt.Errorf("写回配置文件失败: %w", err)
		}
	}
	s.mu.Lock()
	s.cfg = next
	s.mu.Unlock()
	return next, nil
}

// scalarItem 描述一个配置字段如何映射到 YAML 标量节点（键 / 值 / 类型标签）。
type scalarItem struct {
	key   string
	value string
	tag   string // !!int / !!str
}

// collectorScalars 把采集配置转成可写回 YAML 的标量列表。
func collectorScalars(cc CollectorConfig) []scalarItem {
	return []scalarItem{
		{"timeout_seconds", strconv.Itoa(cc.TimeoutSeconds), "!!int"},
		{"max_pages", strconv.Itoa(cc.MaxPages), "!!int"},
		{"page_size", strconv.Itoa(cc.PageSize), "!!int"},
		{"default_incremental_hours", strconv.Itoa(cc.DefaultHours), "!!int"},
		{"max_concurrent", strconv.Itoa(cc.MaxConcurrent), "!!int"},
		{"retry_times", strconv.Itoa(cc.RetryTimes), "!!int"},
		{"retry_backoff_seconds", strconv.Itoa(cc.RetryBackoffSeconds), "!!int"},
		{"progress_save_seconds", strconv.Itoa(cc.ProgressSaveSeconds), "!!int"},
		{"page_interval_ms", strconv.Itoa(cc.PageIntervalMs), "!!int"},
		{"user_agent", cc.UserAgent, "!!str"},
	}
}

// databaseScalars 把数据库配置转成可写回 YAML 的标量列表。
func databaseScalars(db DatabaseConfig) []scalarItem {
	return []scalarItem{
		{"driver", db.Driver, "!!str"},
		{"path", db.Path, "!!str"},
		{"dsn", db.DSN, "!!str"},
		{"max_open_conns", strconv.Itoa(db.MaxOpenConns), "!!int"},
		{"max_idle_conns", strconv.Itoa(db.MaxIdleConns), "!!int"},
		{"conn_max_lifetime_minutes", strconv.Itoa(db.ConnMaxLifetimeMin), "!!int"},
	}
}

// writeCollectorYAML 只改写 collector 段里我们认识的那几个键，保留其余内容、注释与顺序。
func writeCollectorYAML(path string, cc CollectorConfig) error {
	return writeSectionYAML(path, "collector", collectorScalars(cc))
}

// writeDatabaseYAML 只改写 database 段，保留其余内容与注释（与 collector 同一套写回逻辑）。
func writeDatabaseYAML(path string, db DatabaseConfig) error {
	return writeSectionYAML(path, "database", databaseScalars(db))
}

// writeSectionYAML 把给定段里我们认识的键更新为新值，保留其余内容、注释与顺序。
// 用 yaml.Node 逐节点更新，而不是把整份配置重新 Marshal——后者会丢掉所有注释。
func writeSectionYAML(path, section string, scalars []scalarItem) error {
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
	target := mappingValue(root, section)
	if target == nil || target.Kind != yaml.MappingNode {
		return fmt.Errorf("配置文件缺少 %s 段", section)
	}

	present := map[string]bool{}
	for i := 0; i+1 < len(target.Content); i += 2 {
		key := target.Content[i].Value
		val := target.Content[i+1]
		for _, sc := range scalars {
			if key == sc.key {
				val.Tag = sc.tag
				val.Value = sc.value
				val.Style = 0 // 去掉旧的引号/样式，让数字与字符串按规范输出
				present[key] = true
			}
		}
	}
	// 补齐配置文件里尚不存在的键（例如老版本配置没有 retry_times / dsn）。
	for _, sc := range scalars {
		if present[sc.key] {
			continue
		}
		target.Content = append(target.Content,
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
