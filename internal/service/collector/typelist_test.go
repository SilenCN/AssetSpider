package collector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/model"
)

// TestParseTypeList 验证 ac=typelist 响应的解析：code==1 成功解析、含父子关系。
func TestParseTypeList(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"code": 1, "msg": "", "page": 1, "pagecount": 1,
		"list": []map[string]interface{}{
			{"type_id": "1", "type_name": "电影", "type_pid": "0"},
			{"type_id": "6", "type_name": "动作", "type_pid": "1"},
			{"type_id": "7", "type_name": "喜剧", "type_pid": "1"},
		},
	})
	r, err := ParseTypeList(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.List) != 3 {
		t.Fatalf("应解析出 3 个分类，实际 %d", len(r.List))
	}
	if r.List[0].TypeID != "1" || r.List[0].TypeName != "电影" || r.List[0].TypePID != "0" {
		t.Fatalf("首条分类解析异常: %+v", r.List[0])
	}
	if r.List[1].TypePID != "1" {
		t.Fatalf("二级分类父级关系解析异常: %+v", r.List[1])
	}
}

// TestParseListClassNumericTypeIDs 验证真实站点把分类定义放在影片响应的
// class[] 里、且 type_id / type_pid 是数字时也能解析出来。
func TestParseListClassNumericTypeIDs(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"code": 1, "msg": "数据列表", "page": 1, "pagecount": 1, "limit": "20", "total": 1,
		"list": []map[string]interface{}{
			{"vod_id": 1, "vod_name": "剧集A", "type_id": 26, "type_name": "国产剧"},
		},
		"class": []map[string]interface{}{
			{"type_id": 8, "type_pid": 0, "type_name": "连续剧"},
			{"type_id": 26, "type_pid": 8, "type_name": "国产剧"},
		},
	})
	r, err := ParseList(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Class) != 2 {
		t.Fatalf("应解析出 2 条 class 分类定义，实际 %d", len(r.Class))
	}
	if r.Class[0].TypeID.String() != "8" || r.Class[0].TypePID.String() != "0" || r.Class[0].TypeName.String() != "连续剧" {
		t.Fatalf("数字 type_id/type_pid 解析异常: %+v", r.Class[0])
	}
	if r.Class[1].TypePID.String() != "8" {
		t.Fatalf("class 父子关系解析异常: %+v", r.Class[1])
	}
}

// TestParseTypeListClassFallback 验证 ac=typelist 返回影片列表 + class[] 时，
// class[] 仍能被取到（TypeItem 兼容数字字段，不会解析失败）。
func TestParseTypeListClassFallback(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"code": 1, "msg": "数据列表",
		"list":  []map[string]interface{}{{"vod_id": 1, "type_id": 26, "type_name": "国产剧"}},
		"class": []map[string]interface{}{{"type_id": 8, "type_pid": 0, "type_name": "连续剧"}},
	})
	r, err := ParseTypeList(body)
	if err != nil {
		t.Fatalf("数字 type_id 不应导致解析失败: %v", err)
	}
	if len(r.Class) != 1 || r.Class[0].TypeName.String() != "连续剧" {
		t.Fatalf("class[] 未被解析: %+v", r)
	}
}

// TestParseTypeListNonSuccess 验证非成功状态被识别为错误。
func TestParseTypeListNonSuccess(t *testing.T) {
	body, _ := json.Marshal(map[string]interface{}{
		"code": -1, "msg": "参数错误", "list": []map[string]interface{}{},
	})
	if _, err := ParseTypeList(body); err == nil {
		t.Fatal("code != 1 应返回错误")
	}
}

// TestFetchTypeList 验证 FetchTypeList 通过真实 HTTP 请求拉到分类定义，
// 且 BuildTypeListURL 强制 ac=typelist（覆盖附加参数里的 videolist）。
func TestFetchTypeList(t *testing.T) {
	var gotAC, gotExtra string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAC = r.URL.Query().Get("ac")
		gotExtra = r.URL.Query().Get("t")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "", "page": 1, "pagecount": 1,
			"list": []map[string]interface{}{
				{"type_id": "1", "type_name": "电影", "type_pid": "0"},
				{"type_id": "6", "type_name": "动作", "type_pid": "1"},
			},
		})
	}))
	defer srv.Close()

	src := model.Source{
		APIBase:     srv.URL + "/api.php/provide/vod/",
		ExtraParams: "ac=videolist&t=1",
	}
	f := NewFetcher(config.Default().Collector)
	resp, err := f.FetchTypeList(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if gotAC != "typelist" {
		t.Fatalf("应强制 ac=typelist，实际 %q", gotAC)
	}
	if gotExtra != "1" {
		t.Fatalf("附加参数 t=1 应被保留，实际 %q", gotExtra)
	}
	if len(resp.List) != 2 || resp.List[0].TypeName != "电影" {
		t.Fatalf("typelist 解析结果异常: %+v", resp.List)
	}
}

// TestFetchTypeListError 验证分类接口异常时返回错误（调用方按非致命处理）。
func TestFetchTypeListError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	src := model.Source{APIBase: srv.URL + "/api.php/provide/vod/"}
	f := NewFetcher(config.Default().Collector)
	if _, err := f.FetchTypeList(context.Background(), src); err == nil {
		t.Fatal("分类接口 500 应返回错误")
	}
}

// TestFetchTypeListPaginates 验证分类较多的站点（pagecount > 1）时，
// FetchTypeList 会翻页并合并，而不是只拿第 1 页。
// 同时验证分页硬上限为 3 页：即便站点声称更多页，也最多尝试 3 页。
func TestFetchTypeListPaginates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		pg, _ := strconv.Atoi(r.URL.Query().Get("pg"))
		if pg <= 0 {
			pg = 1
		}
		// 两页：第 1 页 2 个，第 2 页 2 个（含原本落单的二级分类之父——一级分类）。
		var payload map[string]interface{}
		if pg == 1 {
			payload = map[string]interface{}{
				"code": 1, "page": 1, "pagecount": 2, "pagesize": 100,
				"list": []map[string]interface{}{
					{"type_id": "6", "type_name": "动作", "type_pid": "1"},
					{"type_id": "7", "type_name": "喜剧", "type_pid": "1"},
				},
			}
		} else {
			payload = map[string]interface{}{
				"code": 1, "page": 2, "pagecount": 2, "pagesize": 100,
				"list": []map[string]interface{}{
					{"type_id": "1", "type_name": "电影", "type_pid": "0"},
					{"type_id": "2", "type_name": "电视剧", "type_pid": "0"},
				},
			}
		}
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer srv.Close()

	src := model.Source{APIBase: srv.URL + "/api.php/provide/vod/"}
	// PageSize=2 让每页恰好填满（2 条），从而触发真正的多页翻取；
	// 若用默认 pagesize=100，两页都不足一页会当成单页提前结束。
	f := NewFetcher(config.CollectorConfig{PageSize: 2})
	resp, err := f.FetchTypeList(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.List) != 4 {
		t.Fatalf("应合并两页共 4 个分类，实际 %d: %+v", len(resp.List), resp.List)
	}
	// 验证第 2 页的一级分类（type_pid=0）确实被拿到，而非只停留在第 1 页。
	hasFirstLevel := false
	for _, it := range resp.List {
		if it.TypeID.String() == "1" && it.TypePID.String() == "0" {
			hasFirstLevel = true
		}
	}
	if !hasFirstLevel {
		t.Fatalf("第 2 页的一级分类未合并进来: %+v", resp.List)
	}
}

// TestBuildTypeListURLPageSize 验证 BuildTypeListURL 在传入配置时写入 pagesize（上限 100）。
func TestBuildTypeListURLPageSize(t *testing.T) {
	src := model.Source{APIBase: "https://example.com/api.php/provide/vod/"}
	u, err := BuildTypeListURL(src, config.CollectorConfig{PageSize: 50})
	if err != nil {
		t.Fatal(err)
	}
	q := mustQuery(t, u)
	if q.Get("pagesize") != "50" {
		t.Fatalf("应写入 pagesize=50，实际 %q", q.Get("pagesize"))
	}
	if q.Get("ac") != "typelist" {
		t.Fatalf("应强制 ac=typelist，实际 %q", q.Get("ac"))
	}

	// PageSize 超过 100 时被钳到上限 100。
	u2, _ := BuildTypeListURL(src, config.CollectorConfig{PageSize: 500})
	if mustQuery(t, u2).Get("pagesize") != "100" {
		t.Fatalf("pagesize 超过 100 应被钳到 100")
	}

	// 不传配置时不加 pagesize（保持旧行为）。
	u3, _ := BuildTypeListURL(src)
	if mustQuery(t, u3).Get("pagesize") != "" {
		t.Fatalf("不传配置时不应写入 pagesize")
	}
}

// TestFetchTypeListFallbackToListClass 验证专用分类接口 ac=typelist 失败（HTTP 400，
// 真实大站常见）时，FetchTypeList 会通用降级到标准列表动作 ac=list，只取响应里的
// class[] 分类定义，且绝不把 list[] 里的影片当成分类。
func TestFetchTypeListFallbackToListClass(t *testing.T) {
	var gotActions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ac := r.URL.Query().Get("ac")
		gotActions = append(gotActions, ac)
		w.Header().Set("Content-Type", "application/json")
		if ac == "typelist" {
			// 模拟站点不认专用分类接口：400 + 参数错误。
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":0,"msg":"请求参数错误"}`))
			return
		}
		// 标准列表动作：list[] 是影片，class[] 才是分类定义（含 type_pid 层级、数字 ID）。
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "ok", "page": 1, "pagecount": 18,
			"list": []map[string]interface{}{
				{"vod_id": 1, "vod_name": "某部电影", "type_id": 101, "type_name": "动作片"},
			},
			"class": []map[string]interface{}{
				{"type_id": 1, "type_pid": 0, "type_name": "电影"},
				{"type_id": 2, "type_pid": 0, "type_name": "电视剧"},
				{"type_id": 101, "type_pid": 1, "type_name": "动作片"},
			},
		})
	}))
	defer srv.Close()

	src := model.Source{APIBase: srv.URL + "/api.php/provide/vod/"}
	f := NewFetcher(config.Default().Collector)
	resp, err := f.FetchTypeList(context.Background(), src)
	if err != nil {
		t.Fatalf("降级到 list.class[] 应成功，实际报错: %v", err)
	}
	if len(resp.Class) != 3 {
		t.Fatalf("应从 class[] 拿到 3 条分类定义，实际 %d: %+v", len(resp.Class), resp.Class)
	}
	if len(resp.List) != 0 {
		t.Fatalf("降级路径下 List 必须为空（影片不能当分类），实际 %d: %+v", len(resp.List), resp.List)
	}
	// 一级分类名称与层级都在。
	var firstLevel map[string]bool = make(map[string]bool)
	for _, it := range resp.Class {
		if it.TypePID.String() == "0" {
			firstLevel[it.TypeName.String()] = true
		}
	}
	if !firstLevel["电影"] || !firstLevel["电视剧"] {
		t.Fatalf("class[] 里的一级分类名称/层级未正确解析: %+v", resp.Class)
	}
	// 探测序列：先 typelist，再 list；命中即停，不应继续试 videolist。
	if len(gotActions) < 2 || gotActions[0] != "typelist" || gotActions[1] != "list" {
		t.Fatalf("降级探测动作序列异常: %v", gotActions)
	}
	for _, a := range gotActions {
		if a == "videolist" {
			t.Fatalf("list 已命中 class[]，不应再探测 videolist: %v", gotActions)
		}
	}
}

// TestFetchTypeListFallbackPrefersExplicitAC 验证站点显式指定的 ac 会被优先探测，
// 且 typelist 不会在降级序列里被重复探测。
func TestFetchTypeListFallbackPrefersExplicitAC(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ac := r.URL.Query().Get("ac")
		got = append(got, ac)
		w.Header().Set("Content-Type", "application/json")
		if ac == "typelist" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// 仅站点自定义的 custom 动作带 class[]，标准动作返回空，验证优先级。
		if ac == "custom" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 1,
				"class": []map[string]interface{}{
					{"type_id": 9, "type_pid": 0, "type_name": "自定义分类"},
				},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 1, "class": []map[string]interface{}{}})
	}))
	defer srv.Close()

	src := model.Source{
		APIBase:     srv.URL + "/api.php/provide/vod/",
		ExtraParams: "ac=custom", // 站点显式指定动作
	}
	f := NewFetcher(config.Default().Collector)
	resp, err := f.FetchTypeList(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Class) != 1 || resp.Class[0].TypeName.String() != "自定义分类" {
		t.Fatalf("应命中站点显式 ac=custom 的 class[]: %+v", resp.Class)
	}
	if len(got) < 2 || got[0] != "typelist" || got[1] != "custom" {
		t.Fatalf("应优先探测站点显式 ac=custom，实际序列: %v", got)
	}
	for _, a := range got[1:] {
		if a == "typelist" {
			t.Fatalf("typelist 不应在降级序列里重复出现: %v", got)
		}
	}
}

// TestFetchTypeListPrimarySuccessNoFallback 验证主路径成功时不会发起任何降级请求
// （不额外增加请求）。
func TestFetchTypeListPrimarySuccessNoFallback(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Query().Get("ac"))
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "page": 1, "pagecount": 1,
			"list": []map[string]interface{}{
				{"type_id": "1", "type_name": "电影", "type_pid": "0"},
			},
		})
	}))
	defer srv.Close()

	src := model.Source{APIBase: srv.URL + "/api.php/provide/vod/"}
	f := NewFetcher(config.Default().Collector)
	if _, err := f.FetchTypeList(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	for _, a := range got {
		if a != "typelist" {
			t.Fatalf("主路径成功时不应降级，实际探测了: %v", got)
		}
	}
}

// TestFetchTypeListEmptySuccessNoFallback 验证 ac=typelist 成功返回空列表时视为
// 有效应答、不触发降级（避免对正常站点多做无谓探测，也不污染采集页序列）。
func TestFetchTypeListEmptySuccessNoFallback(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Query().Get("ac"))
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "ok", "page": 1, "pagecount": 1,
			"list": []map[string]interface{}{},
		})
	}))
	defer srv.Close()

	src := model.Source{APIBase: srv.URL + "/api.php/provide/vod/"}
	f := NewFetcher(config.Default().Collector)
	resp, err := f.FetchTypeList(context.Background(), src)
	if err != nil {
		t.Fatalf("空列表是有效应答，不应报错: %v", err)
	}
	if len(resp.List) != 0 || len(resp.Class) != 0 {
		t.Fatalf("应返回空结果: %+v", resp)
	}
	for _, a := range got {
		if a != "typelist" {
			t.Fatalf("成功空列表不应降级，实际探测了: %v", got)
		}
	}
}

// TestFetchTypeListPaginatesAtMostThree 验证 ac=typelist 分页硬上限为 3 页：
// 即便站点声称 pagecount 很大且每页都是满页，也最多请求 3 页。
func TestFetchTypeListPaginatesAtMostThree(t *testing.T) {
	var pageReqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pageReqs++
		pg, _ := strconv.Atoi(r.URL.Query().Get("pg"))
		if pg <= 0 {
			pg = 1
		}
		// 每页都返回满一页（PageSize=1 → 1 条），pagecount 虚高到 99，
		// 逼迫循环直至撞上 3 页硬上限。
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "page": pg, "pagecount": 99, "pagesize": 1,
			"list": []map[string]interface{}{
				{"type_id": pg, "type_name": "分类" + strconv.Itoa(pg), "type_pid": "0"},
			},
		})
	}))
	defer srv.Close()

	src := model.Source{APIBase: srv.URL + "/api.php/provide/vod/"}
	f := NewFetcher(config.CollectorConfig{PageSize: 1})
	resp, err := f.FetchTypeList(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if pageReqs != 3 {
		t.Fatalf("ac=typelist 分页最多尝试 3 页，实际请求 %d 页", pageReqs)
	}
	if len(resp.List) != 3 {
		t.Fatalf("应合并 3 页共 3 条分类，实际 %d", len(resp.List))
	}
}

// TestBuildTypeProbeURL 验证分类降级探测 URL 的通用构造：强制 ac、移除时间窗/分页
// 参数、保留站点认证等其它附加参数。
func TestBuildTypeProbeURL(t *testing.T) {
	src := model.Source{
		APIBase:     "https://example.com/api.php/provide/vod/?h=24&pg=5&token=abc",
		ExtraParams: "pagesize=20&limit=30&key=xyz",
	}
	u, err := buildTypeProbeURL(src, "list")
	if err != nil {
		t.Fatal(err)
	}
	q := mustQuery(t, u)
	if q.Get("ac") != "list" {
		t.Fatalf("应强制 ac=list，实际 %q", q.Get("ac"))
	}
	for _, k := range []string{"h", "pg", "page", "pagesize", "limit"} {
		if q.Get(k) != "" {
			t.Fatalf("分类探测应移除参数 %s，实际 %q（URL=%s）", k, q.Get(k), u)
		}
	}
	// 非时间窗/分页的附加参数（认证等）必须保留。
	if q.Get("token") != "abc" || q.Get("key") != "xyz" {
		t.Fatalf("站点认证类附加参数应保留: %s", u)
	}
}

// TestTypeProbeActions 验证降级探测动作序列的通用性：尊重站点显式 ac、去重、
// 跳过已在主路径试过的 typelist、并以标准 list/videolist 兜底。
func TestTypeProbeActions(t *testing.T) {
	cases := []struct {
		name string
		src  model.Source
		want []string
	}{
		{
			name: "无显式ac：标准动作兜底",
			src:  model.Source{APIBase: "https://s.com/api.php/provide/vod/"},
			want: []string{"list", "videolist"},
		},
		{
			name: "显式ac优先",
			src:  model.Source{APIBase: "https://s.com/api.php/provide/vod/", ExtraParams: "ac=custom"},
			want: []string{"custom", "list", "videolist"},
		},
		{
			name: "显式typelist被跳过",
			src:  model.Source{APIBase: "https://s.com/api.php/provide/vod/?ac=typelist"},
			want: []string{"list", "videolist"},
		},
		{
			name: "APIBase与ExtraParams的ac去重",
			src: model.Source{
				APIBase:     "https://s.com/api.php/provide/vod/?ac=list",
				ExtraParams: "ac=list",
			},
			want: []string{"list", "videolist"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := typeProbeActions(c.src)
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("typeProbeActions = %v, want %v", got, c.want)
			}
		})
	}
}

func mustQuery(t *testing.T, rawURL string) url.Values {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}
