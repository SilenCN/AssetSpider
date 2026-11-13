package repository

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// dryRunSQLite 构造一个不会真正读盘/连库的 SQLite GORM 连接，用来校验
// 原 SQLite 检索路径（FTS5 / bigram）在引入 MySQL 分支后不受影响。
func dryRunSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		DryRun: true,
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("构造 SQLite DryRun 连接失败: %v", err)
	}
	return db
}

// dryRunMySQL 构造一个不会真正连库的 MySQL GORM 连接，用来校验方言相关的
// SQL 生成。SkipInitializeWithVersion 跳过 SELECT VERSION()，
// DisableAutomaticPing 跳过 Ping，二者都不需要真实服务器。
func dryRunMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN:                       "asset:pwd@tcp(127.0.0.1:3306)/assetspider?charset=utf8mb4&parseTime=True&loc=Local",
		SkipInitializeWithVersion: true,
	}), &gorm.Config{
		DryRun:               true,
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("构造 MySQL DryRun 连接失败: %v", err)
	}
	return db
}

// keywordSQL 生成一次带关键词条件的列表查询 SQL。
func keywordSQL(t *testing.T, db *gorm.DB, kw string, caps videoSearchCaps) string {
	t.Helper()
	var cards []model.VideoCard
	return db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		return applyVideoKeyword(tx.Model(&model.Video{}), kw, caps).
			Select(videoCardColumns).Order("id DESC").Limit(20).Find(&cards)
	})
}

func TestMySQLFulltextMatchPhrase(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"流浪地球", `"流浪地球"`, true},
		{"流浪", `"流浪"`, true},
		{"复仇者 联盟", `"复仇者 联盟"`, true},
		{`复仇者"终局"`, `"复仇者 终局"`, true}, // BOOLEAN MODE 的引号被替换成空格
		{"流浪+地球", `"流浪 地球"`, true},    // + 是操作符，替换成空格
		{"AND", `"AND"`, true},        // 引号内 AND 是普通词，不再是操作符
		{"A", "", false},              // 1 字：ngram_token_size=2 无法命中
		{"", "", false},
		{"   ", "", false},
		{"100%", "", false}, // 含 LIKE 通配符，保持原语义
		{"a_b", "", false},
		{"()", "", false}, // 清理后为空
		{"@", "", false},
	}
	for _, c := range cases {
		got, ok := mysqlFulltextMatch(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("mysqlFulltextMatch(%q) = (%q, %v)，期望 (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestMySQLFulltextDDLUsesNgram(t *testing.T) {
	ddl := mysqlFulltextDDL()
	for _, want := range []string{
		"ALTER TABLE videos ADD FULLTEXT INDEX", mysqlFulltextIndex,
		"WITH PARSER ngram", "vod_name", "vod_en", "vod_actor",
		"vod_director", "vod_class", "vod_area",
	} {
		if !strings.Contains(ddl, want) {
			t.Errorf("DDL 缺少 %q：%s", want, ddl)
		}
	}
	if mysqlFulltextColumns != strings.Join(videoSearchColumns, ", ") {
		t.Fatal("MySQL 全文索引列必须与 SQLite FTS5 检索列完全一致")
	}
}

func TestMySQLKeywordQueryUsesMatchAgainst(t *testing.T) {
	db := dryRunMySQL(t)
	sql := keywordSQL(t, db, "流浪地球", videoSearchCaps{mysqlFulltext: true})
	for _, want := range []string{"MATCH(" + mysqlFulltextColumns + ")", "AGAINST", "IN BOOLEAN MODE"} {
		if !strings.Contains(sql, want) {
			t.Errorf("全文检索 SQL 缺少 %q：%s", want, sql)
		}
	}
	if strings.Contains(sql, "LIKE") {
		t.Errorf("走全文索引时不应再出现 LIKE：%s", sql)
	}
}

func TestMySQLKeywordFallsBackToLike(t *testing.T) {
	db := dryRunMySQL(t)
	cases := []struct {
		name string
		kw   string
		caps videoSearchCaps
	}{
		{"索引未就绪", "流浪地球", videoSearchCaps{mysqlFulltext: false}},
		{"1 字关键词", "流", videoSearchCaps{mysqlFulltext: true}},
		{"含 LIKE 通配符", "流浪%", videoSearchCaps{mysqlFulltext: true}},
		{"纯操作符", "()", videoSearchCaps{mysqlFulltext: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sql := keywordSQL(t, db, c.kw, c.caps)
			if !strings.Contains(sql, "LIKE") {
				t.Errorf("应回退六列 LIKE：%s", sql)
			}
			if strings.Contains(sql, "MATCH(") {
				t.Errorf("降级时不应出现 MATCH：%s", sql)
			}
		})
	}
}

// SQLite 路径不能被 MySQL 分支影响：caps.fts / caps.bigram 仍走原来的索引。
func TestSQLiteKeywordPathsUnchanged(t *testing.T) {
	db := dryRunSQLite(t)
	fts := keywordSQL(t, db, "流浪地球", videoSearchCaps{fts: true})
	if !strings.Contains(fts, "videos_fts MATCH") {
		t.Errorf("SQLite 3 字以上应走 FTS5：%s", fts)
	}
	bigram := keywordSQL(t, db, "流浪", videoSearchCaps{bigram: true})
	if !strings.Contains(bigram, "video_bigrams") {
		t.Errorf("SQLite 2 字应走 bigram 倒排：%s", bigram)
	}
}

func TestNewVideoRepositoryDetectsDialect(t *testing.T) {
	sqliteDB := dryRunSQLite(t)
	if got := dialectOf(sqliteDB); got != DialectSQLite {
		t.Errorf("SQLite 连接被识别成 %q", got)
	}
	mysqlDB := dryRunMySQL(t)
	if got := dialectOf(mysqlDB); got != DialectMySQL {
		t.Errorf("MySQL 连接被识别成 %q", got)
	}
	ready := &atomic.Bool{}
	ready.Store(true)
	r := newVideoRepository(mysqlDB, DialectMySQL, ready)
	if r.bigram() {
		t.Error("MySQL 后端不应启用 SQLite 的 video_bigrams 表")
	}
}

// GORM 的 clause.OnConflict 在 MySQL 下必须翻译成 ON DUPLICATE KEY UPDATE，
// 否则采集批量 upsert 会直接失败。
func TestMySQLOnConflictGeneratesUpsert(t *testing.T) {
	db := dryRunMySQL(t)
	rows := []model.Video{{SourceID: 1, VodID: "v1", VodName: "测试"}}
	sql := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "source_id"}, {Name: "vod_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"vod_name", "last_seen_at"}),
		}).Create(&rows)
	})
	if !strings.Contains(sql, "ON DUPLICATE KEY UPDATE") {
		t.Errorf("MySQL upsert 未生成 ON DUPLICATE KEY UPDATE：%s", sql)
	}
	if strings.Contains(sql, "ON CONFLICT") {
		t.Errorf("MySQL 不支持 ON CONFLICT 语法：%s", sql)
	}
}

func TestNormalizeMySQLDSN(t *testing.T) {
	got, err := normalizeMySQLDSN("root:pwd@tcp(127.0.0.1:3306)/assetspider")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"parseTime=true", "charset=utf8mb4", "loc=Local"} {
		if !strings.Contains(got, want) {
			t.Errorf("规范化后的 DSN 缺少 %q：%s", want, got)
		}
	}
	// 用户显式指定的参数不被覆盖。注意 loc=UTC 会被 FormatDSN 当默认值省略，
	// 故用非默认时区 Asia/Shanghai 验证显式值被保留。
	explicit, err := normalizeMySQLDSN("root:pwd@tcp(h:3306)/db?charset=latin1&loc=Asia%2FShanghai&parseTime=true")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explicit, "charset=latin1") || !strings.Contains(explicit, "loc=Asia%2FShanghai") {
		t.Errorf("用户显式指定的 DSN 参数被覆盖：%s", explicit)
	}
	if _, err := normalizeMySQLDSN("not-a-dsn"); err == nil {
		t.Fatal("非法 DSN 应返回错误")
	}
}
