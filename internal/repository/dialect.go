package repository

// 数据库方言与连接装配。
//
// 业务代码只认 GORM 的标准 API；SQLite 专有的 PRAGMA / FTS5 虚拟表 / bigram
// 触发器集中在 store.go 的 SQLite 分支，MySQL 专有的 FULLTEXT ngram 索引集中在
// video_search_mysql.go。这样任一方言的读写都不会误跑到另一方的 DDL 上。

import (
	"fmt"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Dialect 标识底层数据库方言。
type Dialect string

const (
	DialectSQLite Dialect = "sqlite"
	DialectMySQL  Dialect = "mysql"
)

// mysqlTableOptions 让 MySQL 下新建的表统一 utf8mb4，否则中文片名、演员名
// 会因库默认字符集落到 latin1/utf8（3 字节）而被截断或乱码。
const mysqlTableOptions = "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci"

// Options 是打开数据库所需的全部参数，由 app 装配层从 config.DatabaseConfig 映射。
// 放在 repository 包内定义，避免仓储层反向依赖 config 包（分层约束）。
type Options struct {
	Driver             string // sqlite（默认）| mysql
	Path               string // SQLite 文件路径
	DSN                string // MySQL 连接串
	MaxOpenConns       int    // MySQL 连接池上限，<=0 用默认
	MaxIdleConns       int    // MySQL 空闲连接上限，<=0 用默认
	ConnMaxLifetimeMin int    // MySQL 连接存活分钟数，<=0 用默认
	Display            string // 对外展示用的脱敏描述；留空时回退到 Path
}

func (o Options) dialect() Dialect {
	if strings.EqualFold(strings.TrimSpace(o.Driver), "mysql") {
		return DialectMySQL
	}
	return DialectSQLite
}

// databaseDisplay 返回健康检查与设置页可以安全展示的数据库描述。
// MySQL 的 DSN 含密码，必须由 config.DatabaseConfig.Display() 预先脱敏后传入。
func (o Options) databaseDisplay() string {
	if o.Display != "" {
		return o.Display
	}
	if o.dialect() == DialectMySQL {
		return "mysql"
	}
	return o.Path
}

// dialectOf 从已建好的 GORM 连接反推方言，供独立构造的仓储对象（含测试）使用。
func dialectOf(db *gorm.DB) Dialect {
	if db != nil && db.Dialector != nil && db.Dialector.Name() == "mysql" {
		return DialectMySQL
	}
	return DialectSQLite
}

// openDB 建立底层连接。SQLite 限制为单连接（单写者模型，避免 database is locked），
// MySQL 按配置设置连接池。
func openDB(o Options) (*gorm.DB, error) {
	cfg := &gorm.Config{Logger: logger.Default.LogMode(logger.Warn)}
	if o.dialect() == DialectMySQL {
		dsn, err := normalizeMySQLDSN(o.DSN)
		if err != nil {
			return nil, err
		}
		db, err := gorm.Open(mysql.Open(dsn), cfg)
		if err != nil {
			return nil, fmt.Errorf("连接 MySQL: %w", err)
		}
		configureMySQLPool(db, o)
		return db, nil
	}
	if o.Path == "" {
		return nil, fmt.Errorf("SQLite 数据库路径不能为空")
	}
	db, err := gorm.Open(sqlite.Open(sqliteDSN(o.Path)), cfg)
	if err != nil {
		return nil, fmt.Errorf("打开 SQLite %q: %w", o.Path, err)
	}
	// SQLite 是单写者数据库；后台维护任务另开一条连接，写锁冲突由 DSN 上的
	// busy_timeout 化解。
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	return db, nil
}

// configureMySQLPool 设置连接池上限与连接存活时间，避免长连接被服务端
// wait_timeout 断开后拿到坏连接。
func configureMySQLPool(db *gorm.DB, o Options) {
	sqlDB, err := db.DB()
	if err != nil {
		return
	}
	maxOpen := o.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = 16
	}
	maxIdle := o.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = maxOpen / 2
	}
	if maxIdle > maxOpen {
		maxIdle = maxOpen
	}
	lifetime := o.ConnMaxLifetimeMin
	if lifetime <= 0 {
		lifetime = 30
	}
	sqlDB.SetMaxOpenConns(maxOpen)
	sqlDB.SetMaxIdleConns(maxIdle)
	sqlDB.SetConnMaxLifetime(time.Duration(lifetime) * time.Minute)
}

// normalizeMySQLDSN 补齐 GORM 依赖的连接参数，避免因用户 DSN 写漏导致
// time.Time 扫描失败或中文落到非 utf8mb4 字符集：
//
//   - parseTime=true：DATETIME / TIMESTAMP 才能扫进 time.Time；
//   - charset=utf8mb4：中文与 emoji 完整写入；
//   - loc=Local：未显式指定时按本机时区解释时间。
//
// 用驱动自带的 ParseDSN / FormatDSN 处理，避免字符串拼接破坏含特殊字符的密码。
func normalizeMySQLDSN(raw string) (string, error) {
	cfg, err := mysqldriver.ParseDSN(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("解析 MySQL DSN: %w", err)
	}
	// 用户是否在 DSN 里显式写了这些参数。ParseDSN 对 loc 的默认值是 UTC、
	// 对 parseTime 默认是 false，仅看 cfg 字段无法区分「用户没写」与「用户写了 UTC」，
	// 所以直接从原始 query 串里判定，避免覆盖用户显式配置（见测试）。
	explicit := explicitMySQLParams(strings.TrimSpace(raw))
	if !explicit["parseTime"] && !cfg.ParseTime {
		cfg.ParseTime = true
	}
	// 用户没写 loc 才补本机时区；显式写了 UTC/Asia/Shanghai 等一律保留。
	if !explicit["loc"] {
		cfg.Loc = time.Local
	}
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	if !explicit["charset"] && cfg.Params["charset"] == "" {
		cfg.Params["charset"] = "utf8mb4"
	}
	return cfg.FormatDSN(), nil
}

// explicitMySQLParams 解析 DSN 末尾的 query 串，返回 loc / charset / parseTime
// 是否被用户显式指定。只关心「有没有这个键」，值留给 ParseDSN 标准化。
func explicitMySQLParams(raw string) map[string]bool {
	out := map[string]bool{}
	if i := strings.Index(raw, "?"); i >= 0 {
		for _, kv := range strings.Split(raw[i+1:], "&") {
			k, _, _ := strings.Cut(kv, "=")
			switch strings.ToLower(strings.TrimSpace(k)) {
			case "loc", "charset", "parsetime":
				out[strings.ToLower(k)] = true
			}
		}
	}
	return out
}
