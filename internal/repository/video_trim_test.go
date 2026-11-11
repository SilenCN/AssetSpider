package repository

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
)

// addTrimmedColumns 模拟「上一轮裁剪实体之前」的老库：把 23 列加回 videos 表，
// 给其中几列灌值，再造一条索引验证 DROP COLUMN 不会连带删掉它。
// 用原生 SQL 而非实体，因为裁剪后 model.Video 已无这些字段。
func addTrimmedColumns(t *testing.T, r *Repositories) {
	t.Helper()
	for _, col := range trimmedVideoColumns {
		if err := r.db.Exec(fmt.Sprintf(`ALTER TABLE videos ADD COLUMN "%s" TEXT`, col)).Error; err != nil {
			t.Fatalf("加回历史列 %s: %v", col, err)
		}
	}
	// 给保留列 + 若干待删列写入一条可核对的记录。
	if err := r.db.Exec(`INSERT INTO videos (source_id, vod_id, vod_name, vod_en, hash, vod_level, vod_pwd, vod_status)
		VALUES (1, 'legacy-1', '老库影片流浪地球', 'legacy-en', 'h-legacy', 'VIP', 'secret', '1')`).Error; err != nil {
		t.Fatalf("写入老库记录: %v", err)
	}
	if err := r.db.Exec(`CREATE INDEX idx_legacy_level ON videos(vod_level)`).Error; err != nil {
		t.Fatalf("建立引用待删列的索引: %v", err)
	}
}

func hasColumn(t *testing.T, r *Repositories, col string) bool {
	t.Helper()
	var n int64
	if err := r.db.Raw(`SELECT count(*) FROM pragma_table_info('videos') WHERE name = ?`, col).Scan(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n > 0
}

// TestDropTrimmedColumnsOnUpgrade 是核心回归：老库（含 23 列 + 引用待删列的索引
// + FTS 触发器）在 Open 升级后，23 列被物理删除，其余列数据、FTS 全文索引、
// 与待删列无关的索引都完好；user_version 升到 schemaVersion；幂等重开不报错。
func TestDropTrimmedColumnsOnUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	addTrimmedColumns(t, r)

	// 写一条能被 trigram 命中的全文索引，并确认删列前可检索。
	var hitBefore int64
	if err := r.db.Raw(`SELECT count(*) FROM videos_fts WHERE videos_fts MATCH '流浪地球'`).Scan(&hitBefore).Error; err != nil {
		t.Fatalf("删列前 FTS 查询: %v", err)
	}
	if hitBefore != 1 {
		t.Fatalf("删列前 FTS 应命中 1 条，实际 %d", hitBefore)
	}
	// 伪装成升级前的库，重开触发 v6 → v7 迁移。
	if err := r.db.Exec("PRAGMA user_version = 6").Error; err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()

	// 23 列必须全部消失。
	for _, col := range trimmedVideoColumns {
		if hasColumn(t, again, col) {
			t.Errorf("历史列 %s 未被删除", col)
		}
	}
	// 保留列与数据必须完好（尤其别名列 vod_en，曾与待删列 vod_letter 相邻）。
	var name, en, hash string
	if err := again.db.Raw(`SELECT vod_name, vod_en, hash FROM videos WHERE vod_id='legacy-1'`).
		Row().Scan(&name, &en, &hash); err != nil {
		t.Fatalf("删列后读取保留列失败: %v", err)
	}
	if name != "老库影片流浪地球" || en != "legacy-en" || hash != "h-legacy" {
		t.Errorf("保留列数据被破坏: name=%q en=%q hash=%q", name, en, hash)
	}
	// 引用待删列的索引应随列一起被 SQLite 清掉；其余索引必须保留。
	var legacyIdx int64
	again.db.Raw(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_legacy_level'`).Scan(&legacyIdx)
	if legacyIdx != 0 {
		t.Error("引用已删列的 idx_legacy_level 索引未清理")
	}
	var srcVodIdx int64
	again.db.Raw(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_source_vod'`).Scan(&srcVodIdx)
	if srcVodIdx != 1 {
		t.Error("核心唯一索引 idx_source_vod 被误删")
	}
	// FTS 触发器保留、全文索引仍可命中（DROP COLUMN 与 VACUUM 都不能破坏它）。
	var hitAfter int64
	if err := again.db.Raw(`SELECT count(*) FROM videos_fts WHERE videos_fts MATCH '流浪地球'`).Scan(&hitAfter).Error; err != nil {
		t.Fatalf("删列后 FTS 查询失败: %v", err)
	}
	if hitAfter != 1 {
		t.Errorf("删列后 FTS 应仍命中 1 条，实际 %d", hitAfter)
	}
	// 触发器存活：再插一条应能被索引。
	if err := again.db.Exec(`INSERT INTO videos (source_id, vod_id, vod_name, hash) VALUES (1, 'legacy-2', '新片星际穿越', 'h2')`).Error; err != nil {
		t.Fatalf("删列后插入: %v", err)
	}
	var hitNew int64
	again.db.Raw(`SELECT count(*) FROM videos_fts WHERE videos_fts MATCH '星际穿越'`).Scan(&hitNew)
	if hitNew != 1 {
		t.Errorf("删列后新记录未被 FTS 索引（触发器可能失效）: %d", hitNew)
	}
	// user_version 必须升到当前版本。
	var version int
	if err := again.db.Raw("PRAGMA user_version").Scan(&version).Error; err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Errorf("user_version 应升到 %d，实际 %d", schemaVersion, version)
	}
	// 库仍完整。
	var integrity string
	again.db.Raw("PRAGMA integrity_check").Scan(&integrity)
	if integrity != "ok" {
		t.Errorf("迁移后 integrity_check = %q", integrity)
	}
}

// TestDropTrimmedColumnsIdempotent 验证幂等：对已经是目标结构的库重复调用，
// 既不报错也不做多余 VACUUM，且实体读写正常。
func TestDropTrimmedColumnsIdempotent(t *testing.T) {
	r := newTestRepos(t) // 全新库，本就没有 23 列
	for _, col := range trimmedVideoColumns {
		if hasColumn(t, r, col) {
			t.Fatalf("全新库不应含裁剪列 %s", col)
		}
	}
	// 连续调用两次都应是无操作。
	dropTrimmedVideoColumns(r.db)
	dropTrimmedVideoColumns(r.db)
	if _, err := r.Videos.UpsertVideos([]model.Video{{SourceID: 1, VodID: "x", VodName: "幂等测试", Hash: "h"}}); err != nil {
		t.Fatalf("幂等清理后写入失败: %v", err)
	}
}

// TestTrimmedColumnsAreNotInEntity 守住清单与实体的一致性：trimmedVideoColumns
// 里的列名都不能出现在当前 model.Video 的 gorm 列定义中，防止清单与实体漂移
// （若某列又加回实体，却仍在删除清单里，启动时会把新字段删掉）。
func TestTrimmedColumnsAreNotInEntity(t *testing.T) {
	// 按裁剪后实体建立的新库，表里不应再出现任何裁剪列。
	var cols []string
	if err := newTestRepos(t).db.Raw(`SELECT name FROM pragma_table_info('videos')`).Scan(&cols).Error; err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, c := range cols {
		have[c] = true
	}
	for _, col := range trimmedVideoColumns {
		if have[col] {
			t.Errorf("裁剪列 %s 仍存在于按 model.Video 建立的新库表中（清单与实体漂移）", col)
		}
	}
}

// TestTrimShouldSkip 锁定删列的空间预检阈值：可用空间不足 2 倍库大小时跳过，
// 避免大库重写把磁盘写满反过来拖垮服务。
func TestTrimShouldSkip(t *testing.T) {
	const mb = int64(1024 * 1024)
	cases := []struct {
		name         string
		free, dbSize int64
		wantSkip     bool
	}{
		{"磁盘宽裕-大库", 10 * 1024 * mb, 2 * 1024 * mb, false},
		{"恰好够2倍", 4 * 1024 * mb, 2 * 1024 * mb, false},
		{"不足2倍", 3 * 1024 * mb, 2 * 1024 * mb, true},
		{"严重不足", 130 * mb, 2119 * mb, true}, // 真实事故场景
		{"小库-空间小也放行", 200 * mb, 100 * 1024, false},
		{"无效库大小", 0, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := trimShouldSkip(c.free, c.dbSize); got != c.wantSkip {
				t.Errorf("trimShouldSkip(%d, %d) = %v, want %v", c.free, c.dbSize, got, c.wantSkip)
			}
		})
	}
}
