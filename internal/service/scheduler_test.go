package service

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

type fakeScheduledCrawler struct {
	mu      sync.Mutex
	started []uint
	active  map[uint]bool
	err     error
}

func (f *fakeScheduledCrawler) HasActiveTask(id uint) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active[id]
}

func (f *fakeScheduledCrawler) StartScheduled(id uint) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.started = append(f.started, id)
	return nil
}

func (f *fakeScheduledCrawler) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.started)
}

func newScheduleTestStore(t *testing.T) *repository.Repositories {
	t.Helper()
	st, err := repository.Open(filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func addScheduledSource(t *testing.T, st *repository.Repositories, name, cron string, enabled, scheduled bool) uint {
	t.Helper()
	src := &model.Source{
		Name: name, APIBase: "https://example.com/api.php/provide/vod/",
		Format: model.FormatMacCMSJSON, Enabled: enabled,
		ScheduleEnabled: scheduled, ScheduleCron: cron,
	}
	if err := st.Sources.CreateSource(src); err != nil {
		t.Fatal(err)
	}
	return src.ID
}

func TestScheduleRunOnceOnlyTriggersDueSources(t *testing.T) {
	st := newScheduleTestStore(t)
	due := addScheduledSource(t, st, "到点站", "0 3 * * *", true, true)
	addScheduledSource(t, st, "未到点站", "0 4 * * *", true, true)
	addScheduledSource(t, st, "未开启定时", "0 3 * * *", true, false)
	addScheduledSource(t, st, "停用站", "0 3 * * *", false, true)
	addScheduledSource(t, st, "非法表达式", "not a cron", true, true)

	fake := &fakeScheduledCrawler{active: map[uint]bool{}}
	sched := NewScheduleService(st.Sources, fake)
	at := time.Date(2026, 11, 10, 3, 0, 30, 0, time.Local)
	if got := sched.runOnce(at); got != 1 {
		t.Fatalf("本分钟应只触发 1 个站点，实际 %d", got)
	}
	if fake.count() != 1 || fake.started[0] != due {
		t.Fatalf("触发了错误的站点: %+v", fake.started)
	}
	src, err := st.Sources.GetSource(due)
	if err != nil {
		t.Fatal(err)
	}
	if src.ScheduleLastAt == nil || !src.ScheduleLastAt.Truncate(time.Minute).Equal(at.Truncate(time.Minute)) {
		t.Fatalf("应回写本次触发的分钟: %+v", src.ScheduleLastAt)
	}
}

func TestScheduleRunOnceDedupeAndActiveTask(t *testing.T) {
	st := newScheduleTestStore(t)
	id := addScheduledSource(t, st, "每分钟", "* * * * *", true, true)
	fake := &fakeScheduledCrawler{active: map[uint]bool{}}
	sched := NewScheduleService(st.Sources, fake)
	minute := time.Date(2026, 11, 10, 3, 0, 10, 0, time.Local)

	if got := sched.runOnce(minute); got != 1 {
		t.Fatalf("首次应触发 1 次，实际 %d", got)
	}
	// 同一分钟再次检查（例如进程重启）不应重复触发。
	if got := sched.runOnce(minute.Add(30 * time.Second)); got != 0 {
		t.Fatalf("同一分钟不应重复触发，实际 %d", got)
	}
	// 下一分钟已有任务在跑：跳过。
	fake.mu.Lock()
	fake.active[id] = true
	fake.mu.Unlock()
	next := minute.Add(time.Minute)
	if got := sched.runOnce(next); got != 0 {
		t.Fatalf("已有任务时应跳过，实际 %d", got)
	}
	// 任务结束后下一次到点照常触发。
	fake.mu.Lock()
	fake.active[id] = false
	fake.mu.Unlock()
	if got := sched.runOnce(next.Add(time.Minute)); got != 1 {
		t.Fatalf("任务结束后应恢复触发，实际 %d", got)
	}
	if fake.count() != 2 {
		t.Fatalf("累计应触发 2 次，实际 %d", fake.count())
	}
}

func TestScheduleServiceStartStopIdempotent(t *testing.T) {
	st := newScheduleTestStore(t)
	sched := NewScheduleService(st.Sources, &fakeScheduledCrawler{active: map[uint]bool{}})
	sched.Start()
	sched.Start()
	sched.Stop()
	sched.Stop()
}
