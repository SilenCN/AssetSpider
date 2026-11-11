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

// trimmedColumns 是 2026-11 从 Video 实体裁剪掉的 23 个后台管理字段。
// 它们不再参与采集映射 / 入库 / 展示，但历史库里这些列及其数据应当原样保留，
// GORM 只忽略它们，不删除、不清空。
var trimmedColumns = []string{
	"vod_status", "vod_audit_remark", "vod_letter", "vod_tpl", "vod_lock",
	"vod_level", "vod_copyright", "vod_points", "vod_points_play", "vod_points_down",
	"vod_share_count", "vod_time_hits", "vod_recycle_time", "vod_trysee", "vod_reurl",
	"vod_rel_vod", "vod_rel_art", "vod_pwd", "vod_pwd_url", "vod_pwd_play",
	"vod_pwd_play_url", "vod_pwd_down", "vod_pwd_down_url",
}

// TestOpenLegacyVideosTableKeepsTrimmedColumns 验证「老库兼容」：
// 一个含全部 23 个已裁剪列（且带历史数据）的 videos 表，用当前实体 Open 后：
//  1. 启动 / AutoMigrate 不报错；
//  2. 这 23 列仍存在、历史数据原样保留（不 DROP、不清空）；
//  3. 用裁剪后的实体 Upsert / 查询正常，新字段读写不受多余列影响。
func TestOpenLegacyVideosTableKeepsTrimmedColumns(t *testing.T) {
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
	for _, col := range trimmedColumns {
		if err := db.Exec(fmt.Sprintf("ALTER TABLE videos ADD COLUMN %s TEXT NOT NULL DEFAULT ''", col)).Error; err != nil {
			t.Fatalf("补建旧列 %s: %v", col, err)
		}
	}
	// 插一条带旧列历史值的影片。
	if err := db.Exec(`INSERT INTO videos (source_id, vod_id, vod_name, hash, vod_level, vod_pwd, vod_status)
		VALUES (1, 'old-1', '老库影片', 'h1', '9', 'secret', '1')`).Error; err != nil {
		t.Fatal(err)
	}
	if sqlDB, _ := db.DB(); sqlDB != nil {
		sqlDB.Close()
	}

	// ② 用当前（已裁剪）实体重新打开：AutoMigrate 必须容忍多余列。
	r, err := Open(path)
	if err != nil {
		t.Fatalf("老库用裁剪后实体打开失败: %v", err)
	}
	defer r.Close()

	// ③ 23 列仍在，历史值未被清空。
	for _, col := range trimmedColumns {
		var n int64
		if err := r.db.Raw("SELECT count(*) FROM pragma_table_info('videos') WHERE name = ?", col).Scan(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("旧列 %s 被删除了（应保留）", col)
		}
	}
	var level, pwd string
	row := r.db.Raw("SELECT vod_level, vod_pwd FROM videos WHERE vod_id='old-1'").Row()
	if err := row.Scan(&level, &pwd); err != nil {
		t.Fatalf("读取旧列历史值失败: %v", err)
	}
	if level != "9" || pwd != "secret" {
		t.Errorf("旧列历史值被破坏: level=%q pwd=%q", level, pwd)
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
	// 更新后旧列历史值仍应保留（Upsert 不再触碰它们）。
	row = r.db.Raw("SELECT vod_level, vod_pwd FROM videos WHERE vod_id='old-1'").Row()
	if err := row.Scan(&level, &pwd); err != nil {
		t.Fatal(err)
	}
	if level != "9" || pwd != "secret" {
		t.Errorf("Upsert 破坏了旧列历史值: level=%q pwd=%q", level, pwd)
	}
}
