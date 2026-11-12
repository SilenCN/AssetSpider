package service

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// MaxSourceBundleBytes 是站点配置 JSON 的大小上限。
//
// 一份配置大约 300 字节，4 MiB 能容纳上万条；再大基本是拿错文件（例如整站导出
// 的影片数据），尽早拒绝比解析到一半再失败更省事。
const MaxSourceBundleBytes = 4 << 20

// DecodeSourceBundle 解析站点配置 JSON，支持三种形态：
//   - 导出文件：{"version":1,"sources":[...]}
//   - 裸数组：[{...},{...}]
//   - 单条对象：{...}
//
// 文件导入与网络订阅共用这套解析，避免两处各写一遍、行为逐渐分叉。
func DecodeSourceBundle(body []byte) ([]SourceInput, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, fmt.Errorf("内容为空，应为站点配置 JSON")
	}
	if len(body) > MaxSourceBundleBytes {
		return nil, fmt.Errorf("内容过大（上限 %d MB）", MaxSourceBundleBytes>>20)
	}
	if body[0] == '{' {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(body, &probe); err != nil {
			return nil, fmt.Errorf("JSON 解析失败：%v", err)
		}
		if raw, ok := probe["sources"]; ok {
			var items []SourceInput
			if err := json.Unmarshal(raw, &items); err != nil {
				return nil, fmt.Errorf("sources 字段不是合法的站点数组：%v", err)
			}
			return items, nil
		}
		// 也允许直接贴一条站点对象。
		var one SourceInput
		if err := json.Unmarshal(body, &one); err != nil {
			return nil, fmt.Errorf("JSON 解析失败：%v", err)
		}
		return []SourceInput{one}, nil
	}
	var items []SourceInput
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("JSON 解析失败（应为站点数组、单条对象或含 sources 的导出文件）：%v", err)
	}
	return items, nil
}
