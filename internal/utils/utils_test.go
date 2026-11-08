package utils

import (
	"encoding/json"
	"testing"
)

func TestParseRequestValues(t *testing.T) {
	if n, err := ParseUintParam("12"); err != nil || n != 12 {
		t.Fatalf("ParseUintParam(12) = %d, %v", n, err)
	}
	for _, raw := range []string{"", "0", "abc", "-1"} {
		if _, err := ParseUintParam(raw); err == nil {
			t.Errorf("ParseUintParam(%q) 应返回错误", raw)
		}
	}
	if ParseUintQuery("7") != 7 || ParseUintQuery("x") != 0 || ParseIntQuery("3") != 3 || ParseIntQuery("x") != 0 {
		t.Error("query 参数解析异常")
	}
}

func TestStringifyJSONValue(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`"hello"`, "hello"},
		{`21`, "21"},
		{`null`, ""},
		{`{"a": 1, "b": [2]}`, `{"a":1,"b":[2]}`},
		{`true`, "true"},
	}
	for _, c := range cases {
		if got := StringifyJSONValue(json.RawMessage(c.raw)); got != c.want {
			t.Errorf("StringifyJSONValue(%s) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestNormalizeTime(t *testing.T) {
	if got := NormalizeTime("2026-11-07 10:00"); got != "2026-11-07 10:00:00" {
		t.Errorf("分钟精度应补齐秒: %q", got)
	}
	if got := NormalizeTime("2026-11-07T10:00:00"); got != "2026-11-07 10:00:00" {
		t.Errorf("ISO 时间归一化异常: %q", got)
	}
	if got := NormalizeTime("not-a-time"); got != "not-a-time" {
		t.Errorf("无法识别时应原样返回: %q", got)
	}
}

func TestFingerprint(t *testing.T) {
	a := map[string]string{"x": "1", "y": "2"}
	b := map[string]string{"y": "2", "x": "1"}
	if Fingerprint(a) != Fingerprint(b) {
		t.Error("键顺序不应影响指纹")
	}
	b["y"] = "3"
	if Fingerprint(a) == Fingerprint(b) {
		t.Error("内容变化应改变指纹")
	}
	if len(Fingerprint(a)) != 32 {
		t.Error("指纹应为 16 字节的十六进制串")
	}
}

func TestCategoryKey(t *testing.T) {
	if CategoryKey("6", "动作片") != "id:6" || CategoryKey(" 6 ", "别的名字") != "id:6" {
		t.Error("有 type_id 时应以 id 为键")
	}
	if CategoryKey("", "喜剧片") != "name:喜剧片" || CategoryKey("0", "喜剧片") != "name:喜剧片" {
		t.Error("type_id 为空或 0 时应回退到分类名")
	}
	if CategoryKey("0", "  ") != "" || CategoryKey("", "") != "" {
		t.Error("无分类信息时应返回空串")
	}
}

func TestEnvInt(t *testing.T) {
	const name = "ASSETSPIDER_UTILS_TEST_INT"
	t.Setenv(name, "42")
	n := 1
	if err := EnvInt(name, &n); err != nil || n != 42 {
		t.Fatalf("EnvInt 异常: %d %v", n, err)
	}
	t.Setenv(name, "abc")
	if err := EnvInt(name, &n); err == nil {
		t.Error("非整数环境变量应返回错误")
	}
	t.Setenv(name, "")
	n = 5
	if err := EnvInt(name, &n); err != nil || n != 5 {
		t.Error("变量为空时应保持原值")
	}
}
