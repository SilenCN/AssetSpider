package utils

import (
	"testing"
	"time"
)

func TestParseCronValid(t *testing.T) {
	cases := []string{
		"* * * * *",
		"*/30 * * * *",
		"0 3 * * *",
		"0 3 * * 1-5",
		"15,45 2-4 1,15 * 0,6",
		"0 0 1 1 *",
		"0 0 * * 7",
	}
	for _, spec := range cases {
		if _, err := ParseCron(spec); err != nil {
			t.Errorf("合法表达式 %q 解析失败: %v", spec, err)
		}
	}
}

func TestParseCronInvalid(t *testing.T) {
	cases := []string{
		"", "* * * *", "* * * * * *",
		"60 * * * *", "* 24 * * *", "* * 0 * *", "* * * 13 *", "* * * * 8",
		"*/0 * * * *", "a * * * *", "5-2 * * * *", "1,,2 * * * *",
	}
	for _, spec := range cases {
		if _, err := ParseCron(spec); err == nil {
			t.Errorf("非法表达式 %q 应解析失败", spec)
		}
	}
}

func TestCronMatch(t *testing.T) {
	loc := time.Local
	at := func(y, m, d, h, mi int) time.Time {
		return time.Date(y, time.Month(m), d, h, mi, 0, 0, loc)
	}
	cases := []struct {
		spec string
		at   time.Time
		want bool
	}{
		{"* * * * *", at(2026, 11, 10, 20, 3), true},
		{"*/30 * * * *", at(2026, 11, 10, 20, 30), true},
		{"*/30 * * * *", at(2026, 11, 10, 20, 31), false},
		{"0 3 * * *", at(2026, 11, 10, 3, 0), true},
		{"0 3 * * *", at(2026, 11, 10, 3, 1), false},
		// 2026-11-10 是周二：工作日表达式命中
		{"0 3 * * 1-5", at(2026, 11, 10, 3, 0), true},
		{"0 3 * * 1-5", at(2026, 11, 14, 3, 0), false}, // 周六
		{"15,45 2-4 * * *", at(2026, 11, 10, 2, 45), true},
		{"15,45 2-4 * * *", at(2026, 11, 10, 5, 15), false},
		// dom 与 dow 同时限定时取并集：1 号（任意周）或每周一都命中
		{"0 0 1 * 1", at(2026, 12, 1, 0, 0), true},
		{"0 0 1 * 1", at(2026, 11, 9, 0, 0), true},
		// 7 = 周日
		{"0 0 * * 7", at(2026, 11, 15, 0, 0), true},
		{"0 0 * * 7", at(2026, 11, 10, 0, 0), false},
	}
	for _, c := range cases {
		spec, err := ParseCron(c.spec)
		if err != nil {
			t.Fatalf("解析 %q: %v", c.spec, err)
		}
		if got := spec.Match(c.at); got != c.want {
			t.Errorf("%q @ %s = %v，期望 %v", c.spec, c.at.Format(time.RFC3339), got, c.want)
		}
	}
}

func TestCronNext(t *testing.T) {
	cases := []struct {
		spec, after, want string
	}{
		{"0 3 * * *", "2026-11-10T20:00:00+08:00", "2026-11-11T03:00:00+08:00"},
		{"0 3 * * *", "2026-11-10T02:59:00+08:00", "2026-11-10T03:00:00+08:00"},
		{"*/30 * * * *", "2026-11-10T20:01:00+08:00", "2026-11-10T20:30:00+08:00"},
		{"0 0 1 1 *", "2026-11-10T20:00:00+08:00", "2027-01-01T00:00:00+08:00"},
	}
	loc := time.FixedZone("CST", 8*3600)
	for _, c := range cases {
		spec, err := ParseCron(c.spec)
		if err != nil {
			t.Fatal(err)
		}
		after, _ := time.ParseInLocation("2006-01-02T15:04:05Z07:00", c.after, loc)
		want, _ := time.ParseInLocation("2006-01-02T15:04:05Z07:00", c.want, loc)
		got := spec.Next(after)
		if !got.Equal(want) {
			t.Errorf("%q after %s: Next = %s，期望 %s", c.spec, c.after, got.Format(time.RFC3339), want.Format(time.RFC3339))
		}
		if !spec.Match(got) {
			t.Errorf("%q 的 Next 结果 %s 竟然不匹配", c.spec, got.Format(time.RFC3339))
		}
	}
}
