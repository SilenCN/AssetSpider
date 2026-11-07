package model

import (
	"reflect"
	"strings"
	"testing"
)

// TestVodFieldsMatchVideoEntity 保证字段清单与 Video 实体严格一致。
// 采集映射与入库更新都由清单驱动，一旦两者脱节就会静默丢字段，因此用测试锁死。
func TestVodFieldsMatchVideoEntity(t *testing.T) {
	typ := reflect.TypeOf(Video{})
	groupSet := make(map[string]bool, len(VodGroupLabels))
	for _, g := range VodGroupLabels {
		groupSet[g.Key] = true
	}

	seenKey := map[string]bool{}
	seenGo := map[string]bool{}
	seenCol := map[string]bool{}
	for _, f := range VodFields {
		if seenKey[f.Key] {
			t.Errorf("重复的接口键: %s", f.Key)
		}
		if seenGo[f.Go] {
			t.Errorf("重复的 Go 字段: %s", f.Go)
		}
		if seenCol[f.Column] {
			t.Errorf("重复的列名: %s", f.Column)
		}
		seenKey[f.Key], seenGo[f.Go], seenCol[f.Column] = true, true, true

		sf, ok := typ.FieldByName(f.Go)
		if !ok {
			t.Errorf("Video 缺少字段 %s（接口键 %s）", f.Go, f.Key)
			continue
		}
		if sf.Type.Kind() != reflect.String {
			t.Errorf("字段 %s 应为 string，实际 %s", f.Go, sf.Type.Kind())
		}
		column := ""
		for _, part := range strings.Split(sf.Tag.Get("gorm"), ";") {
			if strings.HasPrefix(part, "column:") {
				column = strings.TrimPrefix(part, "column:")
			}
		}
		if column != f.Column {
			t.Errorf("字段 %s 的 gorm column=%q，清单为 %q", f.Go, column, f.Column)
		}
		if tag := sf.Tag.Get("json"); tag == "" || strings.Split(tag, ",")[0] == "" {
			t.Errorf("字段 %s 缺少 json tag", f.Go)
		}
		if !groupSet[f.Group] {
			t.Errorf("字段 %s 的分组 %q 未在 VodGroupLabels 中定义", f.Go, f.Group)
		}
	}
	if len(VodFields) < 90 {
		t.Errorf("标准字段数量异常（%d），疑似清单被裁剪", len(VodFields))
	}
}

func TestVodFieldMetasAndKnownKeys(t *testing.T) {
	metas := VodFieldMetas()
	if len(metas) != len(VodFields) {
		t.Fatalf("元数据数量 %d != 清单数量 %d", len(metas), len(VodFields))
	}
	typ := reflect.TypeOf(Video{})
	for i, m := range metas {
		if m.Key != VodFields[i].Key || m.Group != VodFields[i].Group || m.Long != VodFields[i].Long {
			t.Errorf("元数据与清单不一致: %+v", m)
		}
		if m.Label == "" || m.Desc == "" {
			t.Errorf("元数据缺少中文说明: %+v", m)
		}
		sf, _ := typ.FieldByName(VodFields[i].Go)
		want := strings.Split(sf.Tag.Get("json"), ",")[0]
		if m.Json != want {
			t.Errorf("字段 %s 前端键应为 %q，实际 %q", m.Key, want, m.Json)
		}
		if !IsVodField(m.Key) {
			t.Errorf("IsVodField 应识别标准字段 %s", m.Key)
		}
	}
	if IsVodField("vod_custom_flag") || IsVodField("some_unknown_key") {
		t.Error("IsVodField 不应把未映射字段当成标准字段")
	}
}
