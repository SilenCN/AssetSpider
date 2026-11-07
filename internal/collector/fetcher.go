package collector

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/model"
)

const maxResponseBytes = 64 << 20 // 单页响应最大 64MB

// Fetcher 负责按站点配置请求采集接口并解析 JSON。
type Fetcher struct {
	client *http.Client
	cfg    config.CollectorConfig
}

// NewFetcher 创建采集 HTTP 客户端。
func NewFetcher(cfg config.CollectorConfig) *Fetcher {
	return &Fetcher{
		client: &http.Client{Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second},
		cfg:    cfg,
	}
}

// BuildURL 按海洋CMS 系协议拼装某一页的请求地址。
//
// 参数优先级：站点附加参数可覆盖 ac；pg、h 由采集器强制写入。
// hours > 0 时附加 h=<小时数>，即接口侧的"几小时内数据"增量过滤。
func BuildURL(src model.Source, page, hours int, cfg config.CollectorConfig) (string, error) {
	u, err := url.Parse(strings.TrimSpace(src.APIBase))
	if err != nil {
		return "", fmt.Errorf("站点地址无效: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("站点地址需为完整 http(s) URL: %q", src.APIBase)
	}

	q := u.Query()
	if extra := strings.TrimPrefix(strings.TrimSpace(src.ExtraParams), "?"); extra != "" {
		pairs, err := url.ParseQuery(extra)
		if err != nil {
			return "", fmt.Errorf("站点附加参数无效: %w", err)
		}
		for k, vs := range pairs {
			q[k] = vs
		}
	}
	if q.Get("ac") == "" {
		q.Set("ac", "list")
	}
	q.Set("pg", strconv.Itoa(page))
	if hours > 0 {
		q.Set("h", strconv.Itoa(hours))
	} else {
		q.Del("h")
	}
	if cfg.PageSize > 0 && q.Get("limit") == "" && q.Get("pagesize") == "" {
		q.Set("limit", strconv.Itoa(cfg.PageSize))
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// FetchList 请求指定页并返回解析后的列表响应。
func (f *Fetcher) FetchList(ctx context.Context, src model.Source, page, hours int) (*ListResponse, error) {
	rawURL, err := BuildURL(src, page, hours, f.cfg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	ua := f.cfg.UserAgent
	if ua == "" {
		ua = "AssetSpider/1.0"
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json, */*")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求采集接口失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("采集接口返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("读取采集响应失败: %w", err)
	}
	r, err := ParseList(body)
	if err != nil {
		return nil, fmt.Errorf("%w（响应开头: %.120s）", err, string(body))
	}
	return r, nil
}
