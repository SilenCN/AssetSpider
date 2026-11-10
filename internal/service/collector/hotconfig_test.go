package collector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/model"
)

// TestFetcherHotConfig 验证在线更新 config.Store 后，下一次请求立即用上新 UA
// 与 pagesize，无需重建 Fetcher。
func TestFetcherHotConfig(t *testing.T) {
	var gotUA, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":1,"list":[],"pagecount":1}`))
	}))
	defer srv.Close()

	cfg := config.Default()
	base := cfg.Collector
	store := config.NewStore(cfg)
	f := NewFetcher(base, store)
	src := model.Source{APIBase: srv.URL + "/api.php/provide/vod/"}

	if _, err := f.FetchList(context.Background(), src, 1, 0); err != nil {
		t.Fatal(err)
	}
	if gotUA != base.UserAgent {
		t.Fatalf("初始 UA 不符: %q", gotUA)
	}
	if strings.Contains(gotQuery, "pagesize") {
		t.Fatalf("初始 pagesize=0 不应写入查询串: %s", gotQuery)
	}

	// 在线改 UA 与 pagesize。
	in := store.Collector()
	in.UserAgent = "HotBot/9.9"
	in.PageSize = 50
	if _, err := store.UpdateCollector(in); err != nil {
		t.Fatal(err)
	}

	if _, err := f.FetchList(context.Background(), src, 2, 0); err != nil {
		t.Fatal(err)
	}
	if gotUA != "HotBot/9.9" {
		t.Fatalf("即时生效的 UA 未应用: %q", gotUA)
	}
	if !strings.Contains(gotQuery, "pagesize=50") {
		t.Fatalf("即时生效的 pagesize 未应用: %s", gotQuery)
	}
}
