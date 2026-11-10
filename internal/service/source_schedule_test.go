package service

import (
	"path/filepath"
	"strconv"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/repository"
)

func newSourceServiceForTest(t *testing.T) *SourceService {
	t.Helper()
	st, err := repository.Open(filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return NewSourceService(st.Sources)
}

func validSourceInput() SourceInput {
	enabled, scheduled := true, true
	return SourceInput{
		Name: "定时站", APIBase: "https://example.com/api.php/provide/vod/",
		Enabled: &enabled, ScheduleEnabled: &scheduled, ScheduleCron: "0 3 * * *",
	}
}

func TestSourceScheduleValidation(t *testing.T) {
	svc := newSourceServiceForTest(t)
	cases := []struct {
		name    string
		mutate  func(*SourceInput)
		wantErr bool
	}{
		{"合法表达式", func(*SourceInput) {}, false},
		{"非法表达式", func(in *SourceInput) { in.ScheduleCron = "0 99 * * *" }, true},
		{"段数不足", func(in *SourceInput) { in.ScheduleCron = "* * * *" }, true},
		{"启用却留空", func(in *SourceInput) { in.ScheduleCron = "" }, true},
		{"关闭定时且留空", func(in *SourceInput) {
			off := false
			in.ScheduleEnabled = &off
			in.ScheduleCron = ""
		}, false},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := validSourceInput()
			in.Name += "-" + strconv.Itoa(i) // 站点名唯一，避免上个用例留下的记录干扰
			c.mutate(&in)
			_, err := svc.Create(in)
			if (err != nil) != c.wantErr {
				t.Fatalf("错误判定不符: err=%v wantErr=%v", err, c.wantErr)
			}
		})
	}
}

// 老版本导出的 JSON 不带定时字段时，导入 / 局部 PUT 不应清掉已有的 cron，
// 否则会留下「已启用但没有表达式」的坏配置。
func TestSourceSchedulePreservedWhenFieldAbsent(t *testing.T) {
	svc := newSourceServiceForTest(t)
	created, err := svc.Create(validSourceInput())
	if err != nil {
		t.Fatal(err)
	}
	legacy := validSourceInput()
	legacy.Name = "定时站改名"
	legacy.ScheduleEnabled = nil
	legacy.ScheduleCron = ""
	updated, err := svc.Update(created.ID, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.ScheduleEnabled || updated.ScheduleCron != "0 3 * * *" {
		t.Fatalf("未带定时字段时不应改动原配置: %+v", updated)
	}
}

func TestSourceViewNextScheduleAt(t *testing.T) {
	svc := newSourceServiceForTest(t)
	if _, err := svc.Create(validSourceInput()); err != nil {
		t.Fatal(err)
	}
	views, err := svc.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].NextScheduleAt == nil {
		t.Fatalf("启用定时的站点应带下次触发时刻: %+v", views)
	}
	// 停用后不再给出下次触发时刻。
	in := validSourceInput()
	off := false
	in.Enabled = &off
	if _, err := svc.Update(views[0].ID, in); err != nil {
		t.Fatal(err)
	}
	views, _ = svc.List()
	if views[0].NextScheduleAt != nil {
		t.Fatalf("停用站点不应有下次触发时刻: %+v", views[0])
	}
}
