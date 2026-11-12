package repository

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 两字关键词的 bigram 倒排索引。
//
// FTS5 的 trigram 分词器把文本切成三字符片段，物理上无法为短于 3 个字符的
// 查询建立片段（实测 MATCH '"测试"' 恒返回 0 行），所以 3 字及以上走 videos_fts，
// 2 字关键词改走本文件的 video_bigrams：把六个检索列各自切成连续的两字符片段，
// 以 (gram, video_id) 为主键存倒排表，查询时 gram = LOWER(?) 直接命中主键。
//
// 为什么不用「把阈值改成 2」：那样两字搜索会从「全表 LIKE」变成「一个结果都搜不到」，
// 是静默的功能回归。为什么不直接用 trigram 表存 bigram：外部内容表的 delete 命令
// 要求传入建索引时的原值，改内容就得连回灌/删除语义一起重写，风险高于收益。
//
// 与 FTS5 表共用同样的六列（videoSearchColumns），保证两种长度下「哪些字段参与
// 关键词检索」完全一致。每列独立切片、列间不拼接，因此跨列边界不会产生假的
// bigram，查询结果与原来的六列 LIKE 严格一致，不需要回主表二次过滤。
// 片段必须 DISTINCT：SQLite 的 INSERT OR IGNORE 在部分版本里对「同一条 INSERT
// 语句内重复的主键」仍会报约束失败，不能依赖它去重。
//
// 大小写：写入时统一 lower()（SQLite 只折叠 ASCII），查询用 LOWER(?)，与 SQLite
// LIKE 默认「ASCII 不区分大小写」的语义一致。
//
// 同步靠 videos 上的三条触发器，不依赖 Go 侧写入路径，任何来源的新增/改名/删除
// 都不会漏。UPDATE 触发器带 AFTER UPDATE OF <六列>，只刷 last_seen_at、category_id
// 这类不涉及检索文本的更新不会重算 bigram。
//
// 历史库回灌：启动路径里只做 DDL 与触发器（毫秒级），历史数据由启动后的后台任务
// 按主键分批（每批 videoBigramBatch 行）INSERT OR IGNORE 补齐，**绝不阻塞 HTTP 监听**。
// 大库回灌可能几分钟，同步做会让部署健康检查超时并回滚，回滚又 kill 掉回灌，
// 形成「每次部署都失败、回灌永远做不完」的死循环。每批提交后把进度写进
// video_bigram_state，被打断（部署 kill、磁盘紧张、主动停止）可续跑；完成前
// 2 字检索降级走 LIKE，结果始终正确。回灌只插入新表，不改写 videos 主表，
// 也不触发 PRAGMA user_version 升级（避免把 v6 的整表回灌 + ANALYZE 再拉起来）。

const (
	videoBigramTable      = "video_bigrams"
	videoBigramStateTable = "video_bigram_state"
)

// videoBigramBatch 是每批回灌的行数；声明为变量便于测试用小批次验证续跑。
var videoBigramBatch = 5000

// videoBigramColumns 与 FTS5 的检索列保持一致。
var videoBigramColumns = videoSearchColumns

// videoBigramDDL 是幂等的表 / 索引 / 状态表定义。
// WITHOUT ROWID 让主键 (gram, video_id) 直接成为数据组织方式，省一份 rowid；
// 额外的 (video_id) 索引供删除触发器按影片反查，否则删一条影片要扫整张倒排表。
func videoBigramDDL() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS ` + videoBigramTable + ` (
			gram TEXT NOT NULL,
			video_id INTEGER NOT NULL,
			PRIMARY KEY (gram, video_id)
		) WITHOUT ROWID`,
		`CREATE INDEX IF NOT EXISTS idx_video_bigrams_video ON ` + videoBigramTable + `(video_id)`,
		`CREATE TABLE IF NOT EXISTS ` + videoBigramStateTable + ` (k TEXT PRIMARY KEY, v INTEGER NOT NULL)`,
	}
}

// videoBigramValues 生成 VALUES 子句里的六列表达式（lower + IFNULL 兜底 NULL）。
// prefix 是触发器里的 new / old 行别名。
func videoBigramValues(prefix string) string {
	vals := make([]string, len(videoBigramColumns))
	for i, c := range videoBigramColumns {
		vals[i] = "lower(ifnull(" + prefix + "." + c + ", ''))"
	}
	return strings.Join(vals, "), (")
}

// videoBigramInsertSQL 把一条影片的六列各自切成 bigram 写入倒排表。
//
// 递归 CTE 的 seq 对每列文本从位置 1 走到 length(t)，substr(t, i, 2) 取两个
// **字符**（SQLite 的 length/substr 对 TEXT 按字符而非字节计，中文与 emoji 都正确）；
// WHERE length(...) = 2 丢掉末尾只有一个字符的片段。六列彼此独立，不会产生跨列
// 的假片段；DISTINCT 去掉同一行多列里重复的片段（不能依赖 INSERT OR IGNORE）。
func videoBigramInsertSQL() string {
	return `INSERT OR IGNORE INTO ` + videoBigramTable + `(gram, video_id)
		WITH RECURSIVE cols(t) AS (
			VALUES (` + videoBigramValues("new") + `)
		), seq(t, i) AS (
			SELECT t, 1 FROM cols
			UNION ALL
			SELECT t, i + 1 FROM seq WHERE i < length(t)
		)
		SELECT DISTINCT substr(t, i, 2), new.id FROM seq WHERE length(substr(t, i, 2)) = 2;`
}

// videoBigramTriggerSQL 返回保持倒排表与 videos 同步的触发器。
func videoBigramTriggerSQL() []string {
	insert := videoBigramInsertSQL()
	return []string{
		`CREATE TRIGGER IF NOT EXISTS ` + videoBigramTable + `_ai AFTER INSERT ON videos BEGIN
			` + insert + `
		END`,
		`CREATE TRIGGER IF NOT EXISTS ` + videoBigramTable + `_ad AFTER DELETE ON videos BEGIN
			DELETE FROM ` + videoBigramTable + ` WHERE video_id = old.id;
		END`,
		`CREATE TRIGGER IF NOT EXISTS ` + videoBigramTable + `_au AFTER UPDATE OF ` +
			strings.Join(videoBigramColumns, ", ") + ` ON videos BEGIN
			DELETE FROM ` + videoBigramTable + ` WHERE video_id = old.id;
			` + insert + `
		END`,
	}
}

// videoBigramBackfillSQL 生成一段 id 区间的回灌语句。
// 六个列各扫一次该区间，UNION ALL 成 (vid, t) 后与触发器用同一套递归切片逻辑。
// lo/hi 来自库内查询出的整数主键，直接内联进 SQL，不接收外部输入。
func videoBigramBackfillSQL(lo, hi int64) string {
	parts := make([]string, len(videoBigramColumns))
	for i, c := range videoBigramColumns {
		parts[i] = fmt.Sprintf("SELECT id, lower(ifnull(%s, '')) FROM videos WHERE id BETWEEN %d AND %d", c, lo, hi)
	}
	return `INSERT OR IGNORE INTO ` + videoBigramTable + `(gram, video_id)
		WITH RECURSIVE cols(vid, t) AS (` + strings.Join(parts, " UNION ALL ") + `),
		b(vid, t, i) AS (
			SELECT vid, t, 1 FROM cols
			UNION ALL
			SELECT vid, t, i + 1 FROM b WHERE i < length(t)
		)
		SELECT DISTINCT substr(t, i, 2), vid FROM b WHERE length(substr(t, i, 2)) = 2`
}

// videoBigramState 读取回灌状态；表不存在或无该键时返回 0。
func videoBigramState(db *gorm.DB, key string) int64 {
	var v int64
	if err := db.Raw("SELECT v FROM "+videoBigramStateTable+" WHERE k = ?", key).Scan(&v).Error; err != nil {
		return 0
	}
	return v
}

// initVideoBigrams 只建表、索引、状态表与三条触发器，是毫秒级的启动路径操作。
// 历史数据回灌由 backfillVideoBigrams 负责，二者刻意分开：回灌在大库上要几分钟，
// 不能放在 HTTP 服务开始监听之前。
func initVideoBigrams(db *gorm.DB) error {
	for _, stmt := range videoBigramDDL() {
		if err := db.Exec(stmt).Error; err != nil {
			return err
		}
	}
	for _, stmt := range videoBigramTriggerSQL() {
		if err := db.Exec(stmt).Error; err != nil {
			return err
		}
	}
	return nil
}

// videoBigramNeedsBackfill 判断是否有历史数据待回灌。
// 没有待回灌行时顺手把 done 置 1（新库或刚跑完的库），检索立即走索引，
// 也不必为它启动后台任务。
func videoBigramNeedsBackfill(db *gorm.DB) bool {
	if videoBigramState(db, "done") == 1 {
		return false
	}
	var pending int64
	cursor := videoBigramState(db, "cursor")
	if err := db.Raw("SELECT 1 FROM videos WHERE id > ? LIMIT 1", cursor).Scan(&pending).Error; err != nil {
		return false
	}
	if pending == 0 {
		db.Exec("INSERT OR REPLACE INTO " + videoBigramStateTable + "(k, v) VALUES('done', 1)")
		return false
	}
	return true
}

// errBigramStopped 表示回灌被 pace 主动中止：不是失败，也不该把 done 置 1。
var errBigramStopped = errors.New("bigram backfill stopped")

// backfillVideoBigrams 分批补齐历史数据。
// pace 在每批之前调用，返回 true 就停下并返回 errBigramStopped；传 nil 表示
// 一口气跑完（测试与手动调用）。每批都用静默 logger，避免刷 SLOW SQL 日志。
func backfillVideoBigrams(db *gorm.DB, pace func() bool) error {
	quiet := db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
	cursor := videoBigramState(db, "cursor")
	for {
		if pace != nil && pace() {
			return errBigramStopped
		}
		var lo, hi int64
		row := db.Raw(
			"SELECT COALESCE(MIN(id), 0), COALESCE(MAX(id), 0) FROM (SELECT id FROM videos WHERE id > ? ORDER BY id LIMIT ?)",
			cursor, videoBigramBatch,
		).Row()
		if err := row.Scan(&lo, &hi); err != nil {
			return err
		}
		if hi == 0 {
			break
		}
		if err := quiet.Exec(videoBigramBackfillSQL(lo, hi)).Error; err != nil {
			return err
		}
		// 先提交进度再进入下一批：被打断最多重复当前批，INSERT OR IGNORE 幂等。
		if err := quiet.Exec("INSERT OR REPLACE INTO "+videoBigramStateTable+"(k, v) VALUES('cursor', ?)", hi).Error; err != nil {
			return err
		}
		cursor = hi
	}
	return db.Exec("INSERT OR REPLACE INTO " + videoBigramStateTable + "(k, v) VALUES('done', 1)").Error
}

// ensureVideoBigrams 供测试与手动调用，一次性同步补齐；正式启动走 init + 后台回灌。
func ensureVideoBigrams(db *gorm.DB) error {
	if err := initVideoBigrams(db); err != nil {
		return err
	}
	return backfillVideoBigrams(db, nil)
}

// videoBigramReady 表示历史数据已回灌完毕，2 字检索可以走倒排表。
func videoBigramReady(db *gorm.DB) bool {
	return videoBigramState(db, "done") == 1
}

// dropRetiredVideoIndexes 清理历史库里已不再使用的索引。
//
// AutoMigrate 只会补建索引、不会删掉实体里已移除的索引，所以这里显式 DROP。
// 不提升 schemaVersion：删索引不需要重新 ANALYZE，更不该把 v6 的整表回灌 +
// ANALYZE 重新触发一遍（那正是旧版 v7 在大库上「每次启动都被健康检查打断」的成因）。
// IF EXISTS 让每次启动的空转代价接近零。
//
// 其中两条评分排序表达式索引是随「移除评分排序」一并清理的：它们曾是大库上
// 一次性 CREATE INDEX 要等 165 秒、写入时还要持续维护的重索引，功能移除后
// 不再有任何查询会用到，DROP 掉既省空间也省采集写入的索引维护成本。
func dropRetiredVideoIndexes(db *gorm.DB) {
	for _, name := range []string{
		"idx_videos_vod_name", "idx_videos_hash",
		"idx_videos_score_desc", "idx_videos_score_asc",
	} {
		db.Exec("DROP INDEX IF EXISTS " + name)
	}
}
