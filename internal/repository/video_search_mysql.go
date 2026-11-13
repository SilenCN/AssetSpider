package repository

// MySQL 后端的关键词全文检索（InnoDB FULLTEXT + ngram 分词器）。
//
// SQLite 用 FTS5 trigram（≥3 字）+ bigram 倒排表（2 字）；MySQL 只有一套
// FULLTEXT 索引，靠 ngram parser 把中文切成 2 字片段，因此 2 字及以上统一走
// MATCH ... AGAINST，1 字仍回退 LIKE。
//
// 索引列必须与 MATCH 的列清单**完全一致**（列数与顺序都相同），否则 MySQL 会报
// "Can't find FULLTEXT index matching the column list"；两处都由下面的常量生成。

import (
	"log"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm"
)

const (
	// mysqlFulltextIndex 是建在 videos 上的全文索引名。
	mysqlFulltextIndex = "ft_videos_search"
	// mysqlFulltextColumns 与 SQLite 的 videoSearchColumns 保持一致，
	// 保证两种后端「哪些字段参与关键词检索」完全相同。
	mysqlFulltextColumns = "vod_name, vod_en, vod_actor, vod_director, vod_class, vod_area"
	// mysqlFulltextMinRunes 是 ngram_token_size=2 下能命中的最短关键词。
	mysqlFulltextMinRunes = 2
)

// mysqlFulltextDDL 幂等性由调用方的探测保证（已存在就不再执行）。
// WITH PARSER ngram 是 MySQL 5.7+/8.0 的内置中文分词器；MariaDB 不支持，
// DDL 会失败并降级为 LIKE 检索，不影响服务启动与结果正确性。
func mysqlFulltextDDL() string {
	return "ALTER TABLE videos ADD FULLTEXT INDEX " + mysqlFulltextIndex +
		" (" + mysqlFulltextColumns + ") WITH PARSER ngram"
}

// mysqlFulltextExists 从 information_schema 判断全文索引是否已建好。
// 查询失败时返回 false（降级 LIKE），不阻断启动。
func mysqlFulltextExists(db *gorm.DB) bool {
	var n int64
	err := db.Raw(`SELECT COUNT(*) FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'videos' AND INDEX_NAME = ?`,
		mysqlFulltextIndex).Scan(&n).Error
	return err == nil && n > 0
}

// mysqlFulltextMatch 把用户输入包装成 BOOLEAN MODE 的短语表达式。
//
// 返回 false 表示该关键词不适合走全文索引（过短、含 LIKE 通配符、清理后为空），
// 调用方应回退六列 LIKE。
//
// 为什么用双引号短语：原 LIKE '%词%' 是「连续子串」语义，BOOLEAN MODE 默认的
// 多词 OR 会放宽成「任一词命中」。短语搜索保持连续匹配，ngram parser 会把短语
// 转成 bigram 序列，中文子串（如「流浪」命中「流浪地球」）照样能命中。
//
// BOOLEAN MODE 里 + - @ > < ( ) ~ * " 都是操作符，未闭合引号或孤立 @ 会直接
// 报语法错误；这里把它们替换成空格，既不会拼坏语句，也让含符号的片名仍能搜到。
func mysqlFulltextMatch(keyword string) (string, bool) {
	kw := strings.TrimSpace(keyword)
	if kw == "" || utf8.RuneCountInString(kw) < mysqlFulltextMinRunes {
		return "", false
	}
	// 含 LIKE 通配符时保持原语义：全文索引里 % 与 _ 是普通字符，直接切换属行为突变。
	if strings.ContainsAny(kw, "%_") {
		return "", false
	}
	var b strings.Builder
	pendingSpace := false
	for _, r := range kw {
		if unicode.IsSpace(r) || strings.ContainsRune(`"+-@><()~*`, r) {
			if !pendingSpace {
				b.WriteByte(' ')
				pendingSpace = true
			}
			continue
		}
		b.WriteRune(r)
		pendingSpace = false
	}
	phrase := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(phrase) < mysqlFulltextMinRunes {
		return "", false
	}
	return `"` + phrase + `"`, true
}

// maintainMySQLFulltext 在后台建立全文索引并维护就绪标记。
//
// ADD FULLTEXT INDEX 在大表上会重建整张表、持有 metadata lock，绝不能放进
// repository.OpenOptions 的同步路径（部署健康检查会超时回滚）。索引就绪前
// ready=false，关键词检索走 LIKE；建成后置 true，下一次查询立即切到 MATCH。
//
// DDL 失败（版本不支持 ngram、权限不足等）不重试，只保留每分钟一次的轻量
// 探测：如果 DBA 之后手工建好索引，无需重启即可自动启用；索引被删除也会
// 自动退回 LIKE。
func maintainMySQLFulltext(db *gorm.DB, ready *atomic.Bool, stop <-chan struct{}) {
	// 让服务先完成 HTTP 监听再动 DDL。
	select {
	case <-stop:
		return
	case <-time.After(2 * time.Second):
	}
	if !mysqlFulltextExists(db) {
		if err := db.Exec(mysqlFulltextDDL()).Error; err != nil {
			log.Printf("建立 MySQL 全文索引失败（关键词检索退回 LIKE；需 MySQL 5.7+/8.0 且支持 ngram parser）: %v", err)
		} else {
			log.Printf("MySQL 全文索引 %s 已就绪，关键词检索走 MATCH ... AGAINST", mysqlFulltextIndex)
		}
	}
	ready.Store(mysqlFulltextExists(db))
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			// 读一次 information_schema（主键点查级别），不碰主表。
			ready.Store(mysqlFulltextExists(db))
		}
	}
}
