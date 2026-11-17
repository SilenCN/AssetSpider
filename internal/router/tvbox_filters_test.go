package router_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// seedTVBoxFilterData 建一个带分类、且影片含年份/地区/类型/状态字段的站点，
// 供分类展示与二次筛选测试使用。返回 sourceID 与 api key 所用站点。
func seedTVBoxFilterData(t *testing.T, dbPath string) uint {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	src := model.Source{Name: "筛选测试站", APIBase: "https://example.org/provide/vod/", Enabled: true, Format: model.FormatMacCMSJSON}
	if err := db.Create(&src).Error; err != nil {
		t.Fatal(err)
	}
	catA := model.Category{SourceID: src.ID, Key: "cat-10", TypeID: "10", Name: "动作片", Level: 1}
	catB := model.Category{SourceID: src.ID, Key: "cat-20", TypeID: "20", Name: "喜剧片", Level: 1}
	if err := db.Create(&catA).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&catB).Error; err != nil {
		t.Fatal(err)
	}
	// 4 条影片：年份/地区/类型/状态各异，便于逐维筛选断言。
	videos := []model.Video{
		{VodID: "f-1", CategoryID: catA.ID, TypeID: "10", TypeName: "动作片", VodName: "动作2024大陆", VodYear: "2024", VodArea: "中国大陆", VodClass: "动作,犯罪", VodState: "完结", VodScore: "9.0", VodHits: "100", VodTime: "2026-11-17 10:00:00", VodPlayFrom: "d", VodPlayURL: "第1集$https://v/1"},
		{VodID: "f-2", CategoryID: catA.ID, TypeID: "10", TypeName: "动作片", VodName: "动作2023香港", VodYear: "2023", VodArea: "中国香港", VodClass: "动作,悬疑", VodState: "连载中", VodScore: "8.0", VodHits: "200", VodTime: "2026-11-16 10:00:00", VodPlayFrom: "d", VodPlayURL: "第1集$https://v/2"},
		{VodID: "f-3", CategoryID: catB.ID, TypeID: "20", TypeName: "喜剧片", VodName: "喜剧2024大陆", VodYear: "2024", VodArea: "中国大陆", VodClass: "喜剧", VodState: "完结", VodScore: "7.0", VodHits: "300", VodTime: "2026-11-15 10:00:00", VodPlayFrom: "d", VodPlayURL: "第1集$https://v/3"},
		{VodID: "f-4", CategoryID: catB.ID, TypeID: "20", TypeName: "喜剧片", VodName: "喜剧2022美国", VodYear: "2022", VodArea: "美国", VodClass: "喜剧,爱情", VodState: "完结", VodScore: "6.0", VodHits: "400", VodTime: "2026-11-14 10:00:00", VodPlayFrom: "d", VodPlayURL: "第1集$https://v/4"},
	}
	for i := range videos {
		videos[i].SourceID = src.ID
		if err := db.Create(&videos[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	return src.ID
}

// TestTVBoxCategoryAndFilters 覆盖：裸 GET 返回 class + filters；filters 含排序/年份/
// 地区/类型/状态且值来自真实数据；按分类、年份、地区、类型、状态筛选都命中正确影片；
// 排序 by 生效。
func TestTVBoxCategoryAndFilters(t *testing.T) {
	r, dbPath := newTestRouter(t)
	srcID := seedTVBoxFilterData(t, dbPath)
	key := genKey(t, r)
	vodPath := fmt.Sprintf("/tvbox/%s/vod/%d", key, srcID)

	// --- 1) 裸 GET（客户端取分类+筛选面板）应带 class 和 filters ---
	w := tvboxReq(r, http.MethodGet, vodPath)
	var home struct {
		Class []struct {
			TypeID   string `json:"type_id"`
			Name     string `json:"type_name"`
			TypeFlag string `json:"type_flag"`
		} `json:"class"`
		Filters map[string][]struct {
			Key   string `json:"key"`
			Name  string `json:"name"`
			Value []struct {
				N string `json:"n"`
				V string `json:"v"`
			} `json:"value"`
		} `json:"filters"`
	}
	decode(t, w, &home)
	if len(home.Class) != 2 {
		t.Fatalf("裸 GET 应返回 2 个分类: %s", w.Body.String())
	}
	filters, ok := home.Filters["*"]
	if !ok || len(filters) == 0 {
		t.Fatalf("裸 GET 应返回 filters[*]: %s", w.Body.String())
	}
	keys := map[string]bool{}
	for _, f := range filters {
		keys[f.Key] = len(f.Value) > 0
	}
	for _, want := range []string{"by", "year", "area", "class", "state"} {
		if !keys[want] {
			t.Fatalf("筛选面板缺少有值的 key=%s: %s", want, w.Body.String())
		}
	}
	// 类型筛选把 "动作,犯罪" 拆成独立值。
	var classFilter []struct {
		N string `json:"n"`
		V string `json:"v"`
	}
	for _, f := range filters {
		if f.Key == "class" {
			for _, opt := range f.Value {
				classFilter = append(classFilter, struct {
					N string `json:"n"`
					V string `json:"v"`
				}{N: opt.N, V: opt.V})
			}
		}
	}
	foundAction := false
	for _, opt := range classFilter {
		if opt.V == "动作" {
			foundAction = true
		}
		if strings.Contains(opt.V, ",") {
			t.Fatalf("类型筛选值应是拆分后的单词，实际 %q", opt.V)
		}
	}
	if !foundAction {
		t.Fatalf("类型筛选应含拆分值'动作': %v", classFilter)
	}

	// --- 2) 按分类 t=10 只返回动作片 2 条 ---
	w = tvboxReq(r, http.MethodGet, vodPath+"?ac=detail&t=10&pg=1")
	var list struct {
		List []struct {
			VodID string `json:"vod_id"`
		} `json:"list"`
	}
	decode(t, w, &list)
	if len(list.List) != 2 {
		t.Fatalf("t=10 应返回 2 条动作片: %s", w.Body.String())
	}

	// --- 3) 按年份 year=2024 命中 f-1 / f-3 ---
	w = tvboxReq(r, http.MethodGet, vodPath+"?ac=detail&year=2024&pg=1")
	list = struct {
		List []struct {
			VodID string `json:"vod_id"`
		} `json:"list"`
	}{}
	decode(t, w, &list)
	if len(list.List) != 2 || !hasVod(list.List, "f-1") || !hasVod(list.List, "f-3") {
		t.Fatalf("year=2024 应命中 f-1/f-3: %s", w.Body.String())
	}

	// --- 4) 按地区 area=香港 命中 f-2（模糊匹配） ---
	w = tvboxReq(r, http.MethodGet, vodPath+"?ac=detail&area=%E9%A6%99%E6%B8%AF&pg=1")
	list = struct {
		List []struct {
			VodID string `json:"vod_id"`
		} `json:"list"`
	}{}
	decode(t, w, &list)
	if len(list.List) != 1 || !hasVod(list.List, "f-2") {
		t.Fatalf("area=香港 应命中 f-2: %s", w.Body.String())
	}

	// --- 5) 类型 class=悬疑 命中 f-2（多值串模糊匹配） ---
	w = tvboxReq(r, http.MethodGet, vodPath+"?ac=detail&class=%E6%82%AC%E7%96%91&pg=1")
	list = struct {
		List []struct {
			VodID string `json:"vod_id"`
		} `json:"list"`
	}{}
	decode(t, w, &list)
	if len(list.List) != 1 || !hasVod(list.List, "f-2") {
		t.Fatalf("class=悬疑 应命中 f-2: %s", w.Body.String())
	}

	// --- 6) 状态 state=完结 命中 3 条 ---
	w = tvboxReq(r, http.MethodGet, vodPath+"?ac=detail&state=%E5%AE%8C%E7%BB%93&pg=1")
	list = struct {
		List []struct {
			VodID string `json:"vod_id"`
		} `json:"list"`
	}{}
	decode(t, w, &list)
	if len(list.List) != 3 {
		t.Fatalf("state=完结 应命中 3 条: %s", w.Body.String())
	}

	// --- 7) 排序 by=hits_desc 首条应是 hits=400 的 f-4 ---
	w = tvboxReq(r, http.MethodGet, vodPath+"?ac=detail&by=hits_desc&pg=1")
	list = struct {
		List []struct {
			VodID string `json:"vod_id"`
		} `json:"list"`
	}{}
	decode(t, w, &list)
	if len(list.List) == 0 || list.List[0].VodID != "f-4" {
		t.Fatalf("by=hits_desc 首条应是 f-4: %s", w.Body.String())
	}

	// --- 8) 分类 + 筛选叠加：t=10&year=2023 只命中 f-2 ---
	w = tvboxReq(r, http.MethodGet, vodPath+"?ac=detail&t=10&year=2023&pg=1")
	list = struct {
		List []struct {
			VodID string `json:"vod_id"`
		} `json:"list"`
	}{}
	decode(t, w, &list)
	if len(list.List) != 1 || !hasVod(list.List, "f-2") {
		t.Fatalf("t=10&year=2023 应只命中 f-2: %s", w.Body.String())
	}
}

func hasVod(list []struct {
	VodID string `json:"vod_id"`
}, id string) bool {
	for _, v := range list {
		if v.VodID == id {
			return true
		}
	}
	return false
}
