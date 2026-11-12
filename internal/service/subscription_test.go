package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

func newSubscriptionService(t *testing.T) (*repository.Repositories, *SubscriptionService) {
	t.Helper()
	st, err := repository.Open(filepath.Join(t.TempDir(), "subscription.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, NewSubscriptionService(st.Subscriptions, st.Sources, config.Default().Collector)
}

// subscriptionServer 是一个内容可替换的订阅地址：喂什么 JSON 就返回什么。
func subscriptionServer(t *testing.T, payload *string, status *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != nil && *status != http.StatusOK {
			http.Error(w, "boom", *status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(*payload))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sourceBundle(t *testing.T, items ...map[string]interface{}) string {
	t.Helper()
	b, err := json.Marshal(map[string]interface{}{"version": 1, "sources": items})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func subscriptionSite(name, apiBase string) map[string]interface{} {
	return map[string]interface{}{"name": name, "apiBase": apiBase}
}

func sourcesByName(t *testing.T, st *repository.Repositories) map[string]model.Source {
	t.Helper()
	list, err := st.Sources.ListSources()
	if err != nil {
		t.Fatal(err)
	}
	out := make(map[string]model.Source, len(list))
	for _, src := range list {
		out[src.Name] = src
	}
	return out
}

// TestSubscriptionRefreshSyncsSources 覆盖订阅的一次完整生命周期：
// 首次刷新新建站点、再次刷新按接口地址更新、订阅里被删掉的站点只停用不删除。
func TestSubscriptionRefreshSyncsSources(t *testing.T) {
	payload := sourceBundle(t,
		subscriptionSite("站点甲", "https://a.example.com/api.php/provide/vod/"),
		subscriptionSite("站点乙", "https://b.example.com/api.php/provide/vod/"),
	)
	srv := subscriptionServer(t, &payload, nil)

	st, svc := newSubscriptionService(t)
	sub, err := svc.Create(SubscriptionInput{Name: "公共订阅", URL: srv.URL + "/sources.json", IntervalHours: 6})
	if err != nil {
		t.Fatal(err)
	}

	res, err := svc.Refresh(sub.ID)
	if err != nil {
		t.Fatalf("首次刷新失败: %v", err)
	}
	if res.Total != 2 || res.Created != 2 || res.Updated != 0 {
		t.Fatalf("首次刷新结果 = %+v，期望新增 2 个", res)
	}
	byName := sourcesByName(t, st)
	if len(byName) != 2 {
		t.Fatalf("站点数量 = %d，期望 2", len(byName))
	}
	if byName["站点甲"].SubscriptionID != sub.ID {
		t.Fatalf("新建站点未标记订阅归属: %+v", byName["站点甲"])
	}
	// 订阅里写的是默认格式，站点应能直接用于采集。
	if byName["站点甲"].Format != model.FormatMacCMSJSON {
		t.Fatalf("默认格式 = %q，期望 %q", byName["站点甲"].Format, model.FormatMacCMSJSON)
	}

	// 第二次：站点甲改名（地址不变，按接口地址匹配），站点乙从订阅里消失。
	payload = sourceBundle(t,
		map[string]interface{}{
			"name": "站点甲（新）", "apiBase": "https://a.example.com/api.php/provide/vod",
			"extraParams": "t=1", "incrementalHours": 12,
		},
	)
	res, err = svc.Refresh(sub.ID)
	if err != nil {
		t.Fatalf("二次刷新失败: %v", err)
	}
	if res.Updated != 1 || res.Disabled != 1 || res.Created != 0 {
		t.Fatalf("二次刷新结果 = %+v，期望更新 1 / 停用 1", res)
	}
	byName = sourcesByName(t, st)
	if _, ok := byName["站点甲（新）"]; !ok {
		t.Fatalf("按接口地址匹配失败，站点没有改名: %+v", byName)
	}
	if byName["站点甲（新）"].ExtraParams != "t=1" || byName["站点甲（新）"].IncrementalHours != 12 {
		t.Fatalf("订阅字段没有写回: %+v", byName["站点甲（新）"])
	}
	// 只停用、不删除：影片与采集记录都挂在站点上，删掉就找不回来了。
	if gone, ok := byName["站点乙"]; !ok || gone.Enabled {
		t.Fatalf("订阅里移除的站点应保留但停用: %+v", gone)
	}

	// 重复刷新不应反复「停用」同一批站点（计数会一直涨，用户看着像每次都在改）。
	res, err = svc.Refresh(sub.ID)
	if err != nil {
		t.Fatalf("三次刷新失败: %v", err)
	}
	if res.Disabled != 0 || res.Updated != 1 {
		t.Fatalf("三次刷新结果 = %+v，期望只更新 1 个、不再重复停用", res)
	}
}

// TestSubscriptionRefreshKeepsLocalEnabled 验证订阅不会把用户手动停用的站点重新打开。
func TestSubscriptionRefreshKeepsLocalEnabled(t *testing.T) {
	payload := sourceBundle(t, subscriptionSite("站点甲", "https://a.example.com/api.php/provide/vod/"))
	srv := subscriptionServer(t, &payload, nil)

	st, svc := newSubscriptionService(t)
	sub, err := svc.Create(SubscriptionInput{Name: "公共订阅", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Refresh(sub.ID); err != nil {
		t.Fatal(err)
	}
	src := sourcesByName(t, st)["站点甲"]
	off := false
	if _, err := NewSourceService(st.Sources).Update(src.ID, SourceInput{
		Name: src.Name, APIBase: src.APIBase, Enabled: &off,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Refresh(sub.ID); err != nil {
		t.Fatal(err)
	}
	if again := sourcesByName(t, st)["站点甲"]; again.Enabled {
		t.Fatal("订阅刷新不应把本地停用的站点重新启用")
	}
}

// TestSubscriptionRefreshReportsFailure 验证拉取失败时错误与状态都会落到订阅上，
// 页面因此能直接显示「为什么没刷上」。
func TestSubscriptionRefreshReportsFailure(t *testing.T) {
	payload := "{}"
	status := http.StatusInternalServerError
	srv := subscriptionServer(t, &payload, &status)

	st, svc := newSubscriptionService(t)
	sub, err := svc.Create(SubscriptionInput{Name: "坏订阅", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Refresh(sub.ID); err == nil {
		t.Fatal("HTTP 500 应视为刷新失败")
	} else if !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("失败原因应包含状态码，实际: %v", err)
	}

	got, err := st.Subscriptions.GetSubscription(sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastStatus != SubscriptionFailed || !strings.Contains(got.LastError, "HTTP 500") {
		t.Fatalf("订阅未记录失败状态: %+v", got)
	}
	if got.LastSyncAt == nil {
		t.Fatal("失败也应记录时间，否则坏地址会被每分钟重试")
	}

	// 地址返回的不是站点配置 JSON：错误文案要说清楚，而不是抛出解析器的原文。
	status = http.StatusOK
	payload = "<html>not json</html>"
	if _, err := svc.Refresh(sub.ID); err == nil || !strings.Contains(err.Error(), "合法的站点配置 JSON") {
		t.Fatalf("非 JSON 内容的失败原因不够明确: %v", err)
	}
}

// TestSubscriptionDue 验证自动刷新的触发判据：间隔未到不刷，到点才刷。
func TestSubscriptionDue(t *testing.T) {
	payload := sourceBundle(t, subscriptionSite("站点甲", "https://a.example.com/api.php/provide/vod/"))
	srv := subscriptionServer(t, &payload, nil)

	_, svc := newSubscriptionService(t)
	sub, err := svc.Create(SubscriptionInput{Name: "定时订阅", URL: srv.URL, IntervalHours: 2})
	if err != nil {
		t.Fatal(err)
	}
	// 刚创建：距创建时间还不到间隔，不该触发。
	if n := svc.RefreshDue(time.Now()); n != 0 {
		t.Fatalf("未到间隔就刷新了 %d 个订阅", n)
	}
	// 把上次刷新时间推到 3 小时前（等价于「早就该刷了」）。
	old := time.Now().Add(-3 * time.Hour)
	if err := svcRefreshAt(svc, sub.ID, old); err != nil {
		t.Fatal(err)
	}
	if n := svc.RefreshDue(time.Now()); n != 1 {
		t.Fatalf("到点应刷新 1 个订阅，实际 %d", n)
	}
	// 刷新后 LastSyncAt 更新，下一分钟不该再刷。
	if n := svc.RefreshDue(time.Now()); n != 0 {
		t.Fatalf("刚刷新完不应重复刷新，实际 %d", n)
	}

	// 停用后即使到期也不刷。
	off := false
	if _, err := svc.Update(sub.ID, SubscriptionInput{Name: "定时订阅", URL: srv.URL, IntervalHours: 2, Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if err := svcRefreshAt(svc, sub.ID, old); err != nil {
		t.Fatal(err)
	}
	if n := svc.RefreshDue(time.Now()); n != 0 {
		t.Fatalf("停用的订阅不应自动刷新，实际 %d", n)
	}
}

// svcRefreshAt 把订阅的上次刷新时间改成 at，用于模拟「已经过了很久」。
func svcRefreshAt(svc *SubscriptionService, id uint, at time.Time) error {
	return svc.subs.SaveSubscriptionResult(id, SubscriptionSuccess, "", at, repository.SubscriptionStats{})
}

// TestSubscriptionSkipsNameTakenByManualSource 验证撞名时跳过而不是覆盖：
// 手动添加的站点是用户自己的配置，不该被一次订阅刷新改掉。
func TestSubscriptionSkipsNameTakenByManualSource(t *testing.T) {
	payload := sourceBundle(t,
		subscriptionSite("手动的站点", "https://sub.example.com/api.php/provide/vod/"),
		subscriptionSite("站点甲", "https://a.example.com/api.php/provide/vod/"),
	)
	srv := subscriptionServer(t, &payload, nil)

	st, svc := newSubscriptionService(t)
	manual := &model.Source{Name: "手动的站点", APIBase: "https://manual.example.com/api.php/provide/vod/", Enabled: true}
	if err := st.Sources.CreateSource(manual); err != nil {
		t.Fatal(err)
	}
	sub, err := svc.Create(SubscriptionInput{Name: "公共订阅", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.Refresh(sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 1 || res.Skipped != 1 || len(res.Issues) != 1 {
		t.Fatalf("撞名应跳过 1 条，实际 %+v", res)
	}
	if !strings.Contains(res.Issues[0].Error, "同名站点已存在") {
		t.Fatalf("跳过原因不清楚: %s", res.Issues[0].Error)
	}
	got := sourcesByName(t, st)["手动的站点"]
	if got.APIBase != manual.APIBase || got.SubscriptionID != 0 {
		t.Fatalf("手动站点被订阅改写了: %+v", got)
	}
}

// TestSubscriptionDeleteDetachesSources 验证删订阅只解绑，不删站点。
func TestSubscriptionDeleteDetachesSources(t *testing.T) {
	payload := sourceBundle(t, subscriptionSite("站点甲", "https://a.example.com/api.php/provide/vod/"))
	srv := subscriptionServer(t, &payload, nil)

	st, svc := newSubscriptionService(t)
	sub, err := svc.Create(SubscriptionInput{Name: "公共订阅", URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Refresh(sub.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(sub.ID); err != nil {
		t.Fatal(err)
	}
	src, ok := sourcesByName(t, st)["站点甲"]
	if !ok {
		t.Fatal("删除订阅不应删除站点")
	}
	if src.SubscriptionID != 0 {
		t.Fatalf("站点应解绑为手动站点，实际 subscriptionId=%d", src.SubscriptionID)
	}
}
