package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
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

// startOptions 描述「从哪一页、以什么名义」发起任务。
type startOptions struct {
	resumePage int  // >1 时显式从该页续采（暂停/失败的任务重试时用）
	fromStart  bool // 强制从第 1 页开始（重新运行）

	// reopen 非空时复用这条采集记录，不新开任务：
	// 继续（暂停）与重试（失败）都在原记录上接着跑，统计与尝试次数继续累加。
	reopen *model.CrawlJob

	// reset 与 reopen 搭配使用（重新运行）：在原记录上从第 1 页重来，
	// 统计与尝试次数清零，记录 ID 不变。
	reset bool
}

// begin 校验参数、落一条 queued 任务记录并登记到运行时任务表。
//
// 同一站点在同一时刻只允许一个任务**运行**，但不再拒绝重复发起：后到的任务
// 以 queued 状态进入该站点的串行闸门排队，前一个任务结束（成功/暂停/失败）后
// 自动补位（见 runTask）。不同站点可并行，总量受全局并发上限约束。
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
		ResumePage: startPage, Attempt: 0,
	}
	reopening := opt.reopen != nil
	if reopening {
		// 复用原记录：统计值、尝试次数与续采页都沿用记录本身。
		job = opt.reopen
		if opt.reset {
			// 重新运行：同一个记录 ID 上从头再来，先清空上一轮的成果与尝试次数。
			job.Fetched, job.New, job.Updated = 0, 0, 0
			job.Unchanged, job.Pages, job.Total = 0, 0, 0
			job.Attempt = 0
		}
		job.ResumePage = startPage
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if reopening {
		if err := s.jobs.ReopenJob(job); err != nil {
			return nil, nil, fmt.Errorf("重置采集记录失败: %w", err)
		}
	} else if err := s.jobs.CreateJob(job); err != nil {
		return nil, nil, fmt.Errorf("创建采集记录失败: %w", err)
	}
	task := newCrawlTask(job, 1+s.cfg.RetryTimes)
	s.tasks[job.ID] = task
	return task, src, nil
}

// runTask 是任务的实际执行体：站点串行排队 → 全局并发排队 → 运行（失败则退避重试）→ 收尾。
func (s *CrawlService) runTask(ctx context.Context, task *crawlTask, src *model.Source) (*RunResult, error) {
	// 兜底清理：任何提前返回都不能把任务留在运行时任务表里（重复删除无害，
	// 正常路径由 settle 与站点闸门的 defer 更早完成）。
	defer s.releaseSite(task)

	runCtx, cancel := context.WithCancel(ctx)
	task.attachCancel(cancel)
	defer cancel()

	// 第一道：站点串行闸门。同一资源站同一时刻只允许一个任务运行，其余在这里
	// 排队（任务记录已是 queued），前一个任务收尾后把闸门移交给下一个。
	// 先于全局并发槽位获取：否则同站点的后续任务会白占一个全局槽位空等本站点。
	gate := s.sourceGate(task.sourceID)
	if err := gate.acquire(runCtx); err != nil {
		return s.abortQueued(task, err)
	}
	defer func() {
		// 顺序很重要：先把本任务从任务表里摘掉，再放闸门。否则下一个任务开跑时
		// 同站点会短暂出现两条「进行中」，前端可能把暂停按钮指到已结束的那条。
		s.releaseSite(task)
		gate.release()
	}()

	// 第二道：全局并发上限内的排队；排队期间同样可被暂停/取消打断。
	if err := s.limiter.acquire(runCtx); err != nil {
		return s.abortQueued(task, err)
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

	// 运行期间定期把进度写进采集记录，进程重启也不会丢掉已采的页。
	stopSaver := s.startProgressSaver(task)
	defer stopSaver()

	res := &RunResult{
		JobID: task.job.ID, SourceID: task.sourceID, SourceName: task.sourceName,
		Mode: task.mode, StartedAt: task.job.StartedAt, ResumePage: task.job.ResumePage,
		// 复用原记录重试时，统计从已累计值接着加，不把之前的成果清零。
		Fetched: task.job.Fetched, New: task.job.New, Updated: task.job.Updated,
		Unchanged: task.job.Unchanged, Pages: task.job.Pages, Total: task.job.Total,
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
	// 采集成功但接口没返回播放地址时，把结论直接写进记录的消息里。
	// 这是最常见的一类「播放页显示没有播放地址」根因：数据本身入库正常，
	// 只是接口走到了精简模式（没有 vod_play_url），用户看记录即可自助排查。
	if status == model.JobSuccess && res != nil {
		if res.AutoAC != "" {
			msg += fmt.Sprintf("；默认 ac=videolist 未返回播放地址，已自动改用 ac=%s 补齐播放数据", res.AutoAC)
		}
		if res.NoPlayURL > 0 {
			params := strings.ToLower(src.APIBase + " " + src.ExtraParams)
			if strings.Contains(params, "ac=list") {
				msg += fmt.Sprintf("；其中 %d 条记录接口未返回播放地址：站点地址/附加参数里带了 ac=list，该动作通常只返回分类或精简列表，请改用 ac=videolist 或 ac=detail 后重新采集", res.NoPlayURL)
			} else {
				msg += fmt.Sprintf("；其中 %d 条记录接口未返回播放地址字段，请直接访问采集接口确认响应里是否有 vod_play_url（或播放地址数组），必要时在附加参数里指定 ac=videolist / ac=detail", res.NoPlayURL)
			}
		}
	}
	// 收尾前先停掉定期落库，避免与终态写入交叉。
	stopSaver()
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

// startProgressSaver 启动「定期把运行进度写库」的协程，返回幂等的停止函数。
//
// 进程被强杀时内存里的进度会全部丢失，而 crawl_jobs 只在任务结束（成功/暂停/
// 失败）时才写终态。这里按 collector.progress_save_seconds（默认 60 秒）滚动
// 落库统计与续采页码，重启后 ResumePage 已指向下一页，重试即可接着采。
//
// 停止会等待正在进行的写库结束，保证不会与 settle 的终态写入交叉。
func (s *CrawlService) startProgressSaver(task *crawlTask) func() {
	interval := time.Duration(s.cfg.ProgressSaveSeconds) * time.Second
	if interval <= 0 {
		interval = time.Minute
	}
	stop := make(chan struct{})
	var once sync.Once
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				s.saveProgress(task)
				// 与进度快照同频刷新站点的影片总数：采集运行期间列表上的
				// 「影片数」也在一分钟粒度上跟着涨，不必等任务结束才更新。
				s.syncSourceVideoCount(task.sourceID)
			}
		}
	}()
	return func() {
		once.Do(func() { close(stop) })
		wg.Wait()
	}
}

// saveProgress 写一次进度快照；还没有完成任何分页时不必写。
func (s *CrawlService) saveProgress(task *crawlTask) {
	job := task.persistSnapshot()
	if job.Pages == 0 {
		return
	}
	if err := s.jobs.SaveProgress(job); err != nil {
		log.Printf("保存任务 #%d 进度失败: %v", job.ID, err)
	}
}

// syncSourceVideoCount 重新统计站点影片总数并回写到 sources.video_count。
//
// 采集运行中按进度落库间隔（默认 60 秒）调用，任务结束时再调用一次。
// 每次都重新 COUNT(*) 而不是在旧值上累加：采集会 upsert 既有影片（unchanged/updated），
// 累加会把「未新增」也算进去；重算则永远等于 videos 表的真实行数。
// 写库失败只记日志——统计显示不准确不应影响采集本身。
func (s *CrawlService) syncSourceVideoCount(sourceID uint) {
	n, err := s.videos.CountVideos(sourceID)
	if err != nil {
		log.Printf("统计站点 #%d 影片总数失败: %v", sourceID, err)
		return
	}
	if err := s.sources.UpdateVideoCount(sourceID, n); err != nil {
		log.Printf("回写站点 #%d 影片总数失败: %v", sourceID, err)
	}
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
			Fetched: task.job.Fetched, New: task.job.New, Updated: task.job.Updated,
			Unchanged: task.job.Unchanged, Pages: task.job.Pages, Total: task.job.Total,
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
	// 采集记录一旦写成终态就从运行时任务表里摘掉：否则「记录已结束、任务列表里
	// 还在运行」的窗口会随后面的站点状态回写一起被拉长，轮询容易读到自相矛盾的视图。
	// 这里是幂等的，runTask 的 defer 仍会兜底。
	s.releaseSite(task)

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
	// 任务结束后再校准一次影片总数：暂停/失败时也把已入库的部分如实反映到站点列表上。
	s.syncSourceVideoCount(job.SourceID)
	return res, nil
}

func (t *crawlTask) currentAttempt() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.attempt
}

// releaseSite 把任务从运行时任务表中移除。
//
// 站点级别的「同时只跑一个」由 runTask 里的站点闸门负责释放，与这里无关：
// 任务收尾（写终态）完成后，runTask 的 defer 才把闸门交给该站点的下一个任务。
func (s *CrawlService) releaseSite(task *crawlTask) {
	s.mu.Lock()
	delete(s.tasks, task.job.ID)
	s.mu.Unlock()
}

// sourceGate 返回某站点的串行闸门（容量恒为 1，按需创建）。
//
// 同一资源站同一时刻只允许一个采集任务：重复发起不再返回冲突，而是在闸门上
// 排队，前一个任务结束时把闸门移交给下一个（concurrencyLimiter 的 hand-off）。
// 闸门按站点常驻内存，数量与站点数相当，无需回收。
func (s *CrawlService) sourceGate(sourceID uint) *concurrencyLimiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	gate, ok := s.sourceGates[sourceID]
	if !ok {
		gate = newConcurrencyLimiter(1)
		s.sourceGates[sourceID] = gate
	}
	return gate
}

// abortQueued 收尾「还没真正开始采集就被取消/暂停」的任务。
// 与 runTask 的返回约定一致：被暂停时返回 ErrPaused，否则返回原始错误。
func (s *CrawlService) abortQueued(task *crawlTask, cause error) (*RunResult, error) {
	status, msg := model.JobFailed, "任务被取消："+cause.Error()
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
	return res, cause
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

// SetMaxConcurrent 运行时调整「同时运行的任务数」上限（重启后回到配置文件的值）。
func (s *CrawlService) SetMaxConcurrent(n int) (Capacity, error) {
	if n < 1 || n > 64 {
		return Capacity{}, invalid("max_concurrent 需在 1..64 之间")
	}
	s.limiter.setMax(n)
	return s.Capacity(), nil
}
