package repository

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestOpenLegacyVideosTableDropsTrimmedColumns 验证老库升级的另一种列形态：
// 一个含全部 23 个已裁剪列（NOT NULL DEFAULT ”，常见于老版本 GORM 建表）的
// videos 表，用当前实体 Open 后：
//  1. 启动 / AutoMigrate / 删列迁移全程不报错；
//  2. 这 23 列被物理删除（与 trim_test 的「可空列形态」互补）；
//  3. 用裁剪后的实体 Upsert（新增 + 更新）/ 查询完全正常。
//
// 清单直接复用 trimmedVideoColumns，避免与迁移实现各留一份而漂移。
func TestOpenLegacyVideosTableDropsTrimmedColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	// ① 先用当前实体建表（不含 23 列），再手工补回这些列并灌入历史值，
	// 模拟「升级前就存在的老库」。
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Video{}); err != nil {
		t.Fatal(err)
	}
	for _, col := range trimmedVideoColumns {
		if err := db.Exec(fmt.Sprintf(`ALTER TABLE videos ADD COLUMN "%s" TEXT NOT NULL DEFAULT ''`, col)).Error; err != nil {
			t.Fatalf("补建旧列 %s: %v", col, err)
		}
	}
	if err := db.Exec(`INSERT INTO videos (source_id, vod_id, vod_name, hash, vod_level, vod_pwd, vod_status)
		VALUES (1, 'old-1', '老库影片', 'h1', '9', 'secret', '1')`).Error; err != nil {
		t.Fatal(err)
	}
	if sqlDB, _ := db.DB(); sqlDB != nil {
		sqlDB.Close()
	}

	// ② 用当前实体重新打开：AutoMigrate 容忍多余列，随后 prepareDatabase 幂等删列。
	r, err := Open(path)
	if err != nil {
		t.Fatalf("老库用裁剪后实体打开失败: %v", err)
	}
	defer r.Close()

	// ③ 23 列全部被物理删除。
	for _, col := range trimmedVideoColumns {
		if hasColumn(t, r, col) {
			t.Errorf("旧列 %s 未被删除", col)
		}
	}
	// 保留列与其余行的历史数据不受影响。
	var name string
	if err := r.db.Raw(`SELECT vod_name FROM videos WHERE vod_id='old-1'`).Row().Scan(&name); err != nil {
		t.Fatalf("删列后读取保留列失败: %v", err)
	}
	if name != "老库影片" {
		t.Errorf("保留列数据被破坏: name=%q", name)
	}

	// ④ 裁剪后的实体照常 Upsert（新增 + 更新）与查询。
	st, err := r.Videos.UpsertVideos([]model.Video{
		{SourceID: 1, VodID: "old-1", VodName: "改名", Hash: "h2", VodTime: "2026-11-11"},
		{SourceID: 1, VodID: "new-1", VodName: "新片", Hash: "h3", VodTime: "2026-11-11"},
	})
	if err != nil {
		t.Fatalf("老库上 Upsert 失败: %v", err)
	}
	if st.Updated != 1 || st.New != 1 {
		t.Errorf("老库上 Upsert 统计异常: %+v", st)
	}
	var got model.Video
	if err := r.db.Where("source_id = 1 AND vod_id = 'old-1'").First(&got).Error; err != nil {
		t.Fatalf("老库上查询异常: %v", err)
	}
	if got.VodName != "改名" || got.Hash != "h2" {
		t.Fatalf("老库上更新未生效: %+v", got)
	}
	if hasColumn(t, r, "vod_level") {
		t.Error("Upsert 后旧列不应重新出现")
	}
}
