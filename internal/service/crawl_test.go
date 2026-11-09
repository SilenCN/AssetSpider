package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

// ---------- 模拟海洋CMS JSON 资源站 ----------

type mockItem struct {
	ID        string
	Name      string
	VodTime   string
	Recent    bool
	NumericID bool
	PlayURL   string
	From      string // 覆盖默认 vod_play_from（苹果CMS 播放组名）
}

type mockSite struct {
	mu      sync.Mutex
	items   []mockItem
	status  int
	queries []url.Values
	server  *httptest.Server
}

func newMockSite(t *testing.T, items []mockItem) *mockSite {
	t.Helper()
	m := &mockSite{items: items}
	m.server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.server.Close)
	return m
}

func fullFieldRecord(it mockItem) map[string]interface{} {
	m := make(map[string]interface{}, len(model.VodFields)+3)
	for i, f := range model.VodFields {
		m[f.Key] = i + 1
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
	m["vod_content"] = "简介内容"
	if it.From != "" {
		m["vod_play_from"] = it.From
	} else {
		m["vod_play_from"] = "youku"
	}
	m["vod_play_url"] = it.PlayURL
	m["vod_en"] = "test"
	m["vod_custom_flag"] = "站点自定义字段"
	m["custom_source_tag"] = map[string]interface{}{"level": 3, "note": "嵌套对象"}
	return m
}

func (m *mockSite) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	m.queries = append(m.queries, r.URL.Query())
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

func (m *mockSite) snapshot() []url.Values {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]url.Values(nil), m.queries...)
}

func newCrawlService(t *testing.T) (*repository.Repositories, *CrawlService) {
	t.Helper()
	st, err := repository.Open(filepath.Join(t.TempDir(), "crawl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, NewCrawlService(st.Sources, st.Videos, st.Categories, st.Jobs, config.Default().Collector)
}

func addSource(t *testing.T, st *repository.Repositories, apiBase string, enabled bool) uint {
	t.Helper()
	src := &model.Source{
		Name: "模拟资源站", APIBase: apiBase, Format: "seacms_json",
		Enabled: enabled, IncrementalHours: 2,
	}
	if err := st.Sources.CreateSource(src); err != nil {
		t.Fatal(err)
	}
	return src.ID
}

// ---------- 全量 / 增量 / 去重 / 失败 ----------

func TestCrawlServiceFullIncrementalAndDedup(t *testing.T) {
	items := []mockItem{
		{ID: "1", Name: "影片A", VodTime: "2026-11-06 08:00:00", NumericID: true, PlayURL: "https://x/a.m3u8"},
		{ID: "2", Name: "影片B", VodTime: "2026-11-06 09:00:00", PlayURL: "https://x/b.m3u8"},
		{ID: "3", Name: "影片C", VodTime: "2026-11-07 10:00:00", Recent: true, PlayURL: "https://x/c.m3u8"},
	}
	m := newMockSite(t, items)
	st, svc := newCrawlService(t)
	id := addSource(t, st, m.server.URL+"/api.php/provide/vod/", true)
	ctx := context.Background()

	// 1. 全量：3 条分 2 页全部入库
	res, err := svc.RunOne(ctx, id, model.ModeFull)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != model.JobSuccess || res.Fetched != 3 || res.New != 3 || res.Pages != 2 || res.Total != 3 {
		t.Fatalf("全量采集结果异常: %+v", res)
	}
	if _, total, _ := st.Videos.ListVideos(repository.NewVideoQuery(0, "", 1, 20)); total != 3 {
		t.Fatalf("入库条数应为 3，实际 %d", total)
	}
	vids, _, _ := st.Videos.ListVideos(repository.NewVideoQuery(0, "影片A", 1, 20))
	if len(vids) != 1 || vids[0].VodID != "1" || !strings.Contains(vids[0].VodPlayURL, "a.m3u8") {
		t.Fatalf("数字型 vod_id 或播放地址解析异常: %+v", vids)
	}
	// 全字段入库：抽查冷门字段与扩展字段（不再冗余保存整条 raw）
	if vids[0].VodLevel == "" || vids[0].VodDoubanID == "" || vids[0].VodPwdDownURL == "" {
		t.Errorf("冷门标准字段未入库: %+v", vids[0])
	}
	if !strings.Contains(vids[0].Extra, "vod_custom_flag") {
		t.Errorf("扩展字段未入库: extra=%s", vids[0].Extra)
	}
	src, _ := st.Sources.GetSource(id)
	if src.LastStatus != model.JobSuccess || src.LastCursor != "2026-11-07 10:00:00" || src.LastSyncAt == nil {
		t.Fatalf("站点状态/游标未回写: %+v", src)
	}

	// 1b. 采集时自动创建分类并把影片关联上去
	cats, _ := st.Categories.List(id)
	if len(cats) != 1 || cats[0].Name != "动作片" || cats[0].TypeID != "6" || cats[0].SourceID != id {
		t.Fatalf("未按视频分类自动建分类: %+v", cats)
	}
	allVids, _, _ := st.Videos.ListVideos(repository.NewVideoQuery(0, "", 1, 20))
	for _, v := range allVids {
		if v.CategoryID != cats[0].ID {
			t.Errorf("影片 %s 未关联分类: categoryId=%d want=%d", v.VodID, v.CategoryID, cats[0].ID)
		}
	}
	counts, _ := st.Videos.CountVideosByCategory()
	if counts[cats[0].ID] != 3 {
		t.Errorf("分类影片数应为 3，实际 %d", counts[cats[0].ID])
	}

	// 2. 再次全量：全部命中去重
	res, err = svc.RunOne(ctx, id, model.ModeFull)
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
	res, err = svc.RunOne(ctx, id, model.ModeFull)
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
	res, err = svc.RunOne(ctx, id, model.ModeIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fetched != 1 || res.New != 1 {
		t.Fatalf("增量应只处理游标之后的新数据: %+v", res)
	}
	if _, total, _ := st.Videos.ListVideos(repository.NewVideoQuery(0, "", 1, 20)); total != 4 {
		t.Fatalf("增量入库后总数应为 4，实际 %d", total)
	}
	queries := m.snapshot()
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
	res, err = svc.RunOne(ctx, id, model.ModeIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fetched != 0 || res.New != 0 || res.Unchanged != 0 {
		t.Fatalf("无新数据时增量应为 0: %+v", res)
	}

	// 6. 采集记录与失败处理
	jobs, _ := st.Jobs.ListJobs(id, 50)
	if len(jobs) != 5 || jobs[0].Status != model.JobSuccess {
		t.Fatalf("采集记录数量/状态异常: %d %+v", len(jobs), jobs)
	}
	m.setStatus(http.StatusInternalServerError)
	res, err = svc.RunOne(ctx, id, model.ModeFull)
	if err == nil || res == nil || res.Status != model.JobFailed {
		t.Fatalf("接口异常时应记录失败: err=%v res=%+v", err, res)
	}
	src, _ = st.Sources.GetSource(id)
	if src.LastStatus != model.JobFailed || src.LastError == "" {
		t.Fatalf("失败状态未回写: %+v", src)
	}

	if _, err := svc.RunOne(ctx, id, "bad-mode"); err == nil {
		t.Fatal("非法模式应返回错误")
	}
}

func TestCrawlServiceRunAllOnlyEnabled(t *testing.T) {
	m := newMockSite(t, []mockItem{
		{ID: "1", Name: "A", VodTime: "2026-11-07 10:00:00", Recent: true},
	})
	st, svc := newCrawlService(t)
	good := addSource(t, st, m.server.URL+"/api.php/provide/vod/", true)
	addSource(t, st, m.server.URL+"/api.php/provide/vod/", false)

	results, err := svc.RunAll(context.Background(), model.ModeFull)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].SourceID != good || results[0].New != 1 {
		t.Fatalf("批量采集应只处理启用站点: %+v", results)
	}
	if _, total, _ := st.Videos.ListVideos(repository.NewVideoQuery(0, "", 1, 20)); total != 1 {
		t.Fatalf("入库总数应为 1，实际 %d", total)
	}
}

func TestCrawlCategoriesArePerSource(t *testing.T) {
	m := newMockSite(t, []mockItem{
		{ID: "1", Name: "A", VodTime: "2026-11-07 10:00:00", Recent: true},
	})
	st, svc := newCrawlService(t)
	apiBase := m.server.URL + "/api.php/provide/vod/"
	first := addSource(t, st, apiBase, true)
	second := addSource(t, st, apiBase, true)

	ctx := context.Background()
	if _, err := svc.RunOne(ctx, first, model.ModeFull); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunOne(ctx, second, model.ModeFull); err != nil {
		t.Fatal(err)
	}
	// 两个站点返回同样的 type_id / type_name，仍应各有一套分类
	cats1, _ := st.Categories.List(first)
	cats2, _ := st.Categories.List(second)
	if len(cats1) != 1 || len(cats2) != 1 || cats1[0].ID == cats2[0].ID {
		t.Fatalf("分类未按站点隔离: %+v / %+v", cats1, cats2)
	}
	all, _ := st.Categories.List(0)
	if len(all) != 2 {
		t.Fatalf("应共生成 2 个分类，实际 %d", len(all))
	}
}

func TestNormalizeMode(t *testing.T) {
	if mode, err := NormalizeMode(""); err != nil || mode != model.ModeIncremental {
		t.Fatalf("缺省应为增量: %q %v", mode, err)
	}
	if mode, err := NormalizeMode("full"); err != nil || mode != model.ModeFull {
		t.Fatalf("full 解析异常: %q %v", mode, err)
	}
	if _, err := NormalizeMode("bad"); err == nil {
		t.Fatal("非法模式应返回错误")
	}
}

// TestCrawlStoresPlayGroups 验证采集时按苹果CMS 字段解析出的播放组会随影片入库。
func TestCrawlStoresPlayGroups(t *testing.T) {
	from := "youku$$$qq"
	url := "第1集$https://x/1.m3u8#第2集$https://x/2.m3u8$$$第1集$https://y/1.m3u8#第2集$https://y/2.m3u8"
	items := []mockItem{
		{ID: "1", Name: "带播放组", VodTime: "2026-11-07 10:00:00", Recent: true,
			From: from, PlayURL: url},
	}
	m := newMockSite(t, items)
	st, svc := newCrawlService(t)
	id := addSource(t, st, m.server.URL+"/api.php/provide/vod/", true)
	if _, err := svc.RunOne(context.Background(), id, model.ModeFull); err != nil {
		t.Fatal(err)
	}
	vids, _, _ := st.Videos.ListVideos(repository.NewVideoQuery(0, "带播放组", 1, 20))
	if len(vids) != 1 {
		t.Fatalf("应入库 1 条，实际 %d", len(vids))
	}
	v := vids[0]
	if v.VodPlayList == "" {
		t.Fatal("vod_play_list 未写入播放组")
	}
	var groups []model.PlayGroup
	if err := json.Unmarshal([]byte(v.VodPlayList), &groups); err != nil {
		t.Fatalf("vod_play_list 不是合法播放组 JSON: %v | %s", err, v.VodPlayList)
	}
	if len(groups) != 2 {
		t.Fatalf("应解析出 2 个播放组，实际 %d: %+v", len(groups), groups)
	}
	if groups[0].From != "youku" || len(groups[0].Episodes) != 2 ||
		groups[0].Episodes[0].Url != "https://x/1.m3u8" {
		t.Errorf("第一播放组解析异常: %+v", groups[0])
	}
	if groups[1].From != "qq" || len(groups[1].Episodes) != 2 {
		t.Errorf("第二播放组解析异常: %+v", groups[1])
	}

	// 通过 VideoService 子资源接口取播放组
	svcV := NewVideoService(st.Videos, st.Sources)
	got, err := svcV.GetPlayGroups(v.ID)
	if err != nil {
		t.Fatalf("GetPlayGroups 失败: %v", err)
	}
	if len(got) != 2 || got[0].From != "youku" {
		t.Errorf("GetPlayGroups 返回异常: %+v", got)
	}
	// 不存在的影片返回错误（HTTP 层映射为 404），存在但无播放地址才返回空切片
	if _, err := svcV.GetPlayGroups(99999); err == nil {
		t.Errorf("不存在影片的播放组应返回错误")
	}
}

// 站点接口在默认 ac=videolist 下只返回精简字段、ac=detail 才带播放地址时，
// 采集应自动补齐播放数据；显式配置了 ac 的站点则尊重配置并给出诊断提示。
func TestCrawlPlayURLCompatAndAutoDetect(t *testing.T) {
	var mu sync.Mutex
	var acs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ac := r.URL.Query().Get("ac")
		mu.Lock()
		acs = append(acs, ac)
		mu.Unlock()
		item := map[string]interface{}{
			"vod_id": 1, "vod_name": "测试片", "vod_time": "2026-11-07 10:00:00",
			"type_id": 1, "type_name": "动作片",
		}
		if ac == "detail" {
			item["vod_play_from"] = "m3u8"
			item["vod_play_url"] = "第1集$https://x/1.m3u8"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "数据列表", "page": 1, "pagecount": 1,
			"limit": "20", "total": 1, "list": []interface{}{item},
		})
	}))
	defer srv.Close()

	// 场景 1：默认动作无播放地址 → 自动切到 ac=detail，播放地址正常入库
	st, svc := newCrawlService(t)
	id := addSource(t, st, srv.URL+"/api.php/provide/vod/", true)
	res, err := svc.RunOne(context.Background(), id, model.ModeFull)
	if err != nil {
		t.Fatal(err)
	}
	if res.AutoAC != "detail" {
		t.Fatalf("应自动切换到 ac=detail，实际 %q", res.AutoAC)
	}
	if res.NoPlayURL != 0 {
		t.Fatalf("补齐后不应再有无播放地址记录，实际 %d", res.NoPlayURL)
	}
	if !strings.Contains(res.Message, "ac=detail") {
		t.Errorf("任务消息应说明自动切换，实际 %q", res.Message)
	}
	vids, _, err := st.Videos.ListVideos(repository.NewVideoQuery(0, "", 1, 20))
	if err != nil {
		t.Fatal(err)
	}
	if len(vids) != 1 || vids[0].VodPlayList == "" || !strings.Contains(vids[0].VodPlayURL, "https://x/1.m3u8") {
		t.Fatalf("播放地址未入库: %+v", vids)
	}
	mu.Lock()
	got := append([]string(nil), acs...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "videolist" || got[1] != "detail" {
		t.Fatalf("应先按默认动作(videolist)探测、再按 detail 采集，实际 %v", got)
	}

	// 场景 2：显式 ac=list → 尊重配置、不自动切换、给出可操作诊断
	st2, svc2 := newCrawlService(t)
	src2 := &model.Source{
		Name: "精简站", APIBase: srv.URL + "/api.php/provide/vod/", Format: "seacms_json",
		Enabled: true, ExtraParams: "ac=list",
	}
	if err := st2.Sources.CreateSource(src2); err != nil {
		t.Fatal(err)
	}
	res2, err := svc2.RunOne(context.Background(), src2.ID, model.ModeFull)
	if err != nil {
		t.Fatal(err)
	}
	if res2.AutoAC != "" {
		t.Fatalf("显式指定 ac 时不应自动切换，实际 %q", res2.AutoAC)
	}
	if res2.NoPlayURL != 1 {
		t.Fatalf("精简响应应统计 1 条无播放地址，实际 %d", res2.NoPlayURL)
	}
	if !strings.Contains(res2.Message, "ac=list") || !strings.Contains(res2.Message, "未返回播放地址") {
		t.Errorf("任务消息应给出 ac 排查提示，实际 %q", res2.Message)
	}
}
