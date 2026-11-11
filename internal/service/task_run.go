package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
)

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
	task := newCrawlTask(job, 1+s.collectorCfg().RetryTimes)
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
	interval := time.Duration(s.collectorCfg().ProgressSaveSeconds) * time.Second
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
	base := s.collectorCfg().RetryBackoffSeconds
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
