// Package repository 封装基于 GORM 的 SQLite 数据访问。
//
// 每个实体拥有独立的文件、独立的对象与独立的对外接口：
//
//	source.go   → SourceRepository    （model.Source）
//	video.go    → VideoRepository     （model.Video）
//	category.go → CategoryRepository  （model.Category）
//	job.go      → JobRepository       （model.CrawlJob）
//	user.go     → UserRepository      （model.User / model.Session / model.UserSource）
//
// 本文件只负责共享数据库连接、自动迁移、装配各实体仓储对象与连接生命周期，
// 不承载任何实体的读写方法。
package repository

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
	"gorm.io/gorm"
)

// schemaVersion 是库结构的「检索层」版本号，记在 SQLite 的 PRAGMA user_version 里。
//
// 版本号只用于判断「是否需要重算统计信息」：
// 索引与全文索引都由 AutoMigrate / ensureVideoSearch 幂等创建，但 SQLite 的
// 查询计划器要靠 ANALYZE 得到的 sqlite_stat1 才能选对索引（例如「多站点 +
// 时间倒序」在没有统计信息时会退化成「先按站点过滤再全量排序」）。
// 升级后统计信息必然过期，故版本号提升时重跑一次。
//
// 本库按「全新库」对待：不承载任何历史数据回灌，旧版本的一次性数据迁移
// （地域拆表、分类层级补建等）已全部移除。索引集变化时把版本号 +1 即可。
//
//	7 → 移除 idx_videos_vod_year，并下线 areas / tags / assets 及其连接表
const schemaVersion = 7

// Repositories 是各实体仓储的容器。
// 它只是装配结果，不是「大杂烩仓储」：任何实体读写都必须走对应的那个对象。
type Repositories struct {
	Sources       SourceRepository
	Videos        VideoRepository
	Categories    CategoryRepository
	Jobs          JobRepository
	Users         UserRepository
	Subscriptions SubscriptionRepository

	db          *gorm.DB
	path        string            // 对外展示的数据库描述（MySQL 为脱敏串，不含密码）
	maintenance *videoMaintenance // SQLite 启动后的一次性维护任务，Close 时请求停止
	stop        chan struct{}     // 关闭时通知后台索引任务退出
	closeOnce   sync.Once
}

// sqliteDSN 给连接串加上 busy_timeout。它必须写在 DSN 上，由驱动对**每个**
// 新建连接执行 PRAGMA；Open 之后再用 Exec 设一次，连接池换连接就失效了，
// 后台建索引与前台写入并发时会立刻 SQLITE_BUSY。
func sqliteDSN(path string) string {
	const pragma = "_pragma=busy_timeout(30000)"
	if strings.Contains(path, "?") {
		return path + "&" + pragma
	}
	return path + "?" + pragma
}

// Open 打开（必要时创建）path 指定的 SQLite 数据库，自动迁移表结构，
// 并装配出每个实体各自的仓储对象。
//
// 保留这个签名是为了兼容既有调用与测试；需要切换后端时用 OpenOptions。
func Open(path string) (*Repositories, error) {
	return OpenOptions(Options{Driver: string(DialectSQLite), Path: path})
}

// OpenOptions 按 Options 选择数据库后端，自动迁移表结构，并装配出每个实体
// 各自的仓储对象。
//
// 方言分支只出现在这一处：SQLite 走 FTS5 trigram + bigram 倒排 + PRAGMA 维护；
// MySQL 走 InnoDB FULLTEXT(ngram) 索引。两者的检索索引都是「建不出来也只降级
// LIKE、绝不阻断启动」，因为首次建索引在大表上可能要几分钟，不能卡在 HTTP
// 监听之前（部署健康检查会超时回滚）。
func OpenOptions(o Options) (*Repositories, error) {
	dialect := o.dialect()
	if dialect == DialectSQLite {
		if dir := filepath.Dir(o.Path); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("创建数据库目录 %q: %w", dir, err)
			}
		}
	}
	db, err := openDB(o)
	if err != nil {
		return nil, err
	}
	// MySQL 新表统一 utf8mb4；SQLite 忽略 table_options。
	migrateDB := db
	if dialect == DialectMySQL {
		migrateDB = db.Set("gorm:table_options", mysqlTableOptions)
	}
	if err := migrateDB.AutoMigrate(
		&model.Source{},
		&model.Video{},
		&model.Category{},
		&model.CrawlJob{},
		&model.User{},
		&model.Session{},
		&model.UserSource{},
		&model.Subscription{},
	); err != nil {
		return nil, fmt.Errorf("迁移表结构: %w", err)
	}

	stop := make(chan struct{})
	searchReady := &atomic.Bool{}
	var mt *videoMaintenance
	if dialect == DialectMySQL {
		// ALTER TABLE ... ADD FULLTEXT INDEX 在大表上会重建表，放后台执行；
		// 索引就绪前关键词检索降级为 LIKE，结果始终正确。
		searchReady.Store(mysqlFulltextExists(db))
		go maintainMySQLFulltext(db, searchReady, stop)
	} else {
		// 全文检索索引是虚拟表 + 触发器，AutoMigrate 管不到，单独幂等创建。
		// 建不出来（例如底层 SQLite 未编译 FTS5）只降级为 LIKE 检索。
		if err := ensureVideoSearch(db); err != nil {
			log.Printf("建立影片全文索引失败，关键词检索将退回 LIKE 全表扫描: %v", err)
		}
		// 历史库里已废弃的单列索引由 AutoMigrate 留着不动，显式清掉。
		// DROP INDEX IF EXISTS 是 SQLite 语法，MySQL 分支不执行。
		dropRetiredVideoIndexes(db)
		// 2 字关键词的 bigram 倒排表：只同步建表与触发器（毫秒级）。
		bigramOK := true
		if err := initVideoBigrams(db); err != nil {
			log.Printf("建立两字关键词索引失败，2 字检索将退回 LIKE: %v", err)
			bigramOK = false
		}
		searchReady.Store(videoSearchTableExists(db))
		// PRAGMA user_version 迁移 / ANALYZE / PRAGMA optimize 都是 SQLite 专有。
		prepareDatabase(db)
		// 分钟级的历史数据回灌放后台，服务立即开始监听。
		if bigramOK && videoBigramNeedsBackfill(db) {
			mt = startVideoMaintenance(o.Path)
		}
	}

	return &Repositories{
		Sources:       NewSourceRepository(db),
		Videos:        newVideoRepository(db, dialect, searchReady),
		Categories:    NewCategoryRepository(db),
		Jobs:          NewJobRepository(db),
		Subscriptions: NewSubscriptionRepository(db),
		Users:         NewUserRepository(db),
		db:            db,
		path:          o.databaseDisplay(),
		maintenance:   mt,
		stop:          stop,
	}, nil
}

// Path 返回数据库描述：SQLite 为文件路径，MySQL 为脱敏后的连接目标。
// 健康检查与设置页都读它，因此绝不能包含密码。
func (r *Repositories) Path() string { return r.path }

// prepareDatabase 维护查询计划器依赖的统计信息。
//
//   - 首次（user_version 落后于 schemaVersion）：整库 ANALYZE 一次（大表约几百毫秒，
//     仅发生一次）；
//   - 之后每次启动：PRAGMA optimize，由 SQLite 自行判断哪些索引需要补统计信息，代价极低。
//
// 不做 ANALYZE 的话，新建的复合索引在 sqlite_stat1 里没有记录，计划器仍会按旧索引
// 估算行数，出现「有索引却仍全量排序」的情况。
//
// 这里只维护统计信息，不执行任何历史数据回灌：本库按全新库处理。
func prepareDatabase(db *gorm.DB) {
	var version int
	if err := db.Raw("PRAGMA user_version").Scan(&version).Error; err != nil {
		return
	}
	if version < schemaVersion {
		// 先推进版本号再 ANALYZE：ANALYZE 只影响查询计划选择，失败（磁盘紧张、
		// 被 kill、超时）不该让迁移「永远没完成」——否则下次启动会重跑 ANALYZE，
		// 在大库上每次都要几十秒，把带健康检查的部署拖死。
		// 统计信息后续由每次启动的 PRAGMA optimize 补齐。
		db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion))
		if err := db.Exec("ANALYZE").Error; err != nil {
			log.Printf("重建统计信息失败（不影响数据与启动，后续启动的 PRAGMA optimize 会补齐）: %v", err)
		}
		return
	}
	db.Exec("PRAGMA optimize")
}

// Close 关闭底层连接。
func (r *Repositories) Close() error {
	var stopErr error
	r.closeOnce.Do(func() {
		// 先停后台索引 / 回灌任务，避免它们继续往即将关闭的库里写。
		if r.stop != nil {
			close(r.stop)
		}
		if r.maintenance != nil {
			r.maintenance.Stop(2 * time.Second)
		}
		sqlDB, err := r.db.DB()
		if err != nil {
			stopErr = err
			return
		}
		stopErr = sqlDB.Close()
	})
	return stopErr
}
