package repository

import (
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
)

// 影片关键词检索的全文索引（SQLite FTS5）。
//
// 原来的关键词查询是对六列做 `LIKE '%词%'`，前置通配符让 B-Tree 索引完全用不上，
// 每次搜索都要全表扫描一遍；数据量到十几万行时单次搜索就要 80ms 以上，而且
// COUNT 与分页查询会各扫一遍。这里用 FTS5 的 trigram 分词器建倒排索引，
// 把「任意子串匹配」变成索引查找。
//
// 为什么是 trigram：默认的 unicode61 分词器按词切分，中文整句会被当成一个词，
// 搜「流浪」永远匹配不到「流浪地球」。trigram 把文本切成三字符片段，天然支持
// 中文子串匹配，也顺带支持英文子串与大小写不敏感。
//
// 代价与边界：
//   - trigram 无法为短于 3 个字符的查询建立片段：3 字及以上走这里，2 字改走
//     video_bigrams 倒排表（见 video_bigram.go），1 字仍回退 LIKE；
//   - 索引本身占额外存储（约为被索引文本的 1/3），写入时随之维护；
//   - 用 external content 表（content='videos'）而不是复制一份正文，正文仍只有
//     videos 一份，索引里只存倒排表，靠触发器保持同步。

const (
	// videoSearchTable 是 FTS5 虚拟表名。
	videoSearchTable = "videos_fts"
	// videoSearchMinRunes 是 trigram 分词器能命中所需的最短关键词长度（符文数）。
	videoSearchMinRunes = 3
)

// videoSearchColumns 是参与全文检索的列，与原 LIKE 查询覆盖的列保持一致。
var videoSearchColumns = []string{"vod_name", "vod_en", "vod_actor", "vod_director", "vod_class", "vod_area"}

// ensureVideoSearch 建立 FTS5 虚拟表与同步触发器；虚拟表是新建的会顺带重建一次索引。
//
// 函数幂等：已存在时只补/更新触发器定义，不会重复建表或重建索引。
func ensureVideoSearch(db *gorm.DB) error {
	existed := videoSearchTableExists(db)
	if !existed {
		ddl := "CREATE VIRTUAL TABLE " + videoSearchTable + " USING fts5(" +
			strings.Join(videoSearchColumns, ", ") +
			", content='videos', content_rowid='id', tokenize='trigram')"
		if err := db.Exec(ddl).Error; err != nil {
			return err
		}
	}
	for _, stmt := range videoSearchTriggerSQL() {
		if err := db.Exec(stmt).Error; err != nil {
			return err
		}
	}
	if !existed {
		// 首次建表：把 videos 里已有的行灌进索引（外部内容表的标准重建命令）。
		return db.Exec("INSERT INTO " + videoSearchTable + "(" + videoSearchTable + ") VALUES('rebuild')").Error
	}
	return nil
}

// videoSearchTableExists 判断全文索引表是否已建好。
func videoSearchTableExists(db *gorm.DB) bool {
	var n int64
	if err := db.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", videoSearchTable).
		Scan(&n).Error; err != nil {
		return false
	}
	return n > 0
}

// videoSearchTriggerSQL 返回保持索引与 videos 表同步的触发器。
//
// FTS5 的外部内容模式要求「插入、更新、删除」三条触发器配套：更新与删除必须先用
// 索引里的原始值执行 'delete' 命令，再写入新值，否则倒排表会残留旧词。
// 列值统一用 IFNULL 兜底成空串——历史数据里可能存在 NULL，而 'delete' 命令要求
// 传入的值与建索引时的值完全一致。
func videoSearchTriggerSQL() []string {
	cols := strings.Join(videoSearchColumns, ", ")
	newVals := make([]string, len(videoSearchColumns))
	oldVals := make([]string, len(videoSearchColumns))
	for i, c := range videoSearchColumns {
		newVals[i] = "IFNULL(new." + c + ", '')"
		oldVals[i] = "IFNULL(old." + c + ", '')"
	}
	return []string{
		`CREATE TRIGGER IF NOT EXISTS ` + videoSearchTable + `_ai AFTER INSERT ON videos BEGIN
			INSERT INTO ` + videoSearchTable + `(rowid, ` + cols + `) VALUES (new.id, ` + strings.Join(newVals, ", ") + `);
		END`,
		`CREATE TRIGGER IF NOT EXISTS ` + videoSearchTable + `_ad AFTER DELETE ON videos BEGIN
			INSERT INTO ` + videoSearchTable + `(` + videoSearchTable + `, rowid, ` + cols + `)
			VALUES ('delete', old.id, ` + strings.Join(oldVals, ", ") + `);
		END`,
		`CREATE TRIGGER IF NOT EXISTS ` + videoSearchTable + `_au AFTER UPDATE ON videos BEGIN
			INSERT INTO ` + videoSearchTable + `(` + videoSearchTable + `, rowid, ` + cols + `)
			VALUES ('delete', old.id, ` + strings.Join(oldVals, ", ") + `);
			INSERT INTO ` + videoSearchTable + `(rowid, ` + cols + `) VALUES (new.id, ` + strings.Join(newVals, ", ") + `);
		END`,
	}
}

// videoSearchMatch 把用户输入包装成 FTS5 的短语查询表达式。
//
// 返回 false 表示关键词不足 3 个符文，trigram 索引无法命中，调用方应改走
// bigram 倒排表或回退 LIKE。
// 关键词整体放进双引号，既避免 MATCH 的语法字符（AND / OR / - / *）被当成操作符，
// 也保证多词输入按「连续子串」匹配，与原 LIKE 的语义一致。
func videoSearchMatch(keyword string) (string, bool) {
	kw := strings.TrimSpace(keyword)
	if kw == "" || utf8.RuneCountInString(kw) < videoSearchMinRunes {
		return "", false
	}
	return `"` + strings.ReplaceAll(kw, `"`, `""`) + `"`, true
}

// videoSearchCaps 是本次查询可用的检索索引。
type videoSearchCaps struct {
	fts    bool // videos_fts（trigram，≥3 字）是否可用
	bigram bool // video_bigrams（2 字）是否回灌完毕
}

// applyVideoKeyword 给影片查询附加关键词条件，按关键词长度选索引：
//
//	≥3 字  → videos_fts 的 trigram 短语查询
//	==2 字 → video_bigrams 的主键等值查找（gram = LOWER(?)）
//	1 字 / 索引未就绪 → 六列 LIKE 回退，结果语义与上面两条完全一致
//
// LIKE 回退优先在 FTS5 外部内容表上做：它只读取六列文本，比扫 videos 的九十多列
// 快一个数量级；连 FTS 都不可用时才回主表。
func applyVideoKeyword(q *gorm.DB, keyword string, caps videoSearchCaps) *gorm.DB {
	kw := strings.TrimSpace(keyword)
	if caps.fts {
		if expr, ok := videoSearchMatch(kw); ok {
			return q.Where("videos.id IN (SELECT rowid FROM "+videoSearchTable+" WHERE "+videoSearchTable+" MATCH ?)", expr)
		}
	}
	// 含 LIKE 通配符的两字输入仍走原来的 LIKE 语义：倒排表是精确等值匹配，
	// 直接切换会把「%」从通配符变成普通字符，属于行为突变。
	if caps.bigram && utf8.RuneCountInString(kw) == 2 && !strings.ContainsAny(kw, "%_") {
		return q.Where("videos.id IN (SELECT video_id FROM "+videoBigramTable+" WHERE gram = LOWER(?))", kw)
	}
	like := "%" + kw + "%"
	cond := "vod_name LIKE ? OR vod_en LIKE ? OR vod_actor LIKE ? OR vod_director LIKE ? OR vod_class LIKE ? OR vod_area LIKE ?"
	args := []interface{}{like, like, like, like, like, like}
	if caps.fts {
		return q.Where("videos.id IN (SELECT rowid FROM "+videoSearchTable+" WHERE "+cond+")", args...)
	}
	return q.Where(cond, args...)
}
