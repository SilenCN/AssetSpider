// Package store 封装基于 GORM 的 SQLite 数据访问。
package store

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/SilenCN/AssetSpider/internal/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Store 持有一个 GORM 数据库连接。
type Store struct {
	db   *gorm.DB
	path string
}

// Open 打开（必要时创建）path 指定的 SQLite 数据库，并自动迁移表结构。
func Open(path string) (*Store, error) {
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
		&model.CrawlJob{},
	); err != nil {
		return nil, fmt.Errorf("迁移表结构: %w", err)
	}
	return &Store{db: db, path: path}, nil
}

// Path 返回数据库文件路径。
func (s *Store) Path() string { return s.path }

// Close 关闭底层连接。
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// List 按 ID 倒序返回全部资产。
func (s *Store) List() ([]model.Asset, error) {
	var items []model.Asset
	if err := s.db.Order("id DESC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// Create 新增一条资产并回填主键。
func (s *Store) Create(a *model.Asset) error {
	return s.db.Create(a).Error
}

// Delete 按 ID 删除资产。
func (s *Store) Delete(id uint) error {
	return s.db.Delete(&model.Asset{}, id).Error
}
