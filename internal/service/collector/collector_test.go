package collector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/model"
)

// ---------- 模拟海洋CMS JSON 资源站 ----------

type mockItem struct {
	ID        string
	Name      string
	VodTime   string
	Recent    bool
	NumericID bool
	PlayURL   string
}

type mockSite struct {
	mu      sync.Mutex
	items   []mockItem
	status  int
	queries []url.Values
	agents  []string
	server  *httptest.Server
}

func newMockSite(t *testing.T, items []mockItem) *mockSite {
	t.Helper()
	m := &mockSite{items: items}
	m.server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.server.Close)
	return m
}

// fullFieldRecord 生成一条包含全部标准字段 + 站点自定义字段的记录，
// 用于验证解析与入库过程中任何字段都不会丢失。
func fullFieldRecord(it mockItem) map[string]interface{} {
	m := make(map[string]interface{}, len(model.VodFields)+3)
	for i, f := range model.VodFields {
		m[f.Key] = i + 1 // 数字型，顺便验证类型兼容
	}
	var id interface{} = it.ID
	if it.NumericID {
		n, _ := strconv.Atoi(it.ID)
		id = n
	}
	m["vod_id"] = id
	m["vod_name"] = it.Name
	m["type_id"] = 6
	m["type_name"] = "动作片"
	m["vod_time"] = it.VodTime
	m["vod_remarks"] = "HD"
	m["vod_play_from"] = "youku"
	m["vod_content"] = "简介内容"
	m["vod_play_url"] = it.PlayURL
	m["vod_en"] = "test"
	m["vod_custom_flag"] = "站点自定义字段" // 不在标准清单里
	m["custom_source_tag"] = map[string]interface{}{"level": 3, "note": "嵌套对象"}
	return m
}

func (m *mockSite) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.queries = append(m.queries, r.URL.Query())
	m.agents = append(m.agents, r.Header.Get("User-Agent"))
	status := m.status
	snapshot := append([]mockItem(nil), m.items...)
	m.mu.Unlock()

	if status != 0 {
		w.WriteHeader(status)
		return
	}
	q := r.URL.Query()
	pg, _ := strconv.Atoi(q.Get("pg"))
	if pg < 1 {
		pg = 1
	}
	filtered := snapshot
	if q.Get("h") != "" {
		filtered = nil
		for _, it := range snapshot {
			if it.Recent {
				filtered = append(filtered, it)
			}
		}
	}
	const pageSize = 2
	start := (pg - 1) * pageSize
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	pageCount := (len(filtered) + pageSize - 1) / pageSize
	if pageCount < 1 {
		pageCount = 1
	}

	list := make([]map[string]interface{}, 0, end-start)
	for _, it := range filtered[start:end] {
		list = append(list, fullFieldRecord(it))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"code": 1, "msg": "数据列表", "page": pg, "pagecount": pageCount,
		"limit": "2", "total": len(filtered), "list": list,
	})
}

func (m *mockSite) snapshot() ([]url.Values, []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]url.Values(nil), m.queries...), append([]string(nil), m.agents...)
}

// parseOne 把单条记录包成完整列表响应并解析。
func parseOne(t *testing.T, obj map[string]interface{}) *ListResponse {
	t.Helper()
	raw, err := json.Marshal(map[string]interface{}{
		"code": 1, "msg": "数据列表", "page": 1, "pagecount": 1,
		"limit": "20", "total": 1, "list": []interface{}{obj},
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := ParseList(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.List) != 1 {
		t.Fatalf("期望 1 条记录，实际 %d", len(r.List))
	}
	return r
}

// ---------- 解析与 URL ----------

func TestParseListCompat(t *testing.T) {
	data := []byte(`{"code":1,"msg":"数据列表","page":2,"page_count":9,"limit":"20","total":37,` +
		`"list":[{"vod_id":21,"vod_name":"测试1","type_id":6,"type_name":"子类1",` +
		`"vod_time":"2018-03-29 20:50:19","vod_remarks":"超清","vod_content":null,` +
		`"vod_play_url":"正片$http://a"}]}`)
	r, err := ParseList(data)
	if err != nil {
		t.Fatal(err)
	}
	if r.PageCount.Int() != 9 || r.Limit.Int() != 20 || r.Total.Int() != 37 || r.Page.Int() != 2 {
		t.Fatalf("分页字段解析异常: %+v", r)
	}
	if len(r.List) != 1 {
		t.Fatalf("list 长度 = %d", len(r.List))
	}
	it := r.List[0]
	if it.Get("vod_id") != "21" || it.Get("type_id") != "6" || it.Get("vod_name") != "测试1" {
		t.Fatalf("数字/字符串混合字段解析异常: %+v", it.Fields)
	}
	if it.Get("vod_content") != "" {
		t.Errorf("null 字段应解析为空，实际 %q", it.Get("vod_content"))
	}
	if _, err := ParseList([]byte(`not-json`)); err == nil {
		t.Fatal("非法 JSON 应返回错误")
	}
}

// TestParseListRejectsNonSuccess 验证苹果CMS 约定 code!=1 视为失败。
func TestParseListRejectsNonSuccess(t *testing.T) {
	for _, code := range []string{"0", "-1", "-2"} {
		data := []byte(`{"code":` + code + `,"msg":"错误","page":1,"pagecount":1,"limit":"20","total":0,"list":[]}`)
		if _, err := ParseList(data); err == nil {
			t.Fatalf("code=%s 应视为失败", code)
		}
	}
	if _, err := ParseList([]byte(`{"code":1,"msg":"ok","page":1,"pagecount":1,"limit":"20","total":0,"list":[]}`)); err != nil {
		t.Fatalf("code=1 应成功: %v", err)
	}
}

// TestParsePlayGroups 覆盖苹果CMS 播放地址的 $$$ / # / $ 三层分隔与容错。
func TestParsePlayGroups(t *testing.T) {
	// 两组播放源、每组两集，含服务器组与备注。
	from := "gugukk$$$yjm3u8"
	url := "第1集$https://a/1.m3u8#第2集$https://a/2.m3u8$$$第1集$https://b/1.m3u8#第2集$https://b/2.m3u8"
	server := "no$$$no"
	note := "备用$$$主用"
	g := ParsePlayGroups(from, url, server, note)
	if len(g) != 2 {
		t.Fatalf("播放组数量异常: %+v", g)
	}
	if g[0].From != "gugukk" || g[0].Server != "no" || g[0].Note != "备用" {
		t.Errorf("第一组元信息异常: %+v", g[0])
	}
	if len(g[0].Episodes) != 2 || g[0].Episodes[0].Name != "第1集" || g[0].Episodes[0].Url != "https://a/1.m3u8" {
		t.Errorf("第一组分集解析异常: %+v", g[0].Episodes)
	}
	if g[1].From != "yjm3u8" || g[1].Note != "主用" || len(g[1].Episodes) != 2 {
		t.Errorf("第二组解析异常: %+v", g[1])
	}

	// 单组、无服务器/备注
	single := ParsePlayGroups("默认线路", "第1集$https://x/1.m3u8", "", "")
	if len(single) != 1 || single[0].From != "默认线路" || len(single[0].Episodes) != 1 {
		t.Fatalf("单组解析异常: %+v", single)
	}

	// 缺失线路名：单组标「默认」，多组标「线路N」
	if got := ParsePlayGroups("", "第1集$https://x/1.m3u8", "", ""); len(got) != 1 || got[0].From != "默认" {
		t.Errorf("单组缺失线路名应标为默认: %+v", got)
	}
	multi := ParsePlayGroups("$$$", "第1集$https://x/1.m3u8$$$第1集$https://y/1.m3u8", "", "")
	if len(multi) != 2 || multi[0].From != "线路1" || multi[1].From != "线路2" {
		t.Errorf("多组缺失线路名应标为线路N: %+v", multi)
	}

	// 每集缺 $：整段视为地址、集名为空（单集电影常省略集名）
	noDollar := ParsePlayGroups("ck", "https://x/1.m3u8#https://x/2.m3u8", "", "")
	if len(noDollar) != 1 || len(noDollar[0].Episodes) != 2 ||
		noDollar[0].Episodes[0].Name != "" || noDollar[0].Episodes[0].Url != "https://x/1.m3u8" {
		t.Errorf("缺 $ 的集解析异常: %+v", noDollar)
	}

	// 空地址：返回 nil（调用方回退原始 vod_play_list）
	if got := ParsePlayGroups("ck", "", "", ""); got != nil {
		t.Errorf("空地址应返回 nil，实际 %+v", got)
	}
}

// TestAllFieldsPreserved 是「Video 实体保留所有字段」的核心回归测试。
func TestAllFieldsPreserved(t *testing.T) {
	obj := make(map[string]interface{}, len(model.VodFields)+4)
	for i, f := range model.VodFields {
		obj[f.Key] = i + 1
	}
	obj["vod_id"] = 21
	obj["vod_name"] = "全字段测试片"
	obj["vod_time"] = "2026-11-07 10:00:00"
	obj["vod_content"] = nil // null 应转为空串
	obj["vod_play_list"] = []interface{}{
		map[string]interface{}{"ep": "1", "url": "https://x/1.m3u8"},
	}
	obj["vod_custom_flag"] = "自定义值" // 不在标准清单里
	obj["custom_source_tag"] = map[string]interface{}{"level": 3, "note": "嵌套"}

	r := parseOne(t, obj)
	it := r.List[0]
	if len(it.Fields) != len(obj) {
		t.Fatalf("字段数 %d != 原始 %d", len(it.Fields), len(obj))
	}
	for _, f := range model.VodFields {
		if _, ok := it.Fields[f.Key]; !ok {
			t.Errorf("解析后丢失标准字段 %s", f.Key)
		}
	}
	if it.Get("vod_content") != "" {
		t.Errorf("null 应为空串，实际 %q", it.Get("vod_content"))
	}
	if !strings.Contains(it.Get("vod_play_list"), `"ep":"1"`) {
		t.Errorf("数组字段未完整保留: %q", it.Get("vod_play_list"))
	}

	v := toVideo(1, it)
	rv := reflect.ValueOf(v)
	for _, f := range model.VodFields {
		fv := rv.FieldByName(f.Go)
		if fv.Kind() != reflect.String {
			t.Errorf("字段 %s 类型异常", f.Go)
			continue
		}
		if f.Key == "vod_content" { // 上面故意设为 null
			continue
		}
		if fv.String() == "" {
			t.Errorf("标准字段 %s 未映射到实体列 %s", f.Key, f.Column)
		}
	}
	if v.VodID != "21" || v.VodName != "全字段测试片" || v.VodTime != "2026-11-07 10:00:00" {
		t.Fatalf("核心字段映射异常: %+v", v)
	}

	// Extra 保留未映射字段，且嵌套对象保持原始 JSON 结构
	var extra map[string]interface{}
	if err := json.Unmarshal([]byte(v.Extra), &extra); err != nil {
		t.Fatalf("Extra 不是合法 JSON: %v | %s", err, v.Extra)
	}
	if extra["vod_custom_flag"] != "自定义值" {
		t.Errorf("自定义字段丢失: %+v", extra)
	}
	tag, ok := extra["custom_source_tag"].(map[string]interface{})
	if !ok || tag["level"] != float64(3) || tag["note"] != "嵌套" {
		t.Errorf("嵌套对象未完整保留: %+v", extra["custom_source_tag"])
	}
	if _, exists := extra["vod_name"]; exists {
		t.Error("标准字段不应重复写入 Extra")
	}

	// 指纹覆盖全部字段：改冷门标准字段或自定义字段都应触发更新
	base := v.Hash
	obj2 := map[string]interface{}{}
	for k, val := range obj {
		obj2[k] = val
	}
	obj2["vod_level"] = 999
	if got := ToVideo(1, parseOne(t, obj2).List[0]).Hash; got == base {
		t.Error("修改标准字段 vod_level 后指纹未变化")
	}
	obj3 := map[string]interface{}{}
	for k, val := range obj {
		obj3[k] = val
	}
	obj3["vod_custom_flag"] = "改过了"
	if got := ToVideo(1, parseOne(t, obj3).List[0]).Hash; got == base {
		t.Error("修改自定义字段后指纹未变化（扩展字段也应能触发更新）")
	}
}

func TestBuildURL(t *testing.T) {
	cfg := config.Default().Collector
	src := model.Source{
		APIBase:     "https://site.com/api.php/provide/vod/",
		ExtraParams: "t=1&ac=videolist&wd=电影",
	}
	raw, err := BuildURL(src, 3, 24, cfg)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	q := u.Query()
	if q.Get("ac") != "videolist" || q.Get("pg") != "3" || q.Get("h") != "24" ||
		q.Get("t") != "1" || q.Get("wd") != "电影" {
		t.Fatalf("参数拼装异常: %s", raw)
	}

	raw, _ = BuildURL(src, 1, 0, cfg)
	u, _ = url.Parse(raw)
	if u.Query().Get("h") != "" {
		t.Errorf("全量采集不应带 h 参数: %s", raw)
	}

	cfg.PageSize = 50
	raw, _ = BuildURL(model.Source{APIBase: "https://site.com/zyapi.php"}, 1, 0, cfg)
	u, _ = url.Parse(raw)
	if u.Query().Get("ac") != "videolist" || u.Query().Get("pagesize") != "50" {
		t.Errorf("默认 ac / pagesize 异常: %s", raw)
	}

	if _, err := BuildURL(model.Source{APIBase: "not-a-url"}, 1, 0, cfg); err == nil {
		t.Fatal("非法地址应返回错误")
	}
}

func TestFetchList(t *testing.T) {
	items := []mockItem{
		{ID: "1", Name: "A", VodTime: "2026-11-06 08:00:00", NumericID: true},
		{ID: "2", Name: "B", VodTime: "2026-11-06 09:00:00"},
		{ID: "3", Name: "C", VodTime: "2026-11-07 10:00:00", Recent: true},
	}
	m := newMockSite(t, items)
	src := model.Source{APIBase: m.server.URL + "/api.php/provide/vod/"}
	f := NewFetcher(config.Default().Collector)

	r, err := f.FetchList(context.Background(), src, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.List) != 2 || r.Total.Int() != 3 || r.PageCount.Int() != 2 {
		t.Fatalf("首页解析异常: %+v", r)
	}
	queries, agents := m.snapshot()
	if len(queries) != 1 || queries[0].Get("pg") != "1" || queries[0].Get("h") != "" {
		t.Fatalf("请求参数异常: %+v", queries)
	}
	if !strings.Contains(agents[0], "AssetSpider") {
		t.Errorf("User-Agent 未正确设置: %q", agents[0])
	}
}

// 真实资源站的播放字段写法五花八门：标准 $$$ 串、别名键、结构化数组、
// 海洋CMS 的 v_* 字段。这里锁定「只要接口给了播放地址，就必须完整入库」
// 这一契约，避免播放页出现「没有播放地址」。
func TestPlayFieldAliasesAndStructured(t *testing.T) {
	cases := []struct {
		name         string
		obj          map[string]interface{}
		wantVodID    string
		wantURLPart  string
		wantFrom     string
		wantGroups   int
		wantExtraKey string
	}{
		{
			name: "标准 vod_play_url 串",
			obj: map[string]interface{}{
				"vod_id": 1, "vod_name": "标准", "vod_time": "2026-11-07 10:00:00",
				"vod_play_from": "youku$$$qq",
				"vod_play_url":  "第1集$https://x/1.m3u8#第2集$https://x/2.m3u8$$$第1集$https://y/1.m3u8",
			},
			wantVodID: "1", wantURLPart: "第1集$https://x/1.m3u8", wantFrom: "youku$$$qq", wantGroups: 2,
		},
		{
			name: "别名 vod_playurl",
			obj: map[string]interface{}{
				"vod_id": 2, "vod_name": "别名", "vod_play_from": "m3u8",
				"vod_playurl": "第1集$https://x/1.m3u8#第2集$https://x/2.m3u8",
			},
			wantVodID: "2", wantURLPart: "第1集$https://x/1.m3u8", wantFrom: "m3u8",
			wantGroups: 1, wantExtraKey: "vod_playurl",
		},
		{
			name: "海洋CMS v_playdata / v_id",
			obj: map[string]interface{}{
				"v_id": 3, "v_name": "海洋", "v_playdata": "正片$https://x/1.m3u8",
			},
			wantVodID: "3", wantURLPart: "正片$https://x/1.m3u8",
			wantGroups: 1, wantExtraKey: "v_playdata",
		},
		{
			name: "vod_play_url 为结构化数组",
			obj: map[string]interface{}{
				"vod_id": 4, "vod_name": "结构化",
				"vod_play_url": []interface{}{
					map[string]interface{}{"from": "m3u8", "episodes": []interface{}{
						map[string]interface{}{"name": "第1集", "url": "https://x/1.m3u8"},
					}},
				},
			},
			wantVodID: "4", wantURLPart: "第1集$https://x/1.m3u8", wantFrom: "m3u8", wantGroups: 1,
		},
		{
			name: "只有 vod_play_list 结构化数组",
			obj: map[string]interface{}{
				"vod_id": 5, "vod_name": "播放列表",
				"vod_play_list": []interface{}{
					map[string]interface{}{"from": "m3u8", "episodes": []interface{}{
						map[string]interface{}{"name": "第1集", "url": "https://x/1.m3u8"},
					}},
				},
			},
			wantVodID: "5", wantURLPart: "第1集$https://x/1.m3u8", wantFrom: "m3u8", wantGroups: 1,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := ToVideo(1, parseOne(t, c.obj).List[0])
			if v.VodID != c.wantVodID {
				t.Errorf("vod_id 异常: %q，期望 %q", v.VodID, c.wantVodID)
			}
			if !strings.Contains(v.VodPlayURL, c.wantURLPart) {
				t.Errorf("vod_play_url 未正确入库: %q", v.VodPlayURL)
			}
			if c.wantFrom != "" && !strings.Contains(v.VodPlayFrom, c.wantFrom) {
				t.Errorf("vod_play_from 异常: %q", v.VodPlayFrom)
			}
			var groups []model.PlayGroup
			if err := json.Unmarshal([]byte(v.VodPlayList), &groups); err != nil {
				t.Fatalf("vod_play_list 不是合法播放组 JSON: %v | %s", err, v.VodPlayList)
			}
			if len(groups) != c.wantGroups {
				t.Fatalf("播放组数 %d != 期望 %d: %+v", len(groups), c.wantGroups, groups)
			}
			for _, g := range groups {
				if g.From == "" {
					t.Errorf("播放组缺少线路名: %+v", g)
				}
				for _, e := range g.Episodes {
					if !strings.HasPrefix(e.Url, "https://") {
						t.Errorf("分集地址异常: %+v", e)
					}
				}
			}
			if c.wantExtraKey != "" && !strings.Contains(v.Extra, `"`+c.wantExtraKey+`"`) {
				t.Errorf("原始别名键应保留在 Extra 便于溯源: %s", v.Extra)
			}
		})
	}

	// 接口完全不给 vod_id：只要有名称或播放地址就生成稳定伪 ID，不再整条跳过。
	noID := map[string]interface{}{"v_name": "无 ID 片", "v_playdata": "正片$https://x/1.m3u8"}
	a := ToVideo(1, parseOne(t, noID).List[0])
	b := ToVideo(1, parseOne(t, noID).List[0])
	if !strings.HasPrefix(a.VodID, "auto-") || a.VodID != b.VodID {
		t.Fatalf("无 vod_id 时应生成稳定伪 ID，实际 %q / %q", a.VodID, b.VodID)
	}
	if a.VodPlayList == "" {
		t.Fatal("无 vod_id 记录的播放地址也应入库")
	}

	// 精简响应（ac=list 之类）确实没有播放字段时保持为空，由任务诊断提示用户。
	slim := map[string]interface{}{"vod_id": 9, "vod_name": "精简", "vod_time": "2026-11-07 10:00:00"}
	plain := ToVideo(1, parseOne(t, slim).List[0])
	if plain.VodPlayURL != "" || plain.VodPlayList != "" {
		t.Fatalf("接口未返回播放字段时不应凭空生成: %+v", plain)
	}
}
