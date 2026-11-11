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
//
// cfg 是构造时的初始快照；store 非空时，UA / 超时 / pagesize 等每次请求都读最新值，
// 让设置页的在线修改即时生效。超时不放在共享 client.Timeout 上（并发改会有数据竞争），
// 而是每次请求用 context.WithTimeout 套一层。
type Fetcher struct {
	client *http.Client
	cfg    config.CollectorConfig
	store  *config.Store
}

// NewFetcher 创建采集 HTTP 客户端。store 可选，用于配置热更新。
func NewFetcher(cfg config.CollectorConfig, store ...*config.Store) *Fetcher {
	f := &Fetcher{client: &http.Client{}, cfg: cfg}
	if len(store) > 0 {
		f.store = store[0]
	}
	return f
}

// SetStore 在运行期注入配置源；注入后所有请求读最新配置。
func (f *Fetcher) SetStore(store *config.Store) { f.store = store }

// current 返回当前采集配置快照。
func (f *Fetcher) current() config.CollectorConfig {
	if f.store != nil {
		return f.store.Collector()
	}
	return f.cfg
}

// requestContext 按当前超时配置派生一个带超时的 context。
func (f *Fetcher) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	seconds := f.current().TimeoutSeconds
	if seconds <= 0 {
		seconds = 20
	}
	return context.WithTimeout(ctx, time.Duration(seconds)*time.Second)
}

// userAgent 返回当前配置的 UA（空则用默认值）。
func (f *Fetcher) userAgent() string {
	ua := f.current().UserAgent
	if ua == "" {
		return "AssetSpider/1.0"
	}
	return ua
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
	rawURL, err := BuildURL(src, page, hours, f.current())
	if err != nil {
		return nil, err
	}
	reqCtx, cancel := f.requestContext(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", f.userAgent())
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
//
// PageCount 是苹果CMS 的分类分页总页数：typelist 接口默认每页约 20 条、上限 100，
// 分类很多的站点（如「巨量资源」这类大资源站）pagecount > 1，只拉第 1 页会丢掉
// 后续页的分类（含落在后页的一级分类），导致一级分类「拿不到」。FetchTypeList 必须
// 翻完所有页再合并。
type TypeListResponse struct {
	Code      flexInt    `json:"code"`
	Msg       flexString `json:"msg"`
	PageCount flexInt    `json:"pagecount"`
	List      []TypeItem `json:"list"`
	Class     []TypeItem `json:"class"`
}

// BuildTypeListURL 按苹果CMS V10 Provide 协议拼装分类定义请求地址。
// 强制 ac=typelist（覆盖站点附加参数里可能写的 videolist），不带分页/时间参数。
// cfg 可选：传入时按采集配置写入 pagesize（上限 100），减少大资源站所需翻页数；
// 不传则不加 pagesize，使用站点默认值（保持与旧行为一致，测试可省略）。
func BuildTypeListURL(src model.Source, cfg ...config.CollectorConfig) (string, error) {
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
	if len(cfg) > 0 {
		ps := cfg[0].PageSize
		if ps <= 0 {
			ps = 100
		} else if ps > 100 {
			ps = 100 // 苹果CMS typelist 的 pagesize 上限为 100
		}
		if q.Get("pagesize") == "" && q.Get("limit") == "" {
			q.Set("pagesize", strconv.Itoa(ps))
		}
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// FetchTypeList 请求苹果CMS 的分类定义接口（ac=typelist）并解析。
//
// 关键点：typelist 是分页接口（默认 pagesize 约 20、上限 100）。分类很多的站点
// pagecount > 1，只拉第 1 页会丢掉后续页的分类——含落在后页的一级分类，使一级分类
// 永远「拿不到」。因此这里按 pagecount 翻完所有页，合并各页的 list[] 与 class[]。
// 翻页以 type_id 去重，避免站点分页实现异常（各页返回重复首项）时无限膨胀。
//
// 返回失败（网络错误、非 200、code != 1、JSON 解析失败）由调用方按「非致命」处理：
// 拿不到分类定义时，分类名称退回由 videolist 提供或 type_id 兜底，采集不阻断。
// 仅第 1 页失败视为「接口整体不可用」直接报错；后续页失败只停止翻页（已拿到的不丢）。
func (f *Fetcher) FetchTypeList(ctx context.Context, src model.Source) (*TypeListResponse, error) {
	cfg := f.current()
	merged := &TypeListResponse{}
	seenList := make(map[string]bool)
	seenClass := make(map[string]bool)
	// 每页条数：用于「末页判定」。当某页返回条数不足一页时，即使站点声称
	// pagecount 更大（异常值）也视为已到末页，避免按虚假 pagecount 空转翻页。
	pageSize := cfg.PageSize
	if pageSize <= 0 {
		pageSize = 100
	} else if pageSize > 100 {
		pageSize = 100 // 苹果CMS typelist 的 pagesize 上限为 100
	}
	const maxTypePages = 200 // 翻页硬上限，防止站点 pagecount 异常导致死循环

	for pg := 1; pg <= maxTypePages; pg++ {
		rawURL, err := BuildTypeListURL(src, cfg)
		if err != nil {
			if pg == 1 {
				return nil, err
			}
			break
		}
		// 追加页码；站点不认 pg 时自然只返回第 1 页（pagecount==1），循环一次结束。
		u, err := url.Parse(rawURL)
		if err != nil {
			if pg == 1 {
				return nil, err
			}
			break
		}
		q := u.Query()
		q.Set("pg", strconv.Itoa(pg))
		u.RawQuery = q.Encode()

		reqCtx, cancel := f.requestContext(ctx)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
		if err != nil {
			cancel()
			if pg == 1 {
				return nil, err
			}
			break
		}
		req.Header.Set("User-Agent", f.userAgent())
		req.Header.Set("Accept", "application/json, */*")

		resp, err := f.client.Do(req)
		if err != nil {
			cancel()
			if pg == 1 {
				return nil, fmt.Errorf("请求分类接口失败: %w", err)
			}
			break // 后续页失败不致命：已合并的分类保留
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		code := resp.StatusCode
		resp.Body.Close()
		cancel()
		if code != http.StatusOK {
			if pg == 1 {
				return nil, fmt.Errorf("分类接口返回 HTTP %d", code)
			}
			break
		}
		if err != nil {
			if pg == 1 {
				return nil, fmt.Errorf("读取分类响应失败: %w", err)
			}
			break
		}
		r, err := ParseTypeList(body)
		if err != nil {
			if pg == 1 {
				return nil, fmt.Errorf("%w（响应开头: %.120s）", err, string(body))
			}
			break
		}
		// 合并去重：list[] 以 type_id 为主键，class[] 同理（class 优先于 list）。
		for _, it := range r.List {
			key := it.TypeID.String() + "\x1f" + it.TypePID.String()
			if seenList[key] {
				continue
			}
			seenList[key] = true
			merged.List = append(merged.List, it)
		}
		for _, it := range r.Class {
			key := it.TypeID.String() + "\x1f" + it.TypePID.String()
			if seenClass[key] {
				continue
			}
			seenClass[key] = true
			merged.Class = append(merged.Class, it)
		}
		pageCount := r.PageCount.Int()
		if pageCount <= 0 {
			pageCount = 1
		}
		// 终止条件：已到声明的末页；本页为空（站点分页异常）；或本页不满一页
		// （即便 pagecount 偏大也视为末页，避免按虚假总数空转）。三者任一即停。
		if pg >= pageCount || len(r.List) == 0 || len(r.List) < pageSize {
			break
		}
	}
	return merged, nil
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
