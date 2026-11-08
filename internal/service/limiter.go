package service

import (
	"context"
	"sync"
)

// concurrencyLimiter 是支持运行时调整上限的并发限流器。
//
// 采集任务用它在「同时运行的任务数」上排队：取到槽位才开始真正抓取，
// 取不到就在队列里等待，并允许被 context（暂停/取消）打断。
//
// 之所以不用带缓冲 channel：上限要在运行期通过接口调整，
// 换 channel 会丢失在途槽位；这里用「互斥量 + 等待者队列」把槽位直接
// 移交（hand-off）给等待者，active 计数不需要变动，调整上限也不会重复计数。
type concurrencyLimiter struct {
	mu      sync.Mutex
	max     int
	active  int
	waiters []chan struct{}
}

func newConcurrencyLimiter(max int) *concurrencyLimiter {
	if max < 1 {
		max = 1
	}
	return &concurrencyLimiter{max: max}
}

// acquire 获取一个并发槽位；排队期间 ctx 取消会返回 ctx.Err()。
func (l *concurrencyLimiter) acquire(ctx context.Context) error {
	l.mu.Lock()
	if l.active < l.max {
		l.active++
		l.mu.Unlock()
		return nil
	}
	ch := make(chan struct{}, 1)
	l.waiters = append(l.waiters, ch)
	l.mu.Unlock()

	select {
	case <-ctx.Done():
		l.mu.Lock()
		for i, w := range l.waiters {
			if w == ch {
				l.waiters = append(l.waiters[:i], l.waiters[i+1:]...)
				break
			}
		}
		l.mu.Unlock()
		return ctx.Err()
	case <-ch:
		return nil
	}
}

// release 释放槽位：优先移交给下一个等待者，没有等待者才减少 active。
func (l *concurrencyLimiter) release() {
	l.mu.Lock()
	if len(l.waiters) > 0 {
		ch := l.waiters[0]
		l.waiters = l.waiters[1:]
		l.mu.Unlock()
		ch <- struct{}{}
		return
	}
	if l.active > 0 {
		l.active--
	}
	l.mu.Unlock()
}

// setMax 调整上限；调大时立即唤醒相应数量的等待者。
func (l *concurrencyLimiter) setMax(n int) {
	if n < 1 {
		n = 1
	}
	l.mu.Lock()
	l.max = n
	wake := make([]chan struct{}, 0)
	for l.active < l.max && len(l.waiters) > 0 {
		wake = append(wake, l.waiters[0])
		l.waiters = l.waiters[1:]
		l.active++
	}
	l.mu.Unlock()
	for _, ch := range wake {
		ch <- struct{}{}
	}
}

// state 返回当前运行数、上限与排队数。
func (l *concurrencyLimiter) state() (active, max, queued int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.active, l.max, len(l.waiters)
}
