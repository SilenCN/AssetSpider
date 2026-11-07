package collector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/store"
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

func (m *mockSite) setItems(items []mockItem) {
	m.mu.Lock()
	m.items = items
	m.mu.Unlock()
}
func (m *mockSite) setStatus(code int) {
	m.mu.Lock()
	m.status = code
	m.mu.Unlock()
}
func (m *mockSite) snapshot() ([]url.Values, []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]url.Values(nil), m.queries...), append([]string(nil), m.agents...)
}

func newRunnerStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "collector.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func addSource(t *testing.T, s *store.Store, apiBase string, enabled bool) uint {
	t.Helper()
	src := &model.Source{
		Name: "模拟资源站", APIBase: apiBase, Format: "seacms_json",
		Enabled: enabled, IncrementalHours: 2,
	}
	if err := s.CreateSource(src); err != nil {
		t.Fatal(err)
	}
	return src.ID
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
	if !strings.Contains(it.RawJSON, `"vod_id":21`) {
		t.Errorf("RawJSON 未保留原始 JSON: %s", it.RawJSON)
	}
	if _, err := ParseList([]byte(`not-json`)); err == nil {
		t.Fatal("非法 JSON 应返回错误")
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
	if !strings.Contains(it.RawJSON, "vod_custom_flag") {
		t.Errorf("RawJSON 未保留原始内容: %s", it.RawJSON)
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

	// Raw 是接口返回的原始单条 JSON 全文，字段数与原始一致
	var rawObj map[string]interface{}
	if err := json.Unmarshal([]byte(v.Raw), &rawObj); err != nil {
		t.Fatalf("Raw 不是合法 JSON: %v", err)
	}
	if len(rawObj) != len(obj) || rawObj["vod_custom_flag"] != "自定义值" {
		t.Fatalf("Raw 未完整保留原始记录: %d/%d", len(rawObj), len(obj))
	}

	// 指纹覆盖全部字段：改冷门标准字段或自定义字段都应触发更新
	base := v.Hash
	obj2 := map[string]interface{}{}
	for k, val := range obj {
		obj2[k] = val
	}
	obj2["vod_level"] = 999
	if got := toVideo(1, parseOne(t, obj2).List[0]).Hash; got == base {
		t.Error("修改标准字段 vod_level 后指纹未变化")
	}
	obj3 := map[string]interface{}{}
	for k, val := range obj {
		obj3[k] = val
	}
	obj3["vod_custom_flag"] = "改过了"
	if got := toVideo(1, parseOne(t, obj3).List[0]).Hash; got == base {
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
	if u.Query().Get("ac") != "list" || u.Query().Get("limit") != "50" {
		t.Errorf("默认 ac / pageSize 异常: %s", raw)
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

// ---------- 全量 / 增量 / 去重 / 失败 ----------

func TestRunnerFullIncrementalAndDedup(t *testing.T) {
	items := []mockItem{
		{ID: "1", Name: "影片A", VodTime: "2026-11-06 08:00:00", NumericID: true, PlayURL: "https://x/a.m3u8"},
		{ID: "2", Name: "影片B", VodTime: "2026-11-06 09:00:00", PlayURL: "https://x/b.m3u8"},
		{ID: "3", Name: "影片C", VodTime: "2026-11-07 10:00:00", Recent: true, PlayURL: "https://x/c.m3u8"},
	}
	m := newMockSite(t, items)
	st := newRunnerStore(t)
	id := addSource(t, st, m.server.URL+"/api.php/provide/vod/", true)
	runner := NewRunner(st, config.Default().Collector)
	ctx := context.Background()

	// 1. 全量：3 条分 2 页全部入库
	res, err := runner.RunOne(ctx, id, model.ModeFull)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != model.JobSuccess || res.Fetched != 3 || res.New != 3 || res.Pages != 2 || res.Total != 3 {
		t.Fatalf("全量采集结果异常: %+v", res)
	}
	if _, total, _ := st.ListVideos(0, "", 1, 20); total != 3 {
		t.Fatalf("入库条数应为 3，实际 %d", total)
	}
	vids, _, _ := st.ListVideos(0, "影片A", 1, 20)
	if len(vids) != 1 || vids[0].VodID != "1" || !strings.Contains(vids[0].VodPlayURL, "a.m3u8") {
		t.Fatalf("数字型 vod_id 或播放地址解析异常: %+v", vids)
	}
	// 全字段入库：抽查冷门字段、扩展字段与原始 JSON
	if vids[0].VodLevel == "" || vids[0].VodDoubanID == "" || vids[0].VodPwdDownURL == "" {
		t.Errorf("冷门标准字段未入库: %+v", vids[0])
	}
	if !strings.Contains(vids[0].Extra, "vod_custom_flag") || !strings.Contains(vids[0].Raw, "vod_custom_flag") {
		t.Errorf("扩展字段或原始 JSON 未入库: extra=%s raw=%s", vids[0].Extra, vids[0].Raw)
	}
	src, _ := st.GetSource(id)
	if src.LastStatus != model.JobSuccess || src.LastCursor != "2026-11-07 10:00:00" || src.LastSyncAt == nil {
		t.Fatalf("站点状态/游标未回写: %+v", src)
	}

	// 2. 再次全量：全部命中去重
	res, err = runner.RunOne(ctx, id, model.ModeFull)
	if err != nil {
		t.Fatal(err)
	}
	if res.New != 0 || res.Unchanged != 3 || res.Fetched != 3 {
		t.Fatalf("重复全量应全部未变化: %+v", res)
	}

	// 3. 站点修改一条内容（vod_time 不变）→ 应识别为更新
	edited := append([]mockItem(nil), items...)
	edited[1].Name = "影片B 修订版"
	m.setItems(edited)
	res, err = runner.RunOne(ctx, id, model.ModeFull)
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 1 || res.Unchanged != 2 {
		t.Fatalf("内容变化应被更新: %+v", res)
	}

	// 4. 站点新增一条更新的影片 → 增量只采这一条
	withNew := append(append([]mockItem(nil), edited...),
		mockItem{ID: "4", Name: "影片D 新增", VodTime: "2026-11-07 12:00:00", Recent: true, PlayURL: "https://x/d.m3u8"})
	m.setItems(withNew)
	res, err = runner.RunOne(ctx, id, model.ModeIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fetched != 1 || res.New != 1 {
		t.Fatalf("增量应只处理游标之后的新数据: %+v", res)
	}
	if _, total, _ := st.ListVideos(0, "", 1, 20); total != 4 {
		t.Fatalf("增量入库后总数应为 4，实际 %d", total)
	}
	queries, _ := m.snapshot()
	hasH := false
	for _, q := range queries {
		if q.Get("h") != "" {
			hasH = true
		}
	}
	if !hasH {
		t.Fatal("增量采集未向接口传递 h 参数")
	}

	// 5. 再增量一次：无新数据，幂等
	res, err = runner.RunOne(ctx, id, model.ModeIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fetched != 0 || res.New != 0 || res.Unchanged != 0 {
		t.Fatalf("无新数据时增量应为 0: %+v", res)
	}

	// 6. 采集记录与失败处理
	jobs, _ := st.ListJobs(id, 50)
	if len(jobs) != 5 || jobs[0].Status != model.JobSuccess {
		t.Fatalf("采集记录数量/状态异常: %d %+v", len(jobs), jobs)
	}
	m.setStatus(http.StatusInternalServerError)
	res, err = runner.RunOne(ctx, id, model.ModeFull)
	if err == nil || res == nil || res.Status != model.JobFailed {
		t.Fatalf("接口异常时应记录失败: err=%v res=%+v", err, res)
	}
	src, _ = st.GetSource(id)
	if src.LastStatus != model.JobFailed || src.LastError == "" {
		t.Fatalf("失败状态未回写: %+v", src)
	}

	if _, err := runner.RunOne(ctx, id, "bad-mode"); err == nil {
		t.Fatal("非法模式应返回错误")
	}
}

func TestRunnerRunAllOnlyEnabled(t *testing.T) {
	m := newMockSite(t, []mockItem{
		{ID: "1", Name: "A", VodTime: "2026-11-07 10:00:00", Recent: true},
	})
	st := newRunnerStore(t)
	good := addSource(t, st, m.server.URL+"/api.php/provide/vod/", true)
	addSource(t, st, m.server.URL+"/api.php/provide/vod/", false)
	runner := NewRunner(st, config.Default().Collector)

	results, err := runner.RunAll(context.Background(), model.ModeFull)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].SourceID != good || results[0].New != 1 {
		t.Fatalf("批量采集应只处理启用站点: %+v", results)
	}
	if _, total, _ := st.ListVideos(0, "", 1, 20); total != 1 {
		t.Fatalf("入库总数应为 1，实际 %d", total)
	}
}
