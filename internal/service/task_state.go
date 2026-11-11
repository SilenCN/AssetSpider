package service

import (
	"context"
	"sync"
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
)

// crawlTask 是一个在跑的采集任务的运行时状态。
//
// 暂停是协作式的：pauseCh 关闭表示「请求暂停」，同时取消派生 context
// 让正在进行的 HTTP 请求尽快返回；抓取循环在页边界与请求出错处检查它，
// 从而保证已完成的页面不会被回滚、也不会只写入半页数据。
//
// 失败自动重试发生在 runTask 内层循环：每次尝试都从上一轮失败页续采，
// 退避等待期间同样可被暂停打断。
type crawlTask struct {
	job        *model.CrawlJob
	sourceID   uint
	sourceName string
	mode       string
	startedAt  time.Time

	maxAttempts int
	baseAttempt int // 本轮运行之前已累计的尝试次数（手动重试复用原记录时 >0）

	mu      sync.Mutex
	status  string
	attempt int
	page    int
	pages   int
	pageCnt int
	fetched int
	added   int
	updated int
	changed int
	total   int
	// resumeNext 是「下一轮该从第几页开始」，定期落库时写入 resume_page，
	// 保证进程重启后能从断点续采而不是从头再来。
	resumeNext int
	message    string
	retryAt    time.Time
	cancel     context.CancelFunc

	pauseOnce sync.Once
	pauseCh   chan struct{}
}

func newCrawlTask(job *model.CrawlJob, maxAttempts int) *crawlTask {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	base := job.Attempt
	if base < 0 {
		base = 0
	}
	return &crawlTask{
		job:         job,
		sourceID:    job.SourceID,
		sourceName:  job.SourceName,
		mode:        job.Mode,
		startedAt:   time.Now(),
		maxAttempts: maxAttempts,
		baseAttempt: base,
		status:      model.JobQueued,
		attempt:     base + 1,
		pages:       job.Pages,
		fetched:     job.Fetched,
		added:       job.New,
		updated:     job.Updated,
		changed:     job.Unchanged,
		total:       job.Total,
		resumeNext:  job.ResumePage,
		pauseCh:     make(chan struct{}),
	}
}

// paused 报告是否已请求暂停。
func (t *crawlTask) paused() bool {
	select {
	case <-t.pauseCh:
		return true
	default:
		return false
	}
}

// requestPause 请求暂停：幂等，同时中断在途请求与重试等待。
func (t *crawlTask) requestPause() {
	t.pauseOnce.Do(func() {
		t.mu.Lock()
		t.status = model.JobPaused
		t.mu.Unlock()
		close(t.pauseCh)
	})
	t.mu.Lock()
	cancel := t.cancel
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// attachCancel 登记取消函数；若暂停请求早于登记到达，立即取消。
func (t *crawlTask) attachCancel(cancel context.CancelFunc) {
	t.mu.Lock()
	t.cancel = cancel
	t.mu.Unlock()
	if t.paused() {
		cancel()
	}
}

func (t *crawlTask) setStatus(status string) {
	t.mu.Lock()
	t.status = status
	t.mu.Unlock()
}

func (t *crawlTask) setMessage(msg string) {
	t.mu.Lock()
	t.message = msg
	t.mu.Unlock()
}

// beginAttempt 在每次尝试开始时重置页码显示并记录累计尝试序号。
// 手动重试复用原记录时 baseAttempt 为之前已尝试的次数，序号接着往下数。
func (t *crawlTask) beginAttempt(attempt, page int) {
	t.mu.Lock()
	t.attempt, t.page, t.retryAt = t.baseAttempt+attempt, page, time.Time{}
	t.mu.Unlock()
}

// retryPending 记录「正在等待自动重试」的状态与倒计时起点。
func (t *crawlTask) retryPending(delay time.Duration, msg string) {
	t.mu.Lock()
	t.retryAt = time.Now().Add(delay)
	t.message = msg
	t.mu.Unlock()
}

func (t *crawlTask) clearRetry() {
	t.mu.Lock()
	t.retryAt = time.Time{}
	t.mu.Unlock()
}

// setPage 在开始抓取某一页时更新「当前页」，让列表能显示到页级进度。
func (t *crawlTask) setPage(page int) {
	t.mu.Lock()
	t.page = page
	t.mu.Unlock()
}

// progress 把抓取循环的当前进度同步到任务视图。
// nextPage 是「下一轮该从第几页开始」，定期落库时写进 resume_page。
func (t *crawlTask) progress(res *RunResult, pageCount, nextPage int) {
	t.mu.Lock()
	t.pages, t.fetched = res.Pages, res.Fetched
	t.added, t.updated, t.changed = res.New, res.Updated, res.Unchanged
	t.total = res.Total
	if nextPage > 0 {
		t.resumeNext = nextPage
	}
	if pageCount > 0 {
		t.pageCnt = pageCount
	}
	t.mu.Unlock()
}

// persistSnapshot 取一份可直接写库的进度副本（只含统计与断点，不含终态字段）。
// 返回副本而不是共享的 job 指针，避免与定期落库协程产生数据竞争。
func (t *crawlTask) persistSnapshot() *model.CrawlJob {
	t.mu.Lock()
	defer t.mu.Unlock()
	return &model.CrawlJob{
		ID:         t.job.ID,
		Pages:      t.pages,
		Fetched:    t.fetched,
		New:        t.added,
		Updated:    t.updated,
		Unchanged:  t.changed,
		Total:      t.total,
		ResumePage: t.resumeNext,
		Attempt:    t.attempt,
	}
}

func (t *crawlTask) snapshot() TaskView {
	t.mu.Lock()
	elapsed := time.Since(t.startedAt)
	v := TaskView{
		JobID:          t.job.ID,
		SourceID:       t.sourceID,
		SourceName:     t.sourceName,
		Mode:           t.mode,
		Status:         t.status,
		Attempt:        t.attempt,
		MaxAttempts:    t.baseAttempt + t.maxAttempts,
		Page:           t.page,
		PageCount:      t.pageCnt,
		Pages:          t.pages,
		Fetched:        t.fetched,
		New:            t.added,
		Updated:        t.updated,
		Unchanged:      t.changed,
		Message:        t.message,
		StartedAt:      t.startedAt,
		ElapsedSeconds: int(elapsed.Seconds()),
	}
	if !t.retryAt.IsZero() {
		if left := time.Until(t.retryAt); left > 0 {
			v.RetryInSeconds = int(left.Seconds()) + 1
		}
	}
	t.mu.Unlock()

	// 百分比：已完成 → 100；知道总页数 → 按页算；否则交给前端展示不定进度。
	switch {
	case v.Status == model.JobSuccess:
		v.Percent = 100
	case v.PageCount > 0 && v.Page > 0:
		v.Percent = float64(v.Page) / float64(v.PageCount) * 100
		if v.Percent > 99.9 && v.Status != model.JobSuccess {
			v.Percent = 99.9 // 未真正结束前不显示满格
		}
	default:
		v.Percent = -1
	}
	if secs := elapsed.Seconds(); secs > 0 && v.Fetched > 0 {
		v.ItemsPerSecond = float64(v.Fetched) / secs
	}
	// 用浮点秒数算剩余时间，避免采集刚开始（不足 1 秒）时被截断成 0。
	if secs := elapsed.Seconds(); secs > 0 && v.PageCount > 0 && v.Page > 0 && v.Page < v.PageCount {
		perPage := secs / float64(v.Page)
		v.EtaSeconds = int(perPage * float64(v.PageCount-v.Page))
		if v.EtaSeconds < 1 {
			v.EtaSeconds = 1
		}
	}
	return v
}
