// Package collector 是 service 层内部的采集协议客户端，实现苹果CMS V10
// Provide 接口（api.php/provide/vod/?ac=videolist）的 JSON 抓取与零丢失解析。
// 它不访问数据库，也不感知 HTTP 服务端。
package collector

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/SilenCN/AssetSpider/internal/utils"
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
// 因此一次解析保留两份数据：
//   - RawFields：每个键的原始 JSON 值（数字/对象/数组保持原样），用于 Extra 兜底；
//   - Fields：每个键统一字符串化后的值，用于映射到实体列与计算内容指纹。
type VideoItem struct {
	RawFields map[string]json.RawMessage
	Fields    map[string]string
}

// UnmarshalJSON 在解析单条记录时保留全部原始键值。
func (it *VideoItem) UnmarshalJSON(b []byte) error {
	it.RawFields = map[string]json.RawMessage{}
	// list 中偶尔会混入 null 或非对象项，此时视为空记录而不是让整页解析失败。
	if err := json.Unmarshal(b, &it.RawFields); err != nil {
		it.RawFields = map[string]json.RawMessage{}
	}
	it.Fields = make(map[string]string, len(it.RawFields))
	for k, v := range it.RawFields {
		it.Fields[k] = utils.StringifyJSONValue(v)
	}
	it.normalizeFields()
	return nil
}

// Get 返回某个字段的字符串值，字段不存在时返回空字符串。
func (it VideoItem) Get(key string) string { return it.Fields[key] }

// fieldAliases 把资源站常见的字段别名映射到苹果CMS 标准键。
//
// 真实资源站差异很大：海洋CMS 系用 v_* / v_playdata，部分二次开发模板用
// vod_playurl / play_list / playlist，还有站点直接返回结构化播放数组。
// 只在标准键「缺失或为空」时补齐，接口显式给出的标准值永远优先；
// 原始别名键仍留在 RawFields / Fields 里，因此 Extra 依旧能溯源到原始响应。
var fieldAliases = map[string][]string{
	"vod_id":        {"id", "v_id", "vodid", "vodId"},
	"vod_name":      {"name", "v_name", "vodname"},
	"vod_time":      {"v_time", "vodtime", "vod_addtime", "addtime", "update_time"},
	"vod_pic":       {"v_pic", "vodpic", "pic"},
	"vod_content":   {"v_content", "vodcontent", "content", "desc"},
	"type_id":       {"tid", "typeid", "v_type"},
	"type_name":     {"typename", "v_type_name"},
	"vod_play_from": {"vod_playfrom", "v_playfrom", "play_from", "playfrom"},
	"vod_play_url":  {"vod_playurl", "vod_url", "v_playdata", "play_url", "playurl", "vod_playurls"},
	"vod_play_list": {"vod_playlist", "play_list", "playlist", "vod_play_url_list"},
}

// normalizeFields 补齐字段别名，让后续「按标准键映射实体列」的逻辑能识别
// 非标准写法。放在 UnmarshalJSON 内一次性完成，避免每个调用方重复处理。
func (it *VideoItem) normalizeFields() {
	for std, aliases := range fieldAliases {
		if strings.TrimSpace(it.Fields[std]) != "" {
			continue
		}
		for _, alias := range aliases {
			if v := strings.TrimSpace(it.Fields[alias]); v != "" {
				it.Fields[std] = v
				break
			}
		}
	}
}

// ListResponse 对应 api.php/provide/vod/?ac=videolist 的 JSON 响应。
//
// 苹果CMS V10 的 Provide 接口约定：code == 1 表示成功；pagecount 为总分页数；
// list 为影片数组，ac=videolist 时包含 vod_play_from / vod_play_url 等完整字段。
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
	// 苹果CMS 约定 code != 1 为失败（如 -1 参数错误、-2 暂无数据、0 未授权）。
	if r.Code != 1 {
		return nil, fmt.Errorf("采集接口返回非成功状态 code=%d msg=%s", r.Code, r.Msg.String())
	}
	return &r, nil
}
