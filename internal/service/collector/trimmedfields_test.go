package collector

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
)

// TestTrimmedFieldsGoToExtra 验证字段裁剪不丢数据：被移出 VodFields 的字段
// 采集时落入 Extra（前端「扩展字段」页签可溯源）、不再被识别为标准字段，
// 而保留字段仍是结构化列。锁死「裁剪 = 降级到扩展字段而非丢弃」这一承诺。
func TestTrimmedFieldsGoToExtra(t *testing.T) {
	raw := `{
		"code":1,"list":[{
		"vod_id":"1","vod_name":"测试","type_id":6,"type_name":"动作片",
		"vod_writer":"保留字段-编剧","vod_douban_id":"保留-豆瓣",
		"vod_level":"裁掉-级别","vod_status":"裁掉-状态","vod_pwd":"裁掉-密码",
		"vod_pwd_down_url":"裁掉-密码链接","vod_share_count":"裁掉-分享数",
		"vod_custom_flag":"自定义"
		}]}`
	resp, err := ParseList([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	v := ToVideo(1, resp.List[0])

	// 保留字段仍结构化。
	if v.VodWriter != "保留字段-编剧" || v.VodDoubanID != "保留-豆瓣" {
		t.Errorf("保留字段未结构化: writer=%s douban=%s", v.VodWriter, v.VodDoubanID)
	}
	// 被裁字段应进 Extra。
	var extra map[string]interface{}
	if err := json.Unmarshal([]byte(v.Extra), &extra); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"vod_level", "vod_status", "vod_pwd", "vod_pwd_down_url", "vod_share_count"} {
		if _, ok := extra[k]; !ok {
			t.Errorf("被裁字段 %s 未落入 Extra（数据丢失）: extra=%s", k, v.Extra)
		}
		if model.IsVodField(k) {
			t.Errorf("被裁字段 %s 不应再被识别为标准字段", k)
		}
	}
	if !strings.Contains(v.Extra, "vod_custom_flag") {
		t.Errorf("自定义字段丢失: %s", v.Extra)
	}
	t.Logf("Extra 内容: %s", v.Extra)
}
