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
const videoBigramPace = 30 * time.Millisecond

// bigramBackfill 是启动后的后台回灌任务。它持有独立的 SQLite 连接，不占用
// 主连接池（主库 SetMaxOpenConns(1)），在线请求不会排在长事务后面。
//
// 任务只写 video_bigrams 与 video_bigram_state，不改写 videos；每批进度落库，
// 进程被 kill 或主动停止都安全——下次启动从 cursor 续跑，完成前 2 字检索降级
// LIKE，结果不漏。
type bigramBackfill struct {
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

func startVideoBigramBackfill(path string) *bigramBackfill {
	t := &bigramBackfill{stop: make(chan struct{}), done: make(chan struct{})}
	go t.run(path)
	return t
}

func (t *bigramBackfill) run(path string) {
	defer close(t.done)
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		log.Printf("两字索引后台回灌无法打开数据库，2 字检索将退回 LIKE: %v", err)
		return
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
		defer sqlDB.Close()
	}
	// 与前台采集写共用同一个 SQLite 文件：遇到写锁等待，而不是立刻报
	// "database is locked"。主库连接也设置了同样的 busy_timeout。
	db.Exec("PRAGMA busy_timeout = 10000")
	if err := backfillVideoBigrams(db, t.pace); err != nil && !errors.Is(err, errBigramStopped) {
		log.Printf("两字索引后台回灌未完成（下次启动会从进度续跑）: %v", err)
	}
}

func (t *bigramBackfill) pace() bool {
	select {
	case <-t.stop:
		return true
	default:
		time.Sleep(videoBigramPace)
		return false
	}
}

// Stop 请求回灌停止并最多等 wait。进程被 SIGKILL 来不及调用也没关系：
// 每批进度已落库，未提交的事务由 SQLite 回滚，下次启动自动续跑。
func (t *bigramBackfill) Stop(wait time.Duration) {
	if t == nil {
		return
	}
	t.once.Do(func() { close(t.stop) })
	select {
	case <-t.done:
	case <-time.After(wait):
	}
}
