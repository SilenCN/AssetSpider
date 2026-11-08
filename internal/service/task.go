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

// ErrPaused 表示采集被手动暂停：不是失败，进度（增量游标与全量续采页码）已保存。
// controller 据此把结果按 200 返回，并把状态展示为「已暂停」。
var ErrPaused = errors.New("采集已暂停")

// TaskView 是一个采集任务的实时视图（排队中 / 运行中 / 已暂停）。
type TaskView struct {
	JobID          uint      `json:"jobId"`
	SourceID       uint      `json:"sourceId"`
	SourceName     string    `json:"sourceName"`
	Mode           string    `json:"mode"`
	Status         string    `json:"status"`
	Pages          int       `json:"pages"`
	Fetched        int       `json:"fetched"`
	New            int       `json:"new"`
	Updated        int       `json:"updated"`
	Unchanged      int       `json:"unchanged"`
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
type crawlTask struct {
	job        *model.CrawlJob
	sourceID   uint
	sourceName string
	mode       string
	startedAt  time.Time

	mu      sync.Mutex
	status  string
	pages   int
	fetched int
	added   int
	updated int
	changed int
	cancel  context.CancelFunc

	pauseOnce sync.Once
	pauseCh   chan struct{}
}

func newCrawlTask(job *model.CrawlJob) *crawlTask {
	return &crawlTask{
		job:        job,
		sourceID:   job.SourceID,
		sourceName: job.SourceName,
		mode:       job.Mode,
		startedAt:  time.Now(),
		status:     model.JobQueued,
		pauseCh:    make(chan struct{}),
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

// requestPause 请求暂停：幂等，同时中断在途请求。
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

// progress 把抓取循环的当前进度同步到任务视图。
func (t *crawlTask) progress(res *RunResult) {
	t.mu.Lock()
	t.pages, t.fetched = res.Pages, res.Fetched
	t.added, t.updated, t.changed = res.New, res.Updated, res.Unchanged
	t.mu.Unlock()
}

func (t *crawlTask) snapshot() TaskView {
	t.mu.Lock()
	defer t.mu.Unlock()
	return TaskView{
		JobID:          t.job.ID,
		SourceID:       t.sourceID,
		SourceName:     t.sourceName,
		Mode:           t.mode,
		Status:         t.status,
		Pages:          t.pages,
		Fetched:        t.fetched,
		New:            t.added,
		Updated:        t.updated,
		Unchanged:      t.changed,
		StartedAt:      t.startedAt,
		ElapsedSeconds: int(time.Since(t.startedAt).Seconds()),
	}
}

// begin 校验参数、占用站点、落一条 queued 任务记录并登记到运行时任务表。
//
// 同一站点在同一时刻只允许一个任务（不同站点可并行，总量受并发上限约束）。
// 全量模式下若该站上一次同模式任务是「已暂停」，则从记录的续采页码继续。
func (s *CrawlService) begin(sourceID uint, mode string) (*crawlTask, *model.Source, error) {
	if mode != model.ModeFull && mode != model.ModeIncremental {
		return nil, nil, invalid("不支持的采集模式 %q（可选 full / incremental）", mode)
	}
	src, err := s.sources.GetSource(sourceID)
	if err != nil {
		return nil, nil, wrapRepo(err)
	}

	startPage := 1
	if mode == model.ModeFull {
		if last, lastErr := s.jobs.LastJob(sourceID, mode); lastErr == nil &&
			last.Status == model.JobPaused && last.ResumePage > 1 {
			startPage = last.ResumePage
		}
	}

	job := &model.CrawlJob{
		SourceID: src.ID, SourceName: src.Name, Mode: mode,
		Status: model.JobQueued, StartedAt: time.Now(), ResumePage: startPage,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, running := s.bySource[src.ID]; running {
		return nil, nil, conflict("站点「%s」已有采集任务在进行中，可先暂停或等待其结束", src.Name)
	}
	if err := s.jobs.CreateJob(job); err != nil {
		return nil, nil, fmt.Errorf("创建采集记录失败: %w", err)
	}
	task := newCrawlTask(job)
	s.tasks[job.ID] = task
	s.bySource[src.ID] = job.ID
	return task, src, nil
}

// runTask 是任务的实际执行体：排队 → 运行 → 收尾（成功/暂停/失败）。
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
	crawlErr := s.crawl(runCtx, src, task.mode, task.job.ResumePage, task, res)

	status, msg := model.JobSuccess, "采集完成"
	switch {
	case task.paused():
		status = model.JobPaused
		msg = fmt.Sprintf("已暂停：已完成 %d 页，可稍后继续采集", res.Pages)
	case crawlErr != nil:
		status, msg = model.JobFailed, crawlErr.Error()
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

// releaseSite 把任务从运行时任务表中移除，释放该站点。
func (s *CrawlService) releaseSite(task *crawlTask) {
	s.mu.Lock()
	if cur, ok := s.bySource[task.sourceID]; ok && cur == task.job.ID {
		delete(s.bySource, task.sourceID)
	}
	delete(s.tasks, task.job.ID)
	s.mu.Unlock()
}

// StartTask 异步发起一个采集任务：立即返回任务视图，实际抓取在后台排队执行。
func (s *CrawlService) StartTask(sourceID uint, mode string) (*TaskView, error) {
	task, src, err := s.begin(sourceID, mode)
	if err != nil {
		return nil, err
	}
	go func() {
		_, _ = s.runTask(context.Background(), task, src)
	}()
	view := task.snapshot()
	return &view, nil
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
	return s.StartTask(job.SourceID, job.Mode)
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
