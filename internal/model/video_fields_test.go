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
	// 精确锁定清单规模：既防误删字段导致静默丢列，也防误加字段破坏前后端一致。
	// 2026-11 按需求裁掉 23 个后台管理字段（审核状态 / 模板 / 积分 / 密码等）后为 68。
	if len(VodFields) != 68 {
		t.Errorf("标准字段数量异常（%d），应为 68，疑似清单被误增删", len(VodFields))
	}
	// 每个分组都必须仍有字段，避免删字段后留下空的详情页签。
	usedGroups := map[string]bool{}
	for _, f := range VodFields {
		usedGroups[f.Group] = true
	}
	for _, g := range VodGroupLabels {
		if !usedGroups[g.Key] {
			t.Errorf("分组 %q（%s）已无字段，应同步从 VodGroupLabels 移除", g.Key, g.Label)
		}
	}
}

// TestVodFieldColumnTypesMatchEntity 把「实际字段长度」锁到实体标签上：
// 字段清单的 Size（及 vodColumnOverrides 覆盖）决定 VARCHAR(N) / TEXT / MEDIUMTEXT，Video 的 gorm tag 必须逐字段一致。
// 清单是解析 / 入库 / API 的唯一数据源，若只改 tag 或只改清单，新库列型就会与设计脱节。
func TestVodFieldColumnTypesMatchEntity(t *testing.T) {
	typ := reflect.TypeOf(Video{})
	for _, f := range VodFields {
		if f.Size < 0 {
			t.Errorf("字段 %s 的列宽 %d 非法（0 表示长文本）", f.Key, f.Size)
			continue
		}
		if f.Long != (f.Size == 0) {
			t.Errorf("字段 %s 的 Long=%v 与 Size=%d 矛盾", f.Key, f.Long, f.Size)
		}
		sf, ok := typ.FieldByName(f.Go)
		if !ok {
			t.Errorf("Video 缺少字段 %s", f.Go)
			continue
		}
		want := VodColumnType(f)
		got := ""
		for _, part := range strings.Split(sf.Tag.Get("gorm"), ";") {
			switch {
			case strings.HasPrefix(part, "type:"):
				got = strings.TrimPrefix(part, "type:")
			case strings.HasPrefix(part, "size:"):
				got = "varchar(" + strings.TrimPrefix(part, "size:") + ")"
			}
		}
		if got != want {
			t.Errorf("字段 %s（%s）列型应为 %s，Video 的 gorm tag 为 %q", f.Key, f.Column, want, got)
		}
	}

	// 抽查 MacCMS V10 官方安装表里的代表性长度，防止整张清单被批量改错。
	wantTypes := map[string]string{
		"vod_id":       "varchar(64)",   // 站点 ID 保留原始文本
		"vod_name":     "varchar(255)",  // 官方 varchar(255)
		"vod_pic":      "varchar(1024)", // 官方 varchar(1024)
		"vod_area":     "varchar(128)",  // 官方 varchar(20) + 第三方站点余量
		"vod_actor":    "text",          // 主演串长度不可预估，用 TEXT
		"vod_content":  "mediumtext",    // 官方 mediumtext
		"vod_play_url": "mediumtext",    // 官方 mediumtext
	}
	for key, want := range wantTypes {
		for _, f := range VodFields {
			if f.Key == key {
				if got := VodColumnType(f); got != want {
					t.Errorf("%s 列型应为 %s，实际 %s", key, want, got)
				}
			}
		}
	}
}

// TestVodColumnOverrides 校验显式列型覆盖表：键必须是清单内的长文本字段，否则字段改名 /
// 删除后覆盖会静默失效，列型悄悄退回 MEDIUMTEXT，而 gorm tag 与清单的一致性测试仍会通过。
func TestVodColumnOverrides(t *testing.T) {
	byKey := make(map[string]VodField, len(VodFields))
	for _, f := range VodFields {
		byKey[f.Key] = f
	}
	for key, typ := range vodColumnOverrides {
		f, ok := byKey[key]
		if !ok {
			t.Errorf("覆盖表里的字段 %s 不在 VodFields 清单内", key)
			continue
		}
		if !f.Long || f.Size != 0 {
			t.Errorf("字段 %s 被覆盖为 %s，但仍须是长文本（Long=true、Size=0），实际 Long=%v Size=%d", key, typ, f.Long, f.Size)
		}
		if typ != "text" {
			t.Errorf("字段 %s 的覆盖列型 %q 非法，目前只允许 text", key, typ)
		}
	}
	if got := VodColumnType(byKey["vod_actor"]); got != "text" {
		t.Errorf("vod_actor 列型应为 text，实际 %s", got)
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
