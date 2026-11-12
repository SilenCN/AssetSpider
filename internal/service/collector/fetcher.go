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

	// IsVideo 标记这条记录其实是影片而不是分类定义：有的站点不认 ac=typelist，
	// 仍返回影片列表（带 vod_id / vod_name），只是把字段塞进了 list[]。影片条目
	// 只能用来补分类名称，不能当分类定义——影片没有 type_pid，当成定义会把
	// 二级分类抬成一级。
	IsVideo bool `json:"-"`
}

// UnmarshalJSON 在解析分类定义的同时判断它是否是影片条目。
func (it *TypeItem) UnmarshalJSON(b []byte) error {
	type plain TypeItem // 新类型不继承方法，避免递归调用
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*it = TypeItem(p)
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(b, &keys); err == nil {
		_, vodID := keys["vod_id"]
		_, vodName := keys["vod_name"]
		it.IsVideo = vodID || vodName
	}
	return nil
}

// TypeListResponse 对应 api.php/provide/vod/?ac=typelist 的 JSON 响应。
// 部分站点不认 ac=typelist，仍返回影片列表，但会附带 class[] 分类定义；
// 因此两种载体都要收，class 优先。
//
// PageCount 是苹果CMS 的分类分页总页数：typelist 接口默认每页约 20 条、上限 100，
// 分类较多的站点 pagecount > 1，只拉第 1 页会丢掉后续页的分类（含落在后页的一级
// 分类）。FetchTypeList 按页翻取再合并，为避免异常 pagecount 空转设 3 页硬上限。
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

// FetchTypeList 返回本站的分类定义（type_id / type_name / type_pid），
// 供采集结束后补全一级分类名称、校正父子层级。全程协议级通用逻辑，不含任何站点特判。
//
// 分类定义有两个通用来源，按可靠性依次尝试：
//  1. 专用接口 ac=typelist（list[] / class[]），分页翻取，最多尝试 3 页；
//  2. 仅当该接口**不可用**（HTTP 非 200 / code != 1 / 网络或解析错误）时，降级探测
//     标准列表动作（见 fetchClassFallback），只取响应里的 class[] —— 苹果CMS 在
//     列表响应里普遍附带本站全部分类定义。
//
// 降级只在「接口层失败」时触发，不在「成功返回空列表」时触发：站点以 code=1 +
// 空列表明确作答，视为有效应答（它可能确实没有独立分类定义，交由采集循环的
// class[] 与 type_id 兜底），避免对正常站点多做一次无谓探测。
//
// 两类来源都拿不到时按「非致命」处理：返回错误或空结果，调用方据此退回
// videolist 自带分类 / type_id 兜底，采集不阻断。
func (f *Fetcher) FetchTypeList(ctx context.Context, src model.Source) (*TypeListResponse, error) {
	cfg := f.current()
	primary, perr := f.fetchTypeListPrimary(ctx, src, cfg)
	if perr == nil {
		return primary, nil // 接口正常应答（含空列表）：不降级
	}
	// 专用分类接口不可用：降级从标准列表响应的 class[] 取权威分类定义。
	if fb := f.fetchClassFallback(ctx, src); fb != nil && len(fb.Class) > 0 {
		return fb, nil
	}
	return nil, perr
}

// fetchTypeListPrimary 请求专用分类接口 ac=typelist 并翻页合并各页的 list[] / class[]。
// typelist 是分页接口；按站点 pagecount 翻取，但最多尝试 3 页，避免分类巨多或
// pagecount 异常时反复翻页拖慢采集（缺失部分由 fetchClassFallback 的 class[] 补齐）。
// 翻页以 type_id + type_pid 去重，避免站点分页异常（各页重复首项）时无限膨胀。
// 仅第 1 页失败视为「接口整体不可用」并报错，交由 FetchTypeList 决定是否降级。
func (f *Fetcher) fetchTypeListPrimary(ctx context.Context, src model.Source, cfg config.CollectorConfig) (*TypeListResponse, error) {
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
	const maxTypePages = 3 // 分类接口分页硬上限：最多尝试 3 页（含第 1 页）

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

// fetchClassFallback 是专用分类接口不可用时的通用降级来源。
//
// 苹果CMS V10 的列表接口（ac=list / ac=videolist / 站点自身 ac）在返回影片的同时，
// 普遍会在 class[] 里顺带给出「本站全部分类定义」（含 type_id / type_name /
// type_pid）。本函数按 typeProbeActions 给出的通用动作序列逐个探测，每个动作只取
// 第 1 页，遇到第一个返回了非空 class[] 的动作即返回。
//
// 正确性要点：列表动作的 list[] 是**影片**，绝不能当分类定义；因此这里用 ParseList
// 解析并只取 Class 字段，从源头上避免把影片 type_id 误建成一级分类。
//
// 全部逻辑基于协议通用约定，不含任何站点名称 / 域名 / ID 判断。拿不到时返回 nil，
// 由 FetchTypeList 按「非致命」语义处理。
func (f *Fetcher) fetchClassFallback(ctx context.Context, src model.Source) *TypeListResponse {
	for _, action := range typeProbeActions(src) {
		rawURL, err := buildTypeProbeURL(src, action)
		if err != nil {
			continue
		}
		reqCtx, cancel := f.requestContext(ctx)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
		if err != nil {
			cancel()
			continue
		}
		req.Header.Set("User-Agent", f.userAgent())
		req.Header.Set("Accept", "application/json, */*")
		resp, err := f.client.Do(req)
		if err != nil {
			cancel()
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		code := resp.StatusCode
		resp.Body.Close()
		cancel()
		if code != http.StatusOK || readErr != nil {
			continue
		}
		// 列表动作响应：只认 class[]，list[] 是影片，绝不参与分类定义。
		lr, err := ParseList(body)
		if err != nil || len(lr.Class) == 0 {
			continue
		}
		return &TypeListResponse{Code: 1, PageCount: 1, Class: lr.Class}
	}
	return nil
}

// typeProbeActions 给出降级探测用的 ac 动作序列，完全由站点自身配置与协议标准动作
// 推导，不含任何站点特判：
//  1. 站点在 APIBase 或 ExtraParams 里显式指定的 ac（尊重站点已配置的接口形态）；
//  2. 协议标准的 list、videolist 动作兜底。
//
// 去重并跳过 typelist（它已在主路径 fetchTypeListPrimary 里试过）。
func typeProbeActions(src model.Source) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(ac string) {
		ac = strings.TrimSpace(ac)
		if ac == "" || strings.EqualFold(ac, "typelist") || seen[ac] {
			return
		}
		seen[ac] = true
		out = append(out, ac)
	}
	if u, err := url.Parse(strings.TrimSpace(src.APIBase)); err == nil {
		add(u.Query().Get("ac"))
	}
	if extra := strings.TrimPrefix(strings.TrimSpace(src.ExtraParams), "?"); extra != "" {
		if q, err := url.ParseQuery(extra); err == nil {
			add(q.Get("ac"))
		}
	}
	add("list")
	add("videolist")
	return out
}

// buildTypeProbeURL 为分类降级探测构造请求地址：保留站点地址与附加参数（含可能的
// 认证参数），强制 ac=action，并移除与时间窗 / 分页相关的参数（h / pg / page /
// pagesize / limit）——分类定义与这些无关，且部分站点带增量时间窗 h 时不再附带
// class[]。只取第 1 页即可拿到全量 class[]，故不设置页码。
func buildTypeProbeURL(src model.Source, action string) (string, error) {
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
	for _, k := range []string{"h", "pg", "page", "pagesize", "limit"} {
		q.Del(k)
	}
	q.Set("ac", action)
	u.RawQuery = q.Encode()
	return u.String(), nil
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
