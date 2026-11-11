package repository

import (
	"fmt"
	"log"
	"strings"
	"time"

	"gorm.io/gorm"
)

// trimmedVideoColumns 是已从 model.Video 实体与 VodFields 清单裁剪掉的 23 个
// 苹果CMS 后台管理字段在 videos 表里对应的物理列名。
//
// 上一轮只改了实体与字段清单，GORM 的 AutoMigrate 不会删除已存在的列，历史库
// 仍带着这 23 列与其中的数据。这里按 SchemaVersion 升级做一次性物理删除，
// 让库结构与裁剪后的实体重新对齐，并回收被这些列占用的空间。
//
// 这些列均为后台管理/统计字段（审核、模板、积分、密码、关联等），不参与采集
// 入库（实体已无对应字段）、不参与检索（FTS 只索引 name/en/actor/director/
// class/area）、不参与任何查询与排序，删除不影响任何功能。接口若仍返回同名
// 键，会按既有设计落入 Extra，不丢数据。
var trimmedVideoColumns = []string{
	"vod_status", "vod_audit_remark", "vod_letter", "vod_tpl", "vod_lock",
	"vod_level", "vod_copyright", "vod_points", "vod_points_play", "vod_points_down",
	"vod_share_count", "vod_time_hits", "vod_recycle_time", "vod_trysee",
	"vod_reurl", "vod_rel_vod", "vod_rel_art",
	"vod_pwd", "vod_pwd_url", "vod_pwd_play", "vod_pwd_play_url", "vod_pwd_down", "vod_pwd_down_url",
}

// dropTrimmedVideoColumns 物理删除历史库 videos 表里遗留的裁剪列，并在确实删过
// 列后执行一次 VACUUM 回收空间。
//
// 幂等且自愈：每次启动都调用，先读 pragma_table_info 得到实际列集合，只删当前
// 仍存在的列。因此以下情况都安全：
//   - 全新库（AutoMigrate 按裁剪后实体建表，本就没有这些列）→ 空操作；
//   - 老库首次升级 → 删除后 VACUUM；
//   - 上次删除/VACUUM 中途被打断（user_version 尚未提升）→ 重开时续做；
//   - 有人用旧二进制重新建出带列的表 → 重开时再次清掉。
//
// 依赖 SQLite 3.35+ 的 ALTER TABLE ... DROP COLUMN（本项目 modernc 驱动支持，
// 见 video_trim_test.go）。该语句会保留表上的索引、触发器、FTS 外部内容与其余
// 列的数据；实测见同测试。VACUUM 必须在事务外执行，且只删过列才做，避免每次
// 启动都重写整库（大库 VACUUM 可达数秒到数十秒）。
//
// 出错只记录日志、不阻断启动：删列是结构优化，即便本次失败，后续启动会重试，
// 程序用裁剪后的实体访问表也不依赖这些列是否存在。
func dropTrimmedVideoColumns(db *gorm.DB) {
	var present []string
	if err := db.Raw("SELECT name FROM pragma_table_info('videos')").Scan(&present).Error; err != nil {
		log.Printf("读取 videos 列信息失败，跳过裁剪列清理: %v", err)
		return
	}
	have := make(map[string]bool, len(present))
	for _, c := range present {
		have[c] = true
	}

	toDrop := make([]string, 0, len(trimmedVideoColumns))
	for _, c := range trimmedVideoColumns {
		if have[c] {
			toDrop = append(toDrop, c)
		}
	}
	if len(toDrop) == 0 {
		return // 已是目标结构，连 VACUUM 都不做
	}

	start := time.Now()
	log.Printf("清理 videos 表 %d 个已裁剪的历史列: %s", len(toDrop), strings.Join(toDrop, ", "))
	// DROP COLUMN 会拒绝删除「仍被索引引用」的列（error in index ... after
	// drop column）。新库索引都建在保留列上，但历史库 / 第三方可能建过引用这些
	// 后台字段的索引；删列前先移除它们——列都没了，这类索引本也无意义。
	dropSet := make(map[string]bool, len(toDrop))
	for _, c := range toDrop {
		dropSet[c] = true
	}
	if err := dropIndexesReferencing(db, dropSet); err != nil {
		log.Printf("清理引用裁剪列的索引失败（下次启动会重试）: %v", err)
		return
	}

	// 逐列删除。逐条 ALTER 各自成事务（SQLite 的 DROP COLUMN 不支持一条语句
	// 删多列），单列失败不回滚已删的 —— 靠上面的幂等扫描在下次启动续做。
	// 大库（几十万行）每列约十余秒，期间会持续打印进度，避免被误判为卡死。
	for i, col := range toDrop {
		stmt := fmt.Sprintf(`ALTER TABLE videos DROP COLUMN "%s"`, col)
		if err := db.Exec(stmt).Error; err != nil {
			log.Printf("删除列 %s 失败（下次启动会重试）: %v", col, err)
			return
		}
		if (i+1)%5 == 0 || i+1 == len(toDrop) {
			log.Printf("已删除 %d/%d 列（耗时 %s）", i+1, len(toDrop), time.Since(start).Round(time.Millisecond))
		}
	}
	log.Printf("已删除 %d 个历史列，耗时 %s；开始 VACUUM 回收空间", len(toDrop), time.Since(start).Round(time.Millisecond))
	if err := db.Exec("VACUUM").Error; err != nil {
		// VACUUM 失败不影响正确性（列已删，只是空间未回收），下次启动因列已
		// 不存在不会再触发 VACUUM；这里记日志即可，不反复重写大库。
		log.Printf("VACUUM 回收空间失败（不影响数据）: %v", err)
		return
	}
	log.Printf("videos 裁剪列清理完成，总耗时 %s", time.Since(start).Round(time.Millisecond))
}

// dropIndexesReferencing 删除 videos 表上「引用了待删列之一」的索引，否则
// SQLite 会因为索引仍指向该列而拒绝 DROP COLUMN。
//
// 用 PRAGMA index_list + index_info 拿到每个索引实际引用的列，而不是去解析
// sqlite_master 里的建索引 SQL（后者有引号/大小写/表达式等歧义）。唯一约束
// 与主键的自动索引（origin = u / pk）无法单独删除，但待删列都不是唯一键或
// 主键的一部分（见 model.Video），故不会命中；真命中则交由上层报错重试。
func dropIndexesReferencing(db *gorm.DB, dropSet map[string]bool) error {
	type indexRow struct {
		Name   string
		Unique int
		Origin string
	}
	var indexes []indexRow
	if err := db.Raw("PRAGMA index_list('videos')").Scan(&indexes).Error; err != nil {
		return err
	}
	for _, idx := range indexes {
		// 唯一约束 / 主键自动创建的索引不能 DROP，也不应引用后台字段。
		if idx.Origin == "u" || idx.Origin == "pk" {
			continue
		}
		type colRow struct {
			Seqno int
			Cid   int
			Name  string
		}
		var cols []colRow
		if err := db.Raw(fmt.Sprintf("PRAGMA index_info(%q)", idx.Name)).Scan(&cols).Error; err != nil {
			return err
		}
		for _, c := range cols {
			if dropSet[c.Name] {
				log.Printf("删除引用裁剪列 %s 的索引 %s", c.Name, idx.Name)
				if err := db.Exec(fmt.Sprintf(`DROP INDEX "%s"`, idx.Name)).Error; err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}
