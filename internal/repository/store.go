// Package repository 封装基于 GORM 的 SQLite 数据访问。
//
// 每个实体拥有独立的文件、独立的对象与独立的对外接口：
//
//	asset.go    → AssetRepository     （model.Asset）
//	source.go   → SourceRepository    （model.Source）
//	video.go    → VideoRepository     （model.Video）
//	category.go → CategoryRepository  （model.Category）
//	tag.go      → TagRepository       （model.Tag / model.VideoTag）
//	job.go      → JobRepository       （model.CrawlJob）
//	user.go     → UserRepository      （model.User / model.Session / model.UserSource）
//
// 本文件只负责共享数据库连接、自动迁移、装配各实体仓储对象与连接生命周期，
// 不承载任何实体的读写方法。
package repository

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// schemaVersion 是库结构的「检索层」版本号，记在 SQLite 的 PRAGMA user_version 里。
//
// 版本号只用于判断「是否需要重算统计信息」：索引与全文索引都由 AutoMigrate /
// ensureVideoSearch 幂等创建，但 SQLite 的查询计划器要靠 ANALYZE 得到的
// sqlite_stat1 才能选对索引（例如「多站点 + 时间倒序」在没有统计信息时会退化成
// 「先按站点过滤再全量排序」）。升级后统计信息必然过期，故版本号提升时重跑一次。
//
// 新增索引 / 改变索引列时把它 +1。
const schemaVersion = 2

// Repositories 是各实体仓储的容器。
// 它只是装配结果，不是「大杂烩仓储」：任何实体读写都必须走对应的那个对象。
type Repositories struct {
	Assets     AssetRepository
	Sources    SourceRepository
	Videos     VideoRepository
	Categories CategoryRepository
	Tags       TagRepository
	Jobs       JobRepository
	Users      UserRepository

	db   *gorm.DB
	path string
}

// Open 打开（必要时创建）path 指定的 SQLite 数据库，自动迁移表结构，
// 并装配出每个实体各自的仓储对象。
func Open(path string) (*Repositories, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建数据库目录 %q: %w", dir, err)
		}
	}
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("打开 SQLite %q: %w", path, err)
	}
	// SQLite 是单写者数据库，限制连接数可避免批量采集并发写时出现 database is locked。
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	if err := db.AutoMigrate(
		&model.Asset{},
		&model.Source{},
		&model.Video{},
		&model.Category{},
		&model.Tag{},
		&model.VideoTag{},
		&model.CrawlJob{},
		&model.User{},
		&model.Session{},
		&model.UserSource{},
	); err != nil {
		return nil, fmt.Errorf("迁移表结构: %w", err)
	}
	// 全文检索索引是虚拟表 + 触发器，AutoMigrate 管不到，单独幂等创建。
	// 建不出来（例如底层 SQLite 未编译 FTS5）只降级为 LIKE 检索，不阻断启动。
	if err := ensureVideoSearch(db); err != nil {
		log.Printf("建立影片全文索引失败，关键词检索将退回 LIKE 全表扫描: %v", err)
	}
	prepareDatabase(db)
	return &Repositories{
		Assets:     NewAssetRepository(db),
		Sources:    NewSourceRepository(db),
		Videos:     NewVideoRepository(db),
		Categories: NewCategoryRepository(db),
		Tags:       NewTagRepository(db),
		Jobs:       NewJobRepository(db),
		Users:      NewUserRepository(db),
		db:         db,
		path:       path,
	}, nil
}

// Path 返回数据库文件路径。
func (r *Repositories) Path() string { return r.path }

// prepareDatabase 维护查询计划器依赖的统计信息。
//
//   - 首次升级到当前 schemaVersion：整库 ANALYZE 一次（大表约几百毫秒，仅发生一次）；
//   - 之后每次启动：PRAGMA optimize，由 SQLite 自行判断哪些索引需要补统计信息，代价极低。
//
// 不做这一步的话，新建的复合索引在 sqlite_stat1 里没有记录，计划器仍会按旧索引
// 估算行数，出现「有索引却仍全量排序」的情况。
func prepareDatabase(db *gorm.DB) {
	var version int
	if err := db.Raw("PRAGMA user_version").Scan(&version).Error; err != nil {
		return
	}
	if version < schemaVersion {
		if err := db.Exec("ANALYZE").Error; err != nil {
			log.Printf("重建统计信息失败（不影响数据，仅影响查询计划选择）: %v", err)
			return
		}
		db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion))
		return
	}
	db.Exec("PRAGMA optimize")
}

// Close 关闭底层连接。
func (r *Repositories) Close() error {
	sqlDB, err := r.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
