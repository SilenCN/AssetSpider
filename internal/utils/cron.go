// Package utils 里的 cron 实现只覆盖站点定时采集需要的 5 段表达式：
// 分 时 日 月 周，支持 *、*/n、n、a-b 与逗号列表。
package utils

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CronSpec 是一条已解析的 5 段 cron 表达式。
// 各字段用位图保存允许值，Match 只做常数次查表。
type CronSpec struct {
	Spec                          string
	minute, hour, dom, month, dow [64]bool
	domAny, dowAny                bool // 字段里写了 *：cron 的 day-of-month / day-of-week 同时限定时取并集
}

type cronField struct {
	min, max int
}

var cronFields = [5]cronField{
	{0, 59}, // 分
	{0, 23}, // 时
	{1, 31}, // 日
	{1, 12}, // 月
	{0, 6},  // 周：0=周日
}

// ParseCron 解析 5 段 cron 表达式。周字段同时接受 7（=周日）。
func ParseCron(spec string) (*CronSpec, error) {
	parts := strings.Fields(strings.TrimSpace(spec))
	if len(parts) != 5 {
		return nil, fmt.Errorf("cron 表达式需为 5 段（分 时 日 月 周），实际 %d 段", len(parts))
	}
	c := &CronSpec{Spec: strings.Join(parts, " ")}
	buckets := [5]*[64]bool{&c.minute, &c.hour, &c.dom, &c.month, &c.dow}
	for i, part := range parts {
		f := cronFields[i]
		bits := buckets[i]
		any := false
		for _, item := range strings.Split(part, ",") {
			if item == "" {
				return nil, fmt.Errorf("cron 第 %d 段 %q 含空项", i+1, part)
			}
			step := 1
			body := item
			if strings.Contains(item, "/") {
				kv := strings.Split(item, "/")
				if len(kv) != 2 || kv[1] == "" {
					return nil, fmt.Errorf("cron 第 %d 段 %q 步长写法非法", i+1, item)
				}
				n, err := strconv.Atoi(kv[1])
				if err != nil || n < 1 {
					return nil, fmt.Errorf("cron 第 %d 段 %q 步长需为正整数", i+1, item)
				}
				body, step = kv[0], n
			}
			lo, hi := f.min, f.max
			switch {
			case body == "*":
				any = step == 1 // 只有纯 * 才是「不限定」，*/n 仍是限定字段
			case strings.Contains(body, "-"):
				kv := strings.Split(body, "-")
				if len(kv) != 2 {
					return nil, fmt.Errorf("cron 第 %d 段 %q 区间写法非法", i+1, item)
				}
				var err error
				if lo, err = parseCronValue(kv[0], f, i); err != nil {
					return nil, err
				}
				if hi, err = parseCronValue(kv[1], f, i); err != nil {
					return nil, err
				}
				if lo > hi {
					return nil, fmt.Errorf("cron 第 %d 段 %q 区间起点大于终点", i+1, item)
				}
			default:
				v, err := parseCronValue(body, f, i)
				if err != nil {
					return nil, err
				}
				if strings.Contains(item, "/") {
					lo, hi = v, f.max
				} else {
					lo, hi = v, v
				}
			}
			for v := lo; v <= hi; v += step {
				if i == 4 && v == 7 {
					bits[0] = true // 周字段 7 与 0 都表示周日
					continue
				}
				if v >= f.min && v <= f.max {
					bits[v] = true
				}
			}
		}
		if i == 2 {
			c.domAny = any
		}
		if i == 4 {
			c.dowAny = any
		}
	}
	return c, nil
}

func parseCronValue(raw string, f cronField, idx int) (int, error) {
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("cron 第 %d 段 %q 不是数字", idx+1, raw)
	}
	// 周字段允许 7 表示周日
	if idx == 4 && v == 7 {
		return 7, nil
	}
	if v < f.min || v > f.max {
		return 0, fmt.Errorf("cron 第 %d 段 %d 超出范围 %d..%d", idx+1, v, f.min, f.max)
	}
	return v, nil
}

// Match 报告 t（本地时区）所在的分钟是否命中表达式。
func (c *CronSpec) Match(t time.Time) bool {
	if !c.minute[t.Minute()] || !c.hour[t.Hour()] || !c.month[int(t.Month())] {
		return false
	}
	day := t.Day()
	week := int(t.Weekday())
	domOK := c.dom[day]
	dowOK := c.dow[week]
	// 标准 cron 语义：日/周都限定时命中任一即可；只限定一个时按该字段判断。
	switch {
	case !c.domAny && !c.dowAny:
		return domOK || dowOK
	case !c.domAny:
		return domOK
	case !c.dowAny:
		return dowOK
	default:
		return true
	}
}

// Next 返回 after 之后第一个命中的分钟（截断到分钟）；两年内无匹配返回零值。
func (c *CronSpec) Next(after time.Time) time.Time {
	cur := after.Truncate(time.Minute).Add(time.Minute)
	// 按「天」跳跃：不匹配的日期直接跳到次日零点，避免逐分钟扫描一整年。
	day := time.Date(cur.Year(), cur.Month(), cur.Day(), 0, 0, 0, 0, cur.Location())
	for d := 0; d < 366*2; d++ {
		if !c.month[int(day.Month())] {
			day = day.AddDate(0, 0, 1)
			continue
		}
		if !c.dayMatches(day) {
			day = day.AddDate(0, 0, 1)
			continue
		}
		for h := 0; h < 24; h++ {
			if !c.hour[h] {
				continue
			}
			for m := 0; m < 60; m++ {
				if !c.minute[m] {
					continue
				}
				candidate := time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, day.Location())
				if !candidate.After(after) {
					continue
				}
				return candidate
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	return time.Time{}
}

func (c *CronSpec) dayMatches(day time.Time) bool {
	domOK, dowOK := c.dom[day.Day()], c.dow[int(day.Weekday())]
	switch {
	case !c.domAny && !c.dowAny:
		return domOK || dowOK
	case !c.domAny:
		return domOK
	case !c.dowAny:
		return dowOK
	default:
		return true
	}
}
