package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
)

// ErrPaused 表示采集被手动暂停：不是失败，进度（增量游标与续采页码）已保存。
// controller 据此把结果按 200 返回，并把状态展示为「已暂停」。
var ErrPaused = errors.New("采集已暂停")

// TaskView 是一个采集任务的实时视图（排队中 / 运行中 / 已暂停）。
//
// Percent 为 -1 表示总量未知（站点未返回总页数且尚未采完），前端应展示
// 不定进度条；其余字段都是「截至本次快照」的累计值。
type TaskView struct {
	JobID       uint   `json:"jobId"`
	SourceID    uint   `json:"sourceId"`
	SourceName  string `json:"sourceName"`
	Mode        string `json:"mode"`
	Status      string `json:"status"`
	Attempt     int    `json:"attempt"`     // 当前是第几次尝试（含自动重试）
	MaxAttempts int    `json:"maxAttempts"` // 最多尝试次数（1 + 自动重试次数）

	Page      int `json:"page"`      // 当前正在处理 / 已处理到的页码
	PageCount int `json:"pageCount"` // 站点报告的总页数，0 表示未知
	Pages     int `json:"pages"`     // 已完成的页数

	Fetched   int `json:"fetched"`
	New       int `json:"new"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`

	Percent        float64 `json:"percent"`        // 0..100，-1 表示未知
	ItemsPerSecond float64 `json:"itemsPerSecond"` // 采集速率（条/秒）
	EtaSeconds     int     `json:"etaSeconds"`     // 预估剩余秒数，0 表示无法估算
	RetryInSeconds int     `json:"retryInSeconds"` // >0 表示正在等待自动重试

	Message        string    `json:"message"`
	StartedAt      time.Time `json:"startedAt"`
	ElapsedSeconds int       `json:"elapsedSeconds"`
}

// Capacity 描述采集任务的并发容量。
type Capacity struct {
	MaxConcurrent int `json:"maxConcurrent"` // 允许同时运行的任务数
	Running       int `json:"running"`       // 正在运行
	Queued        int `json:"queued"`        // 排队等待中
}

// TaskList 是任务列表接口的响应。
type TaskList struct {
	Capacity Capacity   `json:"capacity"`
	Items    []TaskView `json:"items"`
}

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
	message string
	retryAt time.Time
	cancel  context.CancelFunc

	pauseOnce sync.Once
	pauseCh   chan struct{}
}

func newCrawlTask(job *model.CrawlJob, maxAttempts int) *crawlTask {
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	return &crawlTask{
		job:         job,
		sourceID:    job.SourceID,
		sourceName:  job.SourceName,
		mode:        job.Mode,
		startedAt:   time.Now(),
		maxAttempts: maxAttempts,
		status:      model.JobQueued,
		attempt:     1,
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

// beginAttempt 在每次尝试开始时重置页码显示并记录尝试序号。
func (t *crawlTask) beginAttempt(attempt, page int) {
	t.mu.Lock()
	t.attempt, t.page, t.retryAt = attempt, page, time.Time{}
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
func (t *crawlTask) progress(res *RunResult, pageCount int) {
	t.mu.Lock()
	t.pages, t.fetched = res.Pages, res.Fetched
	t.added, t.updated, t.changed = res.New, res.Updated, res.Unchanged
	if pageCount > 0 {
		t.pageCnt = pageCount
	}
	t.mu.Unlock()
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
		MaxAttempts:    t.maxAttempts,
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

// startOptions 描述「从哪一页、以什么名义」发起任务。
type startOptions struct {
	resumePage int  // >1 时显式从该页续采（暂停/失败的任务重试时用）
	fromStart  bool // 强制从第 1 页开始（重新运行）
	retryOf    uint // 手动重试 / 重新运行的来源任务 ID
}

// begin 校验参数、占用站点、落一条 queued 任务记录并登记到运行时任务表。
//
// 同一站点在同一时刻只允许一个任务（不同站点可并行，总量受并发上限约束）。
// 未显式指定起始页时，全量模式下若该站上一次同模式任务是「已暂停/已失败」，
// 则从记录的续采页码继续。
func (s *CrawlService) begin(sourceID uint, mode string, opt startOptions) (*crawlTask, *model.Source, error) {
	if mode != model.ModeFull && mode != model.ModeIncremental {
		return nil, nil, invalid("不支持的采集模式 %q（可选 full / incremental）", mode)
	}
	src, err := s.sources.GetSource(sourceID)
	if err != nil {
		return nil, nil, wrapRepo(err)
	}

	startPage := 1
	switch {
	case opt.fromStart:
		startPage = 1
	case opt.resumePage > 1:
		startPage = opt.resumePage
	case mode == model.ModeFull:
		if last, lastErr := s.jobs.LastJob(sourceID, mode); lastErr == nil &&
			(last.Status == model.JobPaused || last.Status == model.JobFailed) && last.ResumePage > 1 {
			startPage = last.ResumePage
		}
	}

	job := &model.CrawlJob{
		SourceID: src.ID, SourceName: src.Name, Mode: mode,
		Status: model.JobQueued, StartedAt: time.Now(),
		ResumePage: startPage, Attempt: 1, RetryOf: opt.retryOf,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, running := s.bySource[src.ID]; running {
		return nil, nil, conflict("站点「%s」已有采集任务在进行中，可先暂停或等待其结束", src.Name)
	}
	if err := s.jobs.CreateJob(job); err != nil {
		return nil, nil, fmt.Errorf("创建采集记录失败: %w", err)
	}
	task := newCrawlTask(job, 1+s.cfg.RetryTimes)
	s.tasks[job.ID] = task
	s.bySource[src.ID] = job.ID
	return task, src, nil
}

// runTask 是任务的实际执行体：排队 → 运行（失败则退避重试）→ 收尾。
func (s *CrawlService) runTask(ctx context.Context, task *crawlTask, src *model.Source) (*RunResult, error) {
	defer s.releaseSite(task)

	runCtx, cancel := context.WithCancel(ctx)
	task.attachCancel(cancel)
	defer cancel()

	// 并发上限内的排队；排队期间可被暂停/取消打断。
	if err := s.limiter.acquire(runCtx); err != nil {
		status, msg := model.JobFailed, "任务被取消："+err.Error()
		if task.paused() {
			status, msg = model.JobPaused, "排队中被暂停，尚未开始采集"
		}
		res, finishErr := s.settle(task, nil, status, msg)
		if finishErr != nil {
			return res, finishErr
		}
		if status == model.JobPaused {
			return res, ErrPaused
		}
		return res, err
	}
	defer s.limiter.release()

	if task.paused() {
		res, finishErr := s.settle(task, nil, model.JobPaused, "排队中被暂停，尚未开始采集")
		if finishErr != nil {
			return res, finishErr
		}
		return res, ErrPaused
	}

	if err := s.jobs.StartJob(task.job); err != nil {
		return nil, fmt.Errorf("更新采集记录失败: %w", err)
	}
	task.setStatus(model.JobRunning)

	res := &RunResult{
		JobID: task.job.ID, SourceID: task.sourceID, SourceName: task.sourceName,
		Mode: task.mode, StartedAt: task.job.StartedAt, ResumePage: task.job.ResumePage,
	}

	startPage := task.job.ResumePage
	if startPage < 1 {
		startPage = 1
	}
	var crawlErr error
	for attempt := 1; attempt <= task.maxAttempts; attempt++ {
		task.beginAttempt(attempt, startPage)
		crawlErr = s.crawl(runCtx, src, task.mode, startPage, task, res)
		if crawlErr == nil || task.paused() {
			break
		}
		// 本轮失败：还有重试机会就退避等待，等待期间可被暂停 / 取消打断。
		if attempt >= task.maxAttempts || runCtx.Err() != nil {
			break
		}
		next := res.ResumePage
		if next < 1 {
			next = 1
		}
		if !s.awaitRetry(runCtx, task, attempt, crawlErr) {
			break
		}
		startPage = next
	}

	status, msg := model.JobSuccess, "采集完成"
	switch {
	case task.paused():
		status = model.JobPaused
		msg = fmt.Sprintf("已暂停：已完成 %d 页，可稍后继续采集", res.Pages)
		if crawlErr != nil && !errors.Is(crawlErr, ErrPaused) {
			msg = fmt.Sprintf("已暂停：已完成 %d 页（上次尝试失败：%v），可稍后继续采集", res.Pages, crawlErr)
		}
	case crawlErr != nil:
		status = model.JobFailed
		msg = crawlErr.Error()
		if task.maxAttempts > 1 {
			msg = fmt.Sprintf("%s（已自动尝试 %d 次）", msg, task.attempt)
		}
	}
	out, finishErr := s.settle(task, res, status, msg)
	if finishErr != nil {
		return out, finishErr
	}
	switch status {
	case model.JobPaused:
		return out, ErrPaused
	case model.JobFailed:
		return out, crawlErr
	}
	return out, nil
}

// awaitRetry 在两次尝试之间退避等待；返回 false 表示被暂停或取消，应终止任务。
func (s *CrawlService) awaitRetry(ctx context.Context, task *crawlTask, attempt int, cause error) bool {
	delay := s.retryDelay(attempt)
	task.retryPending(delay, fmt.Sprintf("第 %d 次尝试失败：%v；%d 秒后自动重试（%d/%d）",
		attempt, cause, int(delay.Seconds()), attempt+1, task.maxAttempts))

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		task.clearRetry()
		return true
	case <-task.pauseCh:
		task.clearRetry()
		return false
	case <-ctx.Done():
		task.clearRetry()
		return false
	}
}

// retryDelay 计算第 attempt 次失败后的等待时长：线性翻倍退避，上限 30 秒。
func (s *CrawlService) retryDelay(attempt int) time.Duration {
	base := s.cfg.RetryBackoffSeconds
	if base <= 0 {
		base = 3
	}
	d := time.Duration(base) * time.Second
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= 30*time.Second {
			return 30 * time.Second
		}
	}
	return d
}

// settle 写采集记录与站点状态；暂停/成功都推进游标，失败不推进以免漏采。
func (s *CrawlService) settle(task *crawlTask, res *RunResult, status, msg string) (*RunResult, error) {
	now := time.Now()
	if res == nil {
		res = &RunResult{
			JobID: task.job.ID, SourceID: task.sourceID, SourceName: task.sourceName,
			Mode: task.mode, StartedAt: task.job.StartedAt, ResumePage: task.job.ResumePage,
		}
	}
	res.FinishedAt = now
	res.Status, res.Message = status, msg
	if res.ResumePage < 1 {
		res.ResumePage = 1
	}

	job := task.job
	job.Fetched, job.New, job.Updated = res.Fetched, res.New, res.Updated
	job.Unchanged, job.Pages, job.Total = res.Unchanged, res.Pages, res.Total
	job.ResumePage = res.ResumePage
	job.Attempt = task.currentAttempt()
	if err := s.jobs.FinishJob(job, status, msg); err != nil {
		return res, fmt.Errorf("写入采集记录失败: %w", err)
	}

	cursor := ""
	if status == model.JobSuccess || status == model.JobPaused {
		cursor = res.Cursor
	}
	errMsg := ""
	if status == model.JobFailed {
		errMsg = msg
	}
	if err := s.sources.UpdateSourceResult(job.SourceID, job.Mode, status, cursor, errMsg, now); err != nil {
		return res, fmt.Errorf("更新站点状态失败: %w", err)
	}
	return res, nil
}

func (t *crawlTask) currentAttempt() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.attempt
}

// releaseSite 把任务从运行时任务表中移除，释放该站点。
func (s *CrawlService) releaseSite(task *crawlTask) {
	s.mu.Lock()
	if cur, ok := s.bySource[task.sourceID]; ok && cur == task.job.ID {
		delete(s.bySource, task.sourceID)
	}
	delete(s.tasks, task.job.ID)
	s.mu.Unlock()
}

// startTask 登记并异步执行一个任务：立即返回任务视图，实际抓取在后台排队执行。
func (s *CrawlService) startTask(sourceID uint, mode string, opt startOptions) (*TaskView, error) {
	task, src, err := s.begin(sourceID, mode, opt)
	if err != nil {
		return nil, err
	}
	go func() {
		_, _ = s.runTask(context.Background(), task, src)
	}()
	view := task.snapshot()
	return &view, nil
}

// StartTask 异步发起一个采集任务。
func (s *CrawlService) StartTask(sourceID uint, mode string) (*TaskView, error) {
	return s.startTask(sourceID, mode, startOptions{})
}

// PauseTask 手动暂停一个排队中或运行中的任务。
func (s *CrawlService) PauseTask(jobID uint) (*TaskView, error) {
	s.mu.Lock()
	task, ok := s.tasks[jobID]
	s.mu.Unlock()
	if !ok {
		return nil, notFound("任务 %d 不存在或已结束", jobID)
	}
	task.requestPause()
	view := task.snapshot()
	return &view, nil
}

// ResumeTask 继续一个已暂停的任务：按原站点与原模式重新入队。
// 全量模式从暂停时的页码续采，增量模式从已保存的游标继续。
func (s *CrawlService) ResumeTask(jobID uint) (*TaskView, error) {
	s.mu.Lock()
	if task, ok := s.tasks[jobID]; ok {
		s.mu.Unlock()
		view := task.snapshot()
		return &view, nil // 还在跑，视为已恢复
	}
	s.mu.Unlock()

	job, err := s.jobs.GetJob(jobID)
	if err != nil {
		return nil, wrapRepo(err)
	}
	if job.Status != model.JobPaused {
		return nil, conflict("任务 %d 当前状态为「%s」，只有已暂停的任务可以继续", jobID, job.Status)
	}
	return s.startTask(job.SourceID, job.Mode, startOptions{resumePage: job.ResumePage})
}

// RetryTask 手动重试一个失败的任务：从失败页续采，不重复已完成的页面。
func (s *CrawlService) RetryTask(jobID uint) (*TaskView, error) {
	job, err := s.jobs.GetJob(jobID)
	if err != nil {
		return nil, wrapRepo(err)
	}
	if job.Status != model.JobFailed {
		return nil, conflict("任务 %d 当前状态为「%s」，只有失败的任务可以重试", jobID, job.Status)
	}
	return s.startTask(job.SourceID, job.Mode,
		startOptions{resumePage: job.ResumePage, retryOf: job.ID})
}

// RerunTask 重新运行一条已结束的任务：忽略断点，从第 1 页重新采集。
func (s *CrawlService) RerunTask(jobID uint) (*TaskView, error) {
	job, err := s.jobs.GetJob(jobID)
	if err != nil {
		return nil, wrapRepo(err)
	}
	if job.Status == model.JobRunning || job.Status == model.JobQueued {
		return nil, conflict("任务 %d 仍在进行中，请先暂停后再重新运行", jobID)
	}
	return s.startTask(job.SourceID, job.Mode, startOptions{fromStart: true, retryOf: job.ID})
}

// LiveTask 返回某条采集记录的实时进度视图；任务不在运行时返回 nil。
func (s *CrawlService) LiveTask(jobID uint) *TaskView {
	s.mu.Lock()
	task, ok := s.tasks[jobID]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	view := task.snapshot()
	return &view
}

// ListTasks 返回当前排队中/运行中/暂停中的任务及并发容量。
func (s *CrawlService) ListTasks() TaskList {
	s.mu.Lock()
	views := make([]TaskView, 0, len(s.tasks))
	for _, task := range s.tasks {
		views = append(views, task.snapshot())
	}
	s.mu.Unlock()
	sort.Slice(views, func(i, j int) bool { return views[i].JobID < views[j].JobID })

	active, max, queued := s.limiter.state()
	return TaskList{
		Capacity: Capacity{MaxConcurrent: max, Running: active, Queued: queued},
		Items:    views,
	}
}

// Capacity 返回当前并发容量。
func (s *CrawlService) Capacity() Capacity {
	active, max, queued := s.limiter.state()
	return Capacity{MaxConcurrent: max, Running: active, Queued: queued}
}

// SetMaxConcurrent 运行时调整「同时运行的任务数」上限（重启后回到配置文件的值）。
func (s *CrawlService) SetMaxConcurrent(n int) (Capacity, error) {
	if n < 1 || n > 64 {
		return Capacity{}, invalid("max_concurrent 需在 1..64 之间")
	}
	s.limiter.setMax(n)
	return s.Capacity(), nil
}
