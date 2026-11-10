package collector

import (
	"context"
	"encoding/json"
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

// Fetcher 负责按苹果CMS V10 Provide 协议请求采集接口并解析 JSON。
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

// BuildURL 按苹果CMS V10 Provide 协议拼装某一页的请求地址。
//
// 默认使用 ac=videolist，只有该动作才会返回包含 vod_play_url 的完整字段
// （ac=list 是精简字段、不含播放地址，无法解析播放组）。站点附加参数可覆盖 ac，
// 因此仍可用 ExtraParams=ac=list 兼容只支持精简接口的旧站。
// pg、h 由采集器强制写入；pagesize 仅在站点未自定义时按配置写入（上限通常 100）。
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
		q.Set("ac", "videolist")
	}
	q.Set("pg", strconv.Itoa(page))
	if hours > 0 {
		q.Set("h", strconv.Itoa(hours))
	} else {
		q.Del("h")
	}
	if cfg.PageSize > 0 && q.Get("pagesize") == "" && q.Get("limit") == "" {
		q.Set("pagesize", strconv.Itoa(cfg.PageSize))
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

// ---------- 苹果CMS 分类定义接口（ac=typelist） ----------

// TypeItem 对应苹果CMS ac=typelist 中 list[] 的一条分类定义。
// videolist 只携带影片自身的 type_name、不携带父级分类名称，因此父级（纯容器）
// 分类在 EnsureForVideos 里只能以 type_id 兜底展示；typelist 一次性给出本站全部
// 分类的权威名称与父子关系（type_pid），是补全父级名称、校正层级最可靠的来源。
// 字段统一用 flexString：真实站点的 type_id / type_pid 常是数字而非字符串，
// 例如 {"type_id":8,"type_pid":0,"type_name":"连续剧"}，直接用 string 会解析失败。
type TypeItem struct {
	TypeID   flexString `json:"type_id"`
	TypeName flexString `json:"type_name"`
	TypePID  flexString `json:"type_pid"`
	TypeSort flexString `json:"type_sort"`
}

// TypeListResponse 对应 api.php/provide/vod/?ac=typelist 的 JSON 响应。
// 部分站点不认 ac=typelist，仍返回影片列表，但会附带 class[] 分类定义；
// 因此两种载体都要收，class 优先。
type TypeListResponse struct {
	Code  flexInt    `json:"code"`
	Msg   flexString `json:"msg"`
	List  []TypeItem `json:"list"`
	Class []TypeItem `json:"class"`
}

// BuildTypeListURL 按苹果CMS V10 Provide 协议拼装分类定义请求地址。
// 强制 ac=typelist（覆盖站点附加参数里可能写的 videolist），不带分页/时间参数。
func BuildTypeListURL(src model.Source) (string, error) {
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
	q.Set("ac", "typelist") // 强制分类定义接口，覆盖附加参数里的其它 ac
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// FetchTypeList 请求苹果CMS 的分类定义接口（ac=typelist）并解析。
// 返回失败（网络错误、非 200、code != 1、JSON 解析失败）由调用方按「非致命」处理：
// 拿不到分类定义时，分类名称退回由 videolist 提供或 type_id 兜底，采集不阻断。
func (f *Fetcher) FetchTypeList(ctx context.Context, src model.Source) (*TypeListResponse, error) {
	rawURL, err := BuildTypeListURL(src)
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
		return nil, fmt.Errorf("请求分类接口失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("分类接口返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("读取分类响应失败: %w", err)
	}
	r, err := ParseTypeList(body)
	if err != nil {
		return nil, fmt.Errorf("%w（响应开头: %.120s）", err, string(body))
	}
	return r, nil
}

// ParseTypeList 解析 ac=typelist 的 JSON 响应。
func ParseTypeList(data []byte) (*TypeListResponse, error) {
	var r TypeListResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("解析分类接口 JSON 失败: %w", err)
	}
	if r.Code != 1 {
		return nil, fmt.Errorf("分类接口返回非成功状态 code=%d msg=%s", r.Code, r.Msg.String())
	}
	return &r, nil
}
