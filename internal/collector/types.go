// Package collector 实现海洋CMS / 苹果CMS 系视频站点的 JSON 采集。
package collector

import (
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
type VideoItem struct {
	VodID       flexString `json:"vod_id"`
	VodName     flexString `json:"vod_name"`
	TypeID      flexString `json:"type_id"`
	TypeName    flexString `json:"type_name"`
	VodEn       flexString `json:"vod_en"`
	VodTime     flexString `json:"vod_time"`
	VodRemarks  flexString `json:"vod_remarks"`
	VodPlayFrom flexString `json:"vod_play_from"`
	VodPic      flexString `json:"vod_pic"`
	VodArea     flexString `json:"vod_area"`
	VodLang     flexString `json:"vod_lang"`
	VodYear     flexString `json:"vod_year"`
	VodSerial   flexString `json:"vod_serial"`
	VodActor    flexString `json:"vod_actor"`
	VodDirector flexString `json:"vod_director"`
	VodContent  flexString `json:"vod_content"`
	VodPlayURL  flexString `json:"vod_play_url"`
	VodDownURL  flexString `json:"vod_down_url"`
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
