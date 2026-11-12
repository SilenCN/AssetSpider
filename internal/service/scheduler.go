package service

import (
	"log"
	"strings"
	"sync"
	"time"

	"github.com/SilenCN/AssetSpider/internal/repository"
	"github.com/SilenCN/AssetSpider/internal/utils"
)

// ScheduledCrawler 是调度器需要的采集能力。用接口而不是具体 *CrawlService，
// 便于单测注入假的触发器，也避免调度器触碰任务内部状态。
type ScheduledCrawler interface {
	HasActiveTask(sourceID uint) bool
	StartScheduled(sourceID uint) error
}

// SubscriptionRefresher 是调度器需要的订阅刷新能力。同样用接口注入，
// 调度器不必知道订阅是怎么拉取、怎么落库的。
type SubscriptionRefresher interface {
	RefreshDue(now time.Time) int
}

// ScheduleService 按分钟检查各站点的 cron 配置，到点自动发起增量采集；
// 同时顺带检查站点配置订阅是否到期（订阅间隔以小时计，分钟级检查足够细）。
//
// 设计取舍：
//   - 只在「当前分钟」判断命中，进程停机期间错过的时刻不补跑（补跑容易在重启时
//     一次性灌进一堆任务）；站点上的 schedule_last_at 用来保证同一分钟不重复触发，
//     即使进程在这一分钟内重启也不会重复。
//   - 站点已有排队 / 运行中的任务时跳过本次，不为定时任务排队堆积。
//   - 调度器是系统行为，不经过登录主体的可见性校验；但只有启用且配置了 cron
//     的站点才会被扫描。
type ScheduleService struct {
	sources repository.SourceRepository
	crawl   ScheduledCrawler
	subs    SubscriptionRefresher // 可选：未注入时不检查订阅

	startOnce sync.Once
	stopOnce  sync.Once
	stop      chan struct{}
	wg        sync.WaitGroup
}

func NewScheduleService(sources repository.SourceRepository, crawl ScheduledCrawler) *ScheduleService {
	return &ScheduleService{sources: sources, crawl: crawl, stop: make(chan struct{})}
}

// AttachSubscriptions 挂上订阅刷新能力（可选）。与采集不同，订阅刷新放在
// 每分钟检查的末尾：即使某个订阅地址很慢，也不会耽误到点的采集触发。
func (s *ScheduleService) AttachSubscriptions(subs SubscriptionRefresher) {
	s.subs = subs
}

// Start 启动后台检查循环（幂等）。先等到下一个整分钟，之后每分钟检查一次，
// 这样 cron 的分钟粒度与检查节奏一致，也不会在启动瞬间误触发。
func (s *ScheduleService) Start() {
	s.startOnce.Do(func() {
		s.wg.Add(1)
		go s.loop()
	})
}

// Stop 停止后台循环并等待其退出（幂等）。
func (s *ScheduleService) Stop() {
	s.stopOnce.Do(func() { close(s.stop) })
	s.wg.Wait()
}

func (s *ScheduleService) loop() {
	defer s.wg.Done()
	wait := time.Until(time.Now().Truncate(time.Minute).Add(time.Minute))
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-s.stop:
		return
	case <-timer.C:
	}
	s.runOnce(time.Now())

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			s.runOnce(time.Now())
		}
	}
}

// runOnce 检查 now 所在分钟内所有应触发的站点，返回实际发起的任务数。
// 抽成可传时刻的函数，单测无需真的等待一分钟。
func (s *ScheduleService) runOnce(now time.Time) int {
	srcs, err := s.sources.ListSources()
	if err != nil {
		log.Printf("定时调度读取站点失败: %v", err)
		return 0
	}
	minute := now.Truncate(time.Minute)
	triggered := 0
	for _, src := range srcs {
		cron := strings.TrimSpace(src.ScheduleCron)
		if !src.Enabled || !src.ScheduleEnabled || cron == "" {
			continue
		}
		spec, err := utils.ParseCron(cron)
		if err != nil {
			log.Printf("站点 #%d 定时表达式 %q 非法，已跳过: %v", src.ID, cron, err)
			continue
		}
		if !spec.Match(minute) {
			continue
		}
		// 同一分钟已触发过（含进程重启后的重复检查）：不再发起。
		if src.ScheduleLastAt != nil && src.ScheduleLastAt.Truncate(time.Minute).Equal(minute) {
			continue
		}
		if s.crawl.HasActiveTask(src.ID) {
			continue
		}
		// 先落触发时刻再发起：即使任务启动失败或进程立刻重启，也不会重复触发。
		if err := s.sources.UpdateScheduleLastAt(src.ID, minute); err != nil {
			log.Printf("回写站点 #%d 定时触发时间失败: %v", src.ID, err)
			continue
		}
		if err := s.crawl.StartScheduled(src.ID); err != nil {
			log.Printf("站点 #%d 定时增量采集启动失败: %v", src.ID, err)
			continue
		}
		log.Printf("站点 #%d（%s）定时增量采集已触发（%s）", src.ID, src.Name, cron)
		triggered++
	}
	if s.subs != nil {
		s.subs.RefreshDue(now)
	}
	return triggered
}
