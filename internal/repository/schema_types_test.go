package repository

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
)

// TestVideoColumnTypesInNewDB 校验新库里 videos 表的**实际 DDL 列型**：
// 清单与 gorm tag 一致还不够，驱动可能把 size 映射成别的类型，因此直接读 SQLite 的
// PRAGMA table_info。用户明确只优化新库，不做存量数据迁移。
func TestVideoColumnTypesInNewDB(t *testing.T) {
	repos, err := Open(filepath.Join(t.TempDir(), "types.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repos.Close()

	var cols []struct {
		Name string
		Type string
	}
	if err := repos.db.Raw("PRAGMA table_info(videos)").Scan(&cols).Error; err != nil {
		t.Fatal(err)
	}
	declared := make(map[string]string, len(cols))
	for _, c := range cols {
		declared[strings.ToLower(c.Name)] = strings.ToUpper(c.Type)
	}
	if len(declared) == 0 {
		t.Fatal("PRAGMA table_info(videos) 未返回列")
	}

	check := func(column, want string) {
		t.Helper()
		got := declared[strings.ToLower(column)]
		want = strings.ToUpper(want)
		if strings.HasPrefix(want, "VARCHAR(") {
			// SQLite 不同驱动可能写作 VARCHAR(255) / varchar(255)，只看长度数字。
			wantSize := strings.TrimSuffix(strings.TrimPrefix(want, "VARCHAR("), ")")
			if !strings.HasPrefix(got, "VARCHAR(") || !strings.Contains(got, wantSize+")") {
				t.Errorf("列 %s 应为 VARCHAR(%s)，实际 %s", column, wantSize, got)
			}
			return
		}
		if !strings.Contains(got, "TEXT") {
			t.Errorf("列 %s 应为 %s，实际 %s", column, want, got)
		}
	}

	for _, f := range model.VodFields {
		check(f.Column, model.VodColumnType(f))
	}
	// 采集元数据两列不在标准字段清单里，同样不应退回无长度的通用文本。
	check("hash", "varchar(64)")
	check("extra", "mediumtext")

	// 反向断言：短字段不应出现 LONGTEXT / 无长度 TEXT（这正是本次优化要消除的情况）。
	for _, f := range model.VodFields {
		if f.Size == 0 {
			continue
		}
		got := declared[strings.ToLower(f.Column)]
		if strings.Contains(got, "TEXT") || !strings.Contains(got, "(") {
			t.Errorf("短字段 %s 不应是无长度文本类型，实际 %s", f.Column, got)
		}
	}

	// 列宽顺序抽查：确认 size 真的落到了 DDL，而不是被驱动忽略成默认宽度。
	for _, sample := range []struct {
		column string
		size   int
	}{{"vod_name", 255}, {"vod_pic", 1024}, {"vod_id", 64}} {
		got := declared[strings.ToLower(sample.column)]
		if !strings.Contains(got, "("+strconv.Itoa(sample.size)+")") {
			t.Errorf("列 %s 的列宽应为 %d，实际 %s", sample.column, sample.size, got)
		}
	}
}

// TestOtherTablesColumnTypesInNewDB 覆盖影片表之外的实体：同样用显式
// type:varchar(N)，避免 SQLite 忽略 gorm 的 size 标签后退回无长度 TEXT。
func TestOtherTablesColumnTypesInNewDB(t *testing.T) {
	repos, err := Open(filepath.Join(t.TempDir(), "types.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repos.Close()

	for _, tc := range []struct {
		table  string
		column string
		size   int
	}{
		{"users", "username", 64},
		{"sources", "api_base", 512},
		{"crawl_jobs", "status", 16},
		{"categories", "name", 128},
		{"subscriptions", "url", 1024},
	} {
		var got string
		err := repos.db.Raw("SELECT type FROM pragma_table_info(?) WHERE name = ?", tc.table, tc.column).Scan(&got).Error
		if err != nil {
			t.Fatal(err)
		}
		want := "VARCHAR(" + strconv.Itoa(tc.size) + ")"
		if !strings.Contains(strings.ToUpper(got), want) {
			t.Errorf("%s.%s 应为 %s，实际 %s", tc.table, tc.column, want, got)
		}
	}
}
