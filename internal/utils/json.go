package utils

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// StringifyJSONValue 把任意 JSON 值转换成可入库的字符串，且不丢失信息：
// 字符串解码、数字/布尔保留原文、null 转空串、对象/数组保留紧凑 JSON 文本。
func StringifyJSONValue(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return ""
	}
	switch s[0] {
	case '"':
		var v string
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
		return s
	case '{', '[':
		var buf bytes.Buffer
		if err := json.Compact(&buf, raw); err == nil {
			return buf.String()
		}
		return s
	default:
		if n, err := json.Number(s).Int64(); err == nil {
			return strconv.FormatInt(n, 10)
		}
		return s
	}
}
