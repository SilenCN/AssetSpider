package service

import (
	"fmt"
	"log"
)

// RecoverInterrupted 在应用启动时自动恢复「因进程重启而中断」的采集任务。
//
// 任务调度状态只存在于进程内存：进程退出时还在 running / queued 的记录不可能
// 再推进，过去只把它们标记为中断等待人工重试。现在启动时会自动把它们复用原记录
// 重新入队，从保存的续采页（全量）/ 增量游标继续，统计与尝试次数接着累加。
//
// 边界：
//   - 只处理上次进程遗留的 running / queued 记录；用户手动暂停的 paused 任务不动。
//   - 先把它们标记为中断，即使某条恢复失败（站点已删、同站点冲突），页面也不会
//     一直显示「进行中」，用户仍可手动重试。
//   - 复用原记录，不新开 crawl_jobs；并发上限与同站点互斥由既有调度器保证。
//   - 恢复动作异步执行，不阻塞服务启动。
func (s *CrawlService) RecoverInterrupted() (int, error) {
	jobs, err := s.jobs.ListInterrupted()
	if err != nil {
		return 0, fmt.Errorf("查询中断任务失败: %w", err)
	}
	if len(jobs) == 0 {
		return 0, nil
	}

	if _, err := s.jobs.ResetInterrupted("服务重启，任务已中断（系统正自动从断点续采）"); err != nil {
		return 0, fmt.Errorf("标记中断任务失败: %w", err)
	}

	recovered := 0
	for i := range jobs {
		job := jobs[i]
		// 增量模式 ResumePage 通常为 1，游标由站点的 last_cursor 继续推进；
		// 全量模式则从保存的续采页码接着采。
		opt := startOptions{resumePage: job.ResumePage, reopen: &job}
		if _, err := s.startTask(job.SourceID, job.Mode, opt); err != nil {
			log.Printf("自动恢复采集任务 #%d 失败（可手动重试）: %v", job.ID, err)
			continue
		}
		recovered++
		log.Printf("自动恢复重启中断的采集任务 #%d（站点 %d，%s，从续采页 %d 继续）",
			job.ID, job.SourceID, job.Mode, job.ResumePage)
	}
	return recovered, nil
}
