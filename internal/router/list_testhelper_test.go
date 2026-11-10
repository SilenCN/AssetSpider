package router_test

import (
	"encoding/json"
	"testing"
)

// listItemCount 从影片列表响应里取本页条数。
// 列表接口已不再返回 total / hasMore，测试断言改为核对 items 长度。
func listItemCount(t *testing.T, body []byte) int {
	t.Helper()
	var out struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("解析影片列表响应失败: %v | %s", err, body)
	}
	return len(out.Items)
}
