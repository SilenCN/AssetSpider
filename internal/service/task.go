package service

import (
	"context"
	"errors"
	"sort"
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

// startTask 登记并异步执行一个任务：立即返回任务视图，实际抓取在后台排队执行。
func (s *CrawlService) startTask(sourceID uint, mode string, opt startOptions) (*TaskView, error) {
	task, src, err := s.begin(sourceID, mode, opt)
	if err != nil {
		return nil, err
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		_, _ = s.runTask(context.Background(), task, src)
	}()
	view := task.snapshot()
	return &view, nil
}

// WaitForTasks 等待所有异步采集 goroutine 收尾，最长等待 timeout。
// 关闭数据库前先调用它，避免后台任务在连接已关闭后仍写库（既防止测试里的
// 临时目录清理竞态，也让重启时正在收尾的任务有机会把进度写完）。
// 超时返回 false，调用方仍会关闭数据库；未跑完的记录保持 running/queued，
// 下次启动时由 RecoverInterrupted 自动恢复。
func (s *CrawlService) WaitForTasks(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// StartTask 异步发起一个采集任务；非管理员只能采集自己可见的站点。
func (s *CrawlService) StartTask(sourceID uint, mode string, p ...Principal) (*TaskView, error) {
	if !principalOf(p...).CanSee(sourceID) {
		return nil, notFound("站点 %d 不存在或无权访问", sourceID)
	}
	return s.startTask(sourceID, mode, startOptions{})
}

// StartScheduled 由定时调度器调用：系统级发起一次增量采集，绕过登录主体校验
// （调度器没有用户上下文），但仍走完整的任务排队 / 断点 / 状态回写流程。
func (s *CrawlService) StartScheduled(sourceID uint) error {
	_, err := s.startTask(sourceID, model.ModeIncremental, startOptions{})
	return err
}

// PauseTask 手动暂停一个排队中或运行中的任务。
func (s *CrawlService) PauseTask(jobID uint, p ...Principal) (*TaskView, error) {
	s.mu.Lock()
	task, ok := s.tasks[jobID]
	s.mu.Unlock()
	if !ok || !principalOf(p...).CanSee(task.sourceID) {
		return nil, notFound("任务 %d 不存在或已结束", jobID)
	}
	task.requestPause()
	view := task.snapshot()
	return &view, nil
}

// ResumeTask 继续一个已暂停的任务：**复用原记录**，按原站点与原模式重新入队，
// 全量模式从暂停时的页码续采，增量模式从已保存的游标继续，统计与尝试次数继续累加。
func (s *CrawlService) ResumeTask(jobID uint, p ...Principal) (*TaskView, error) {
	actor := principalOf(p...)
	s.mu.Lock()
	if task, ok := s.tasks[jobID]; ok {
		s.mu.Unlock()
		if !actor.CanSee(task.sourceID) {
			return nil, notFound("任务 %d 不存在或已结束", jobID)
		}
		view := task.snapshot()
		return &view, nil // 还在跑，视为已恢复
	}
	s.mu.Unlock()

	job, err := s.jobs.GetJob(jobID)
	if err != nil {
		return nil, wrapRepo(err)
	}
	if !actor.CanSee(job.SourceID) {
		return nil, notFound("任务 %d 不存在或已结束", jobID)
	}
	if job.Status != model.JobPaused {
		return nil, conflict("任务 %d 当前状态为「%s」，只有已暂停的任务可以继续", jobID, job.Status)
	}
	startPage := job.ResumePage
	if startPage < 1 {
		startPage = 1
	}
	return s.startTask(job.SourceID, job.Mode,
		startOptions{resumePage: startPage, reopen: job})
}

// RetryTask 手动重试一个失败的任务：**复用原记录**，从失败页续采，
// 不新开任务，已完成的页面不会重采，拉取/新增/更新/未变等统计继续累加。
func (s *CrawlService) RetryTask(jobID uint, p ...Principal) (*TaskView, error) {
	job, err := s.jobs.GetJob(jobID)
	if err != nil {
		return nil, wrapRepo(err)
	}
	if !principalOf(p...).CanSee(job.SourceID) {
		return nil, notFound("任务 %d 不存在或已结束", jobID)
	}
	if job.Status != model.JobFailed {
		return nil, conflict("任务 %d 当前状态为「%s」，只有失败的任务可以重试", jobID, job.Status)
	}
	startPage := job.ResumePage
	if startPage < 1 {
		startPage = 1
	}
	return s.startTask(job.SourceID, job.Mode,
		startOptions{resumePage: startPage, reopen: job})
}

// RerunTask 重新运行一条已结束的任务：**复用原记录**，忽略断点从第 1 页重采，
// 上一轮的统计与尝试次数清零，任务 ID 不变（不会在记录列表里多出一条）。
func (s *CrawlService) RerunTask(jobID uint, p ...Principal) (*TaskView, error) {
	job, err := s.jobs.GetJob(jobID)
	if err != nil {
		return nil, wrapRepo(err)
	}
	if !principalOf(p...).CanSee(job.SourceID) {
		return nil, notFound("任务 %d 不存在或已结束", jobID)
	}
	if job.Status == model.JobRunning || job.Status == model.JobQueued {
		return nil, conflict("任务 %d 仍在进行中，请先暂停后再重新运行", jobID)
	}
	return s.startTask(job.SourceID, job.Mode,
		startOptions{fromStart: true, reopen: job, reset: true})
}

// HasActiveTask 报告该站点是否还有排队中 / 运行中的采集任务。
//
// 运行时任务表只保留这两类状态（暂停与结束的任务在收尾时就被摘除），
// 因此「存在记录」即等价于「仍在活动」。
func (s *CrawlService) HasActiveTask(sourceID uint) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, task := range s.tasks {
		if task.sourceID == sourceID {
			return true
		}
	}
	return false
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

// ListTasks 返回当前排队中/运行中/暂停中的任务及并发容量；
// 非管理员只能看到自己可见站点的任务。
func (s *CrawlService) ListTasks(p ...Principal) TaskList {
	s.mu.Lock()
	views := make([]TaskView, 0, len(s.tasks))
	for _, task := range s.tasks {
		views = append(views, task.snapshot())
	}
	s.mu.Unlock()
	actor := principalOf(p...)
	if !actor.IsAdmin {
		filtered := make([]TaskView, 0, len(views))
		for _, v := range views {
			if actor.CanSee(v.SourceID) {
				filtered = append(filtered, v)
			}
		}
		views = filtered
	}
	sort.Slice(views, func(i, j int) bool { return views[i].JobID < views[j].JobID })

	active, max, _ := s.limiter.state()
	return TaskList{
		Capacity: Capacity{MaxConcurrent: max, Running: active, Queued: s.queuedTasks()},
		Items:    views,
	}
}

// queuedTasks 汇总当前排队中的任务数：既包括等全局并发槽位的，也包括
// 同一站点等前一个任务结束的（站点串行闸门）。两者互斥——任务先过站点
// 闸门才会去抢全局槽位，因此不会重复计数。
func (s *CrawlService) queuedTasks() int {
	_, _, queued := s.limiter.state()

	s.mu.Lock()
	gates := make([]*concurrencyLimiter, 0, len(s.sourceGates))
	for _, gate := range s.sourceGates {
		gates = append(gates, gate)
	}
	s.mu.Unlock()

	// 在锁外读取各闸门的排队数，避免与任务表互斥量嵌套。
	for _, gate := range gates {
		if _, _, q := gate.state(); q > 0 {
			queued += q
		}
	}
	return queued
}

// Capacity 返回当前并发容量。
func (s *CrawlService) Capacity() Capacity {
	active, max, _ := s.limiter.state()
	return Capacity{MaxConcurrent: max, Running: active, Queued: s.queuedTasks()}
}

// SetMaxConcurrent 调整「同时运行的任务数」上限。
// 有运行时 store 时写入配置并持久化，与设置页共用同一份数据源；
// 无 store（测试 / 未接配置页）时仅调整内存限流器，保持旧行为。
func (s *CrawlService) SetMaxConcurrent(n int) (Capacity, error) {
	if n < 1 || n > 64 {
		return Capacity{}, invalid("max_concurrent 需在 1..64 之间")
	}
	if s.store != nil {
		in := s.store.Collector()
		in.MaxConcurrent = n
		if _, err := s.store.UpdateCollector(in); err != nil {
			return Capacity{}, invalid("%s", err.Error())
		}
	}
	s.limiter.setMax(n)
	return s.Capacity(), nil
}

// ApplyLive 在 Store 的配置被在线更新后调用，把并发上限同步到限流器。
// 其余配置（超时 / UA / 最大页数 / 重试 / 进度间隔 / 默认时间窗）由采集循环与
// Fetcher 直接读 Store，无需重启；这里只处理构造期就缓存了上限的限流器。
func (s *CrawlService) ApplyLive() {
	if s.store == nil {
		return
	}
	s.limiter.setMax(s.store.Collector().MaxConcurrent)
}
