package repository

import (
	"errors"
	"log"
	"sync"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 后台回灌每批之间的让出时间：把写锁与 CPU 留给在线请求，避免一次性重活
// 把健康检查 / 采集写饿死。
const videoMaintenancePace = 30 * time.Millisecond

// videoMaintenance 是启动后的 bigram 历史数据回灌任务。回灌在大库上要几分钟，
// 绝不能放进 repository.Open 的同步路径——服务必须立刻开始监听，否则部署健康
// 检查会超时回滚，回滚又 kill 掉任务，形成「每次部署都失败、回灌永远做不完」
// 的死循环。
//
// 注意：评分排序的表达式索引不在这里建。CREATE INDEX 会修改 sqlite_master，
// 期间其它连接连 prepare 语句都会拿到 SQLITE_BUSY（busy_timeout 对 schema 锁
// 不生效），只能放在 Open 里同步执行——它本身是秒级操作，不会拖死启动。
//
// 任务持有独立的 SQLite 连接，不占用主连接池（主库 SetMaxOpenConns(1)），
// 在线请求不会排在长事务后面。每批进度落库，进程被 kill 或主动停止都安全，
// 下次启动从 cursor 续跑。
type videoMaintenance struct {
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

func startVideoMaintenance(path string) *videoMaintenance {
	m := &videoMaintenance{stop: make(chan struct{}), done: make(chan struct{})}
	go m.run(path)
	return m
}

func (m *videoMaintenance) run(path string) {
	defer close(m.done)
	db, err := gorm.Open(sqlite.Open(sqliteDSN(path)), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		log.Printf("后台回灌无法打开数据库，2 字检索将退回 LIKE: %v", err)
		return
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
		defer sqlDB.Close()
	}
	// busy_timeout 写在 DSN 上，主库与这里每个连接都会自动带上。
	if err := backfillVideoBigrams(db, m.pace); err != nil && !errors.Is(err, errBigramStopped) {
		log.Printf("两字索引后台回灌未完成（下次启动会从进度续跑）: %v", err)
	}
}

func (m *videoMaintenance) pace() bool {
	select {
	case <-m.stop:
		return true
	default:
		time.Sleep(videoMaintenancePace)
		return false
	}
}

// Stop 请求回灌停止并最多等 wait。进程被 SIGKILL 来不及调用也没关系：
// 每批进度已落库，未提交的事务由 SQLite 回滚，下次启动自动续跑。
func (m *videoMaintenance) Stop(wait time.Duration) {
	if m == nil {
		return
	}
	m.once.Do(func() { close(m.stop) })
	select {
	case <-m.done:
	case <-time.After(wait):
	}
}
