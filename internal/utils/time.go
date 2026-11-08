package utils

import (
	"strconv"
	"strings"
	"time"
)

// NormalizeTime 把站点时间统一为可字典序比较的本地时间字符串。
func NormalizeTime(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05", "2006-01-02T15:04:05",
		"2006-01-02 15:04", "2006-01-02",
	} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.Format("2006-01-02 15:04:05")
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 1e12 { // 毫秒时间戳
			n /= 1000
		}
		return time.Unix(n, 0).Local().Format("2006-01-02 15:04:05")
	}
	return s
}
