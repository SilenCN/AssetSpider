// Package collector 实现海洋CMS / 苹果CMS 系视频站点的 JSON 采集。
package collector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// flexString 兼容接口字段既可能是字符串也可能是数字的情况。
// 例如不同站点会返回 "vod_id":21 或 "vod_id":"21"。
type flexString string

func (s *flexString) UnmarshalJSON(b []byte) error {
	raw := strings.TrimSpace(string(b))
	switch {
	case raw == "" || raw == "null":
		*s = ""
		return nil
	case raw[0] == '"':
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*s = flexString(v)
		return nil
	default:
		// 数字与布尔值原样保留，避免 float64 精度丢失；整数去掉多余的小数部分。
		if n, err := json.Number(raw).Int64(); err == nil {
			*s = flexString(strconv.FormatInt(n, 10))
			return nil
		}
		*s = flexString(raw)
		return nil
	}
}

func (s flexString) String() string { return string(s) }

// flexInt 兼容分页字段的数字与字符串写法（如 "limit":"20"）。
type flexInt int

func (i *flexInt) UnmarshalJSON(b []byte) error {
	raw := strings.TrimSpace(string(b))
	if raw == "" || raw == "null" {
		*i = 0
		return nil
	}
	if raw[0] == '"' {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		raw = strings.TrimSpace(v)
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		if f, e := strconv.ParseFloat(raw, 64); e == nil {
			n = int(f)
		} else {
			n = 0
		}
	}
	*i = flexInt(n)
	return nil
}

func (i flexInt) Int() int { return int(i) }

// VideoItem 是采集接口 list[] 中的一条影片记录。
//
// 这里不使用固定结构体的 json.Unmarshal：那会静默丢弃接口返回的未定义字段，
// 而资源站字段因版本、二次开发、自定义模板差异极大，必须做到零丢失。
// 因此一次解析同时保留三份数据：
//   - RawJSON：该条记录原始 JSON 全文（紧凑化），可还原任意字段；
//   - RawFields：每个键的原始 JSON 值（数字/对象/数组保持原样），用于 Extra 兜底；
//   - Fields：每个键统一字符串化后的值，用于映射到实体列与计算内容指纹。
type VideoItem struct {
	RawJSON   string
	RawFields map[string]json.RawMessage
	Fields    map[string]string
}

// UnmarshalJSON 在解析单条记录时保留全部原始信息。
func (it *VideoItem) UnmarshalJSON(b []byte) error {
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return fmt.Errorf("紧凑化影片记录: %w", err)
	}
	it.RawJSON = buf.String()

	it.RawFields = map[string]json.RawMessage{}
	// list 中偶尔会混入 null 或非对象项，此时视为空记录而不是让整页解析失败。
	if err := json.Unmarshal(b, &it.RawFields); err != nil {
		it.RawFields = map[string]json.RawMessage{}
	}
	it.Fields = make(map[string]string, len(it.RawFields))
	for k, v := range it.RawFields {
		it.Fields[k] = stringifyValue(v)
	}
	return nil
}

// Get 返回某个字段的字符串值，字段不存在时返回空字符串。
func (it VideoItem) Get(key string) string { return it.Fields[key] }

// stringifyValue 把任意 JSON 值转换成可入库的字符串，且不丢失信息：
// 字符串解码、数字/布尔保留原文、null 转空串、对象/数组保留紧凑 JSON 文本。
func stringifyValue(raw json.RawMessage) string {
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

// ListResponse 对应 api.php/provide/vod/?ac=list 的 JSON 响应。
type ListResponse struct {
	Code      flexInt     `json:"code"`
	Msg       flexString  `json:"msg"`
	Page      flexInt     `json:"page"`
	PageCount flexInt     `json:"pagecount"`
	Limit     flexInt     `json:"limit"`
	Total     flexInt     `json:"total"`
	List      []VideoItem `json:"list"`
}

// ParseList 解析采集接口的 JSON 响应，兼容常见字段别名与类型差异。
func ParseList(data []byte) (*ListResponse, error) {
	var r ListResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("解析采集接口 JSON 失败: %w", err)
	}
	// 部分站点使用 page_count 而非 pagecount。
	if r.PageCount == 0 {
		var alias struct {
			PageCount flexInt `json:"page_count"`
		}
		if err := json.Unmarshal(data, &alias); err == nil {
			r.PageCount = alias.PageCount
		}
	}
	return &r, nil
}
