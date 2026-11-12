package repository

import "gorm.io/gorm"

// 评分排序的表达式索引。
//
// vod_score 是站点给的字符串（"7.5" / "" / "HD" 之类），排序时统一转成数值：
// CAST(NULLIF(TRIM(vod_score), ”) AS REAL)。表达式上用不了普通 B-Tree 索引，
// SQLite 只能全表扫描 + 临时 B-Tree 排序，15 万行实测约 500 ms，生产库更慢。
// 表达式索引把排序值物化进索引，实测降到 0.2 ms。
//
// 为什么不改表结构加一个 REAL 列：那要回灌全部历史行、让触发器同步、还可能
// 触发整表重写，风险高。表达式索引由 SQLite 在 CREATE INDEX 时扫一遍现有数据
// 自己填满，之后随写入自动维护，不需要任何回灌代码或状态表，也不改表结构。
//
// 为什么建索引是同步的、bigram 回灌却是后台的：CREATE INDEX 会改 sqlite_master，
// 期间其它连接连 prepare 语句都会拿到 SQLITE_BUSY（busy_timeout 对 schema 锁
// 不生效），必须与在线请求串行；而它本身是一次性秒级操作（15 万行约 0.8 s），
// 不会拖死启动。真正分钟级的是历史数据回灌，那个才放后台。
//
// 两条索引分别对应 score_desc / score_asc，保持原有次级排序语义完全不变
// （评分相同时都按 vod_time DESC, id DESC）。共用一个索引反向扫描会顺带把
// 次级顺序反过来，属于行为突变，不做。
const videoScoreExpr = "CAST(NULLIF(TRIM(vod_score), '') AS REAL)"

var videoSortIndexNames = []string{"idx_videos_score_desc", "idx_videos_score_asc"}

// videoSortIndexDDL 幂等创建两条评分排序索引。
// 表达式文本必须与 videoOrder 拼接出的 ORDER BY 完全一致（都引用 videoScoreExpr）。
func videoSortIndexDDL() []string {
	return []string{
		"CREATE INDEX IF NOT EXISTS " + videoSortIndexNames[0] +
			" ON videos(" + videoScoreExpr + " DESC, vod_time DESC, id DESC)",
		"CREATE INDEX IF NOT EXISTS " + videoSortIndexNames[1] +
			" ON videos(" + videoScoreExpr + " ASC, vod_time DESC, id DESC)",
	}
}

// videoSortIndexesMissing 判断是否有索引尚未创建。
// 查询失败时保守返回 true：后台任务用 IF NOT EXISTS，重复执行只是空转。
func videoSortIndexesMissing(db *gorm.DB) bool {
	var n int64
	if err := db.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name IN ?", videoSortIndexNames).
		Scan(&n).Error; err != nil {
		return true
	}
	return n < int64(len(videoSortIndexNames))
}
