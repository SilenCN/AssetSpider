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
	"os"
	"path/filepath"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

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

// Close 关闭底层连接。
func (r *Repositories) Close() error {
	sqlDB, err := r.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
