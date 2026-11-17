package service

import (
	"fmt"
	"strings"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

// TVBoxService 把站内资源包装成 TVBox / 影视仓可直接消费的接口：
//
//   - Config：TVBox 配置文件（sites 数组），每个可见站点一个 type=1 的苹果CMS 采集源；
//   - Vod：苹果CMS V10 provide/vod 协议（ac=list / videolist / detail），
//     TVBox 用 type=1 的站点按这套协议拉分类、列表、详情与播放地址。
//
// 鉴权由 controller 层用 URL 里的 api key 解析出 Principal 后传入，可见范围与
// 站内其他接口完全一致：普通用户只见被授权站点，管理员见全部。
type TVBoxService struct {
	sources    repository.SourceRepository
	categories repository.CategoryRepository
	videos     repository.VideoRepository
	users      repository.UserRepository
}

func NewTVBoxService(
	sources repository.SourceRepository,
	categories repository.CategoryRepository,
	videos repository.VideoRepository,
	users repository.UserRepository,
) *TVBoxService {
	return &TVBoxService{sources: sources, categories: categories, videos: videos, users: users}
}

// TVBoxSite 是 TVBox 配置文件里的一个点播站点。
// type=1 表示苹果CMS 风格 JSON 采集接口；key 用 sourceID 派生，稳定且能作为
// TVBox 的观看历史 / 收藏标识（key 一旦变化，用户的历史就对不上）。
type TVBoxSite struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Type        int    `json:"type"`
	API         string `json:"api"`
	Searchable  int    `json:"searchable"`
	QuickSearch int    `json:"quickSearch"`
	Filterable  int    `json:"filterable"`
}

// TVBoxConfig 是 TVBox / 影视仓的配置根对象。lives / parses 留空数组，
// 本项目只提供点播，不聚合直播，也不需要 spider 爬虫包。
type TVBoxConfig struct {
	Sites  []TVBoxSite   `json:"sites"`
	Lives  []interface{} `json:"lives"`
	Parses []interface{} `json:"parses"`
	Flags  []string      `json:"flags,omitempty"`
}

// Config 生成当前主体可见的 TVBox 配置。
// baseURL 是请求方此刻的根地址（含 scheme + host，不含尾斜杠），api key 由
// controller 传入并拼进每个站点的 api 路径里。
func (s *TVBoxService) Config(baseURL, apiKey string, p Principal) (TVBoxConfig, error) {
	srcs, err := s.sources.ListSources(p.Visible())
	if err != nil {
		return TVBoxConfig{}, err
	}
	// 用户可在自己的接口里进一步选择开放哪些站点（见 AuthService.TVBoxSources）。
	// 这是可见范围内的"软选择"：selected 模式下只列出勾选站点，且勾选已在
	// AuthService 里校验过不越权；all（含历史空值）保持全开，保证升级兼容。
	allowed, err := s.tvboxSelectedSources(p)
	if err != nil {
		return TVBoxConfig{}, err
	}
	sites := make([]TVBoxSite, 0, len(srcs))
	for _, src := range srcs {
		if !src.Enabled {
			continue
		}
		if allowed != nil && !allowed[src.ID] {
			continue
		}
		sites = append(sites, TVBoxSite{
			Key:         tvboxSiteKey(src.ID),
			Name:        src.Name,
			Type:        1, // 苹果CMS provide/vod
			API:         fmt.Sprintf("%s/tvbox/%s/vod/%d", strings.TrimRight(baseURL, "/"), apiKey, src.ID),
			Searchable:  1,
			QuickSearch: 1,
			Filterable:  1,
		})
	}
	return TVBoxConfig{Sites: sites, Lives: []interface{}{}, Parses: []interface{}{}}, nil
}

// tvboxSelectedSources 返回该用户勾选开放的站点集合；nil 表示"不做选择过滤"（全开）。
// selected 模式下返回结果再与可见范围求交，即便勾选表里残留了已撤销授权的站点也不会放行。
func (s *TVBoxService) tvboxSelectedSources(p Principal) (map[uint]bool, error) {
	u, err := s.users.GetUser(p.UserID)
	if err != nil {
		return nil, err
	}
	if normalizeTVBoxMode(u.TVBoxSourcesMode) != TVBoxSourcesSelected {
		return nil, nil
	}
	ids, err := s.users.ListTVBoxSourceIDsByUser(p.UserID)
	if err != nil {
		return nil, err
	}
	allowed := make(map[uint]bool, len(ids))
	for _, id := range ids {
		if p.CanSee(id) {
			allowed[id] = true
		}
	}
	return allowed, nil
}

// tvboxSiteKey 生成稳定的站点标识：tvbox_<sourceID>。
// 不用站点名是因为改名后 key 必须保持不变（TVBox 用它关联观看历史）。
func tvboxSiteKey(sourceID uint) string {
	return fmt.Sprintf("assetspider_%d", sourceID)
}

// VodRequest 是苹果CMS provide/vod 的查询入参（已由 controller 从 query 解析）。
type VodRequest struct {
	SourceID uint
	Action   string // list / videolist / detail；空按 videolist
	TypeID   string // t：苹果CMS type_id
	Keyword  string // wd：搜索关键词
	IDs      string // ids：vod_id 列表（逗号分隔）
	Page     int
	Size     int
	// 二次筛选参数：TVBoxOS 把筛选面板选中项的 key/value 原样作为 query 参数回传。
	By    string // by：排序标识（id_desc/time_desc/score_desc/hits_desc…）
	Year  string // year：年份
	Area  string // area：地区（模糊）
	Class string // class：类型（模糊，对应 vod_class）
	State string // state：完结/连载
}

// vodMsgOK 是苹果CMS V10 provide/vod 成功响应里 msg 字段的标准值。
// 不能写成 "ok" 或留空：以 q215613905/TVBoxOS 为代表的老版 TVBox 客户端在
// DetailActivity 中对详情响应用如下判定——msg 非空且不等于 "数据列表" 就视为
// 错误提示，直接 showEmpty() 并 return，导致影片详情的播放组 / 播放列表全部
// 无法显示（表现为"没找到数据"，且所有站点都失败，与影片内容无关）。
// 回归苹果CMS 标准值 "数据列表" 即可让这类客户端正常进入详情解析。
const vodMsgOK = "数据列表"

// VodResponse 是苹果CMS provide/vod 的标准响应。
// Code=1 成功；list 为空时仍返回 code=1（苹果CMS 语义），只在错误时 code=0。
// Msg 固定为苹果CMS 标准值 "数据列表"，兼容老版 TVBox 的详情判定（见 vodMsgOK）。
type VodResponse struct {
	Code      int        `json:"code"`
	Msg       string     `json:"msg"`
	Page      int        `json:"page,omitempty"`
	PageCount int        `json:"pagecount,omitempty"`
	Limit     int        `json:"limit,omitempty"`
	Total     int64      `json:"total,omitempty"`
	Class     []VodClass `json:"class,omitempty"`
	// Filters 是 TVBoxOS 等老版客户端的二次筛选面板定义：按分类 type_id 索引，
	// 每个分类下有一组筛选项（key + name + 候选值 n/v）。客户端选中后把 key=n→v
	// 作为 query 参数回传，服务端据此过滤。nil 表示不提供筛选面板。
	Filters map[string][]VodFilter `json:"filters,omitempty"`
	List    []VodItem              `json:"list"`
}

// VodClass 是 ac=list / 首页响应里的分类项，对应客户端 AbsSortJson.classes。
type VodClass struct {
	TypeID   string `json:"type_id"`
	TypePID  string `json:"type_pid,omitempty"`
	Name     string `json:"type_name"`
	TypeFlag string `json:"type_flag,omitempty"`
}

// VodFilter 是一个筛选项（如"年份"），对应客户端 getSortFilter 的结构：
// key 会作为 query 参数名回传，value 里每个 n/v 是一个可选项（n=显示名，v=参数值）。
type VodFilter struct {
	Key   string            `json:"key"`
	Name  string            `json:"name"`
	Value []VodFilterOption `json:"value"`
}

// VodFilterOption 是筛选项的一个候选值。
type VodFilterOption struct {
	N string `json:"n"` // 显示名
	V string `json:"v"` // 提交值
}

// 筛选面板的 key（与 VodRequest 字段对应，也是客户端回传的 query 参数名）。
const (
	filterBy    = "by"
	filterYear  = "year"
	filterArea  = "area"
	filterClass = "class"
	filterState = "state"
)

// Vod 处理一条 provide/vod 请求，并校验目标站点对当前主体可见。
func (s *TVBoxService) Vod(req VodRequest, p Principal) (VodResponse, error) {
	if !p.CanSee(req.SourceID) {
		return VodResponse{}, fmt.Errorf("%w: 无权访问该站点", ErrForbidden)
	}
	// 与 config 一致：用户在自己接口里屏蔽的站点，也不能通过 vod 接口绕过访问。
	allowed, err := s.tvboxSelectedSources(p)
	if err != nil {
		return VodResponse{}, err
	}
	if allowed != nil && !allowed[req.SourceID] {
		return VodResponse{}, fmt.Errorf("%w: 该站点已在 TVBox 接口中屏蔽", ErrForbidden)
	}
	if _, err := s.sources.GetSource(req.SourceID, p.Visible()); err != nil {
		return VodResponse{}, wrapRepo(err)
	}

	// ids 在苹果CMS 语义里是「按影片 ID 精确取回」，与其搭配的 ac 并不固定：
	// 官方 detail 用 ac=detail&ids=，但 TVBox / 影视仓等客户端点开影片时，
	// 常见实现还有 ac=videolist&ids=、仅 ids=、甚至 ac=list&ids=。
	// 只要带了 ids 就优先按详情处理，否则客户端拼的 ac 稍有差异就会退化成
	// 整页列表，导致拿不到目标影片的播放组 / 播放列表。
	if len(splitIDs(req.IDs, 200)) > 0 || strings.TrimSpace(req.Keyword) != "" {
		return s.vodDetail(req)
	}
	switch req.Action {
	case "list":
		return s.vodListClasses(req)
	case "detail":
		return s.vodDetail(req)
	case "videolist", "":
		return s.vodVideoList(req)
	default:
		// 苹果CMS 未知 ac 也按 videolist 处理，兼容性更好。
		return s.vodVideoList(req)
	}
}

// vodListClasses 返回该站点的分类列表（ac=list）。
// 同时带上 filters：TVBoxOS 的首页分类请求用同一份响应画分类 tab + 筛选面板，
// 缺少 filters 时筛选面板是空的。
func (s *TVBoxService) vodListClasses(req VodRequest) (VodResponse, error) {
	classes := s.classListOf(req.SourceID)
	filters := s.sortFiltersOf(req.SourceID, classes)
	return VodResponse{
		Code: 1, Msg: vodMsgOK,
		Class: classes, Filters: filters, List: []VodItem{},
	}, nil
}

// classListOf 把本站分类映射成苹果CMS class 数组。
// 只列有 type_id 的分类：客户端把 type_id 当 t 参数回传，无 id 的分类点不动。
// 二级分类一并列出（苹果CMS 本就支持扁平分类列表），父级信息放 type_pid。
func (s *TVBoxService) classListOf(sourceID uint) []VodClass {
	cats, err := s.categories.List(sourceID)
	if err != nil {
		return []VodClass{}
	}
	out := make([]VodClass, 0, len(cats))
	for _, c := range cats {
		if strings.TrimSpace(c.TypeID) == "" {
			continue
		}
		out = append(out, VodClass{
			TypeID: c.TypeID, TypePID: c.TypePID,
			Name: c.Name, TypeFlag: c.TypeID,
		})
	}
	return out
}

// sortFiltersOf 从本站已入库影片聚合出筛选面板。
// 每个分类挂同一套筛选（排序 + 年份 + 地区 + 类型 + 状态）；候选值来自库内真实数据，
// 避免列出选了没结果的选项。聚合失败时降级为只有排序档位，不影响分类展示。
func (s *TVBoxService) sortFiltersOf(sourceID uint, classes []VodClass) map[string][]VodFilter {
	// 排序档位对所有分类通用，客户端选中后以 by=<sort> 回传。
	base := []VodFilter{{
		Key: filterBy, Name: "排序",
		Value: []VodFilterOption{
			{N: "最新", V: "id_desc"},
			{N: "最近更新", V: "time_desc"},
			{N: "评分", V: "score_desc"},
			{N: "热度", V: "hits_desc"},
		},
	}}
	dynamic := s.dynamicFiltersOf(sourceID)
	all := append(base, dynamic...)
	// 客户端按分类 type_id 索引 filters；另外给一个 "*" 兜底（部分客户端首页
	// 用固定 key 取筛选）。无分类时仍能显示一套通用筛选。
	filters := map[string][]VodFilter{"*": all}
	for _, c := range classes {
		filters[c.TypeID] = all
	}
	return filters
}

// dynamicFiltersOf 聚合年份 / 地区 / 类型 / 状态候选值。
// vod_class 是多值串（逗号分隔），这里拆开后去重，保证筛选项与单值字段一致。
func (s *TVBoxService) dynamicFiltersOf(sourceID uint) []VodFilter {
	out := make([]VodFilter, 0, 4)
	defs := []struct {
		column string
		key    string
		name   string
		split  bool
	}{
		{"vod_year", filterYear, "年份", false},
		{"vod_area", filterArea, "地区", false},
		{"vod_class", filterClass, "类型", true},
		{"vod_state", filterState, "状态", false},
	}
	for _, d := range defs {
		values, err := s.videos.DistinctFieldValues(sourceID, d.column, 200)
		if err != nil || len(values) == 0 {
			continue
		}
		var opts []VodFilterOption
		seen := map[string]bool{}
		for _, raw := range values {
			parts := []string{raw}
			if d.split {
				parts = strings.Split(raw, ",")
			}
			for _, p := range parts {
				v := strings.TrimSpace(p)
				if v == "" || seen[v] {
					continue
				}
				seen[v] = true
				opts = append(opts, VodFilterOption{N: v, V: v})
			}
		}
		if len(opts) == 0 {
			continue
		}
		out = append(out, VodFilter{Key: d.key, Name: d.name, Value: opts})
	}
	return out
}

// vodVideoList 按分类 / 关键词分页返回影片（ac=videolist）。
func (s *TVBoxService) vodVideoList(req VodRequest) (VodResponse, error) {
	query := repository.VideoQuery{
		SourceID: req.SourceID,
		Keyword:  strings.TrimSpace(req.Keyword),
		Page:     req.Page,
		Size:     req.Size,
		Sort:     "id_desc",
		Year:     strings.TrimSpace(req.Year),
		Area:     strings.TrimSpace(req.Area),
		Class:    strings.TrimSpace(req.Class),
		State:    strings.TrimSpace(req.State),
	}
	// by=<排序标识>：仅接受白名单内的值，未知值回退默认 id_desc。
	if by := strings.TrimSpace(req.By); by != "" {
		switch by {
		case "id_desc", "time_desc", "time_asc", "score_desc", "hits_desc":
			query.Sort = by
		}
	}
	// t=type_id → 本地 category_id（按苹果CMS type_id 精确匹配本站分类）。
	if tid := strings.TrimSpace(req.TypeID); tid != "" && tid != "0" {
		cat, err := s.categoryByTypeID(req.SourceID, tid)
		if err != nil {
			return VodResponse{}, err
		}
		if cat != nil {
			query.CategoryID = cat.ID
		}
	}
	videos, total, err := s.videos.ListVideosForFeed(query)
	if err != nil {
		return VodResponse{}, err
	}
	names := s.typeNamesOf(req.SourceID)
	items := make([]VodItem, 0, len(videos))
	for i := range videos {
		items = append(items, vodItemOf(videos[i], names[videos[i].CategoryID]))
	}
	pageCount := 0
	if query.Size > 0 {
		pageCount = int((total + int64(query.Size) - 1) / int64(query.Size))
	}
	resp := VodResponse{
		Code: 1, Msg: vodMsgOK, Page: query.Page, PageCount: pageCount,
		Limit: query.Size, Total: total, List: items,
	}
	// TVBoxOS 对 type=1 站点用「裸 GET api」一次性取分类 tab + 筛选面板 + 首页
	// 推荐，缺 class/filters 首页就没有分类和筛选。只在第 1 页附带：翻页不需要
	// 面板，也避开每页重复做 DISTINCT 聚合的开销。
	if req.Page <= 1 {
		resp.Class = s.classListOf(req.SourceID)
		resp.Filters = s.sortFiltersOf(req.SourceID, resp.Class)
	}
	return resp, nil
}

// vodDetail 按 ids（苹果CMS vod_id，逗号分隔）返回完整影片（ac=detail）。
// 也兼容 wd 搜索：有 ids 优先 ids，否则退化为关键词检索的首页结果。
// 既无 ids 也无关键词时按苹果CMS 语义返回含播放字段的首页详情列表。
func (s *TVBoxService) vodDetail(req VodRequest) (VodResponse, error) {
	ids := splitIDs(req.IDs, 200)
	var videos []model.Video
	var err error
	switch {
	case len(ids) > 0:
		videos, err = s.videos.GetVideosByVodIDs(req.SourceID, ids)
		if err != nil {
			return VodResponse{}, err
		}
		// GetVideosByVodIDs 用 IN 查询，返回顺序与数据库行序一致、与 ids 入参
		// 顺序无关；这里按 ids 顺序重排，保证客户端点哪部就能先拿到哪部。
		videos = orderVideosByIDs(videos, ids)
	case strings.TrimSpace(req.Keyword) != "":
		videos, _, err = s.videos.ListVideosForFeed(repository.VideoQuery{
			SourceID: req.SourceID, Keyword: strings.TrimSpace(req.Keyword), Page: 1, Size: 50, Sort: "id_desc",
		})
		if err != nil {
			return VodResponse{}, err
		}
	default:
		// ac=detail 不带 ids：苹果CMS 返回全字段详情列表，供客户端确认播放源。
		return s.vodVideoList(req)
	}
	names := s.typeNamesOf(req.SourceID)
	items := make([]VodItem, 0, len(videos))
	for i := range videos {
		items = append(items, vodItemOf(videos[i], names[videos[i].CategoryID]))
	}
	// 苹果CMS 的 detail 响应与列表共用同一套解析：部分 TVBox / 影视仓修改版
	// 会用统一的分页解析器读取 page / pagecount / limit / total，缺失时把响应
	// 判为无效并显示「没找到数据」。这里补齐这些字段，使 detail 与 videolist
	// 顶层结构完全一致（detail 只有一页、条数即命中数）。
	size := req.Size
	if size < 1 {
		size = 20
	}
	return VodResponse{
		Code: 1, Msg: vodMsgOK,
		Page: 1, PageCount: 1, Limit: size, Total: int64(len(items)),
		List: items,
	}, nil
}

// orderVideosByIDs 把查回的影片按 ids 给定顺序重排；ids 里查不到的直接忽略。
func orderVideosByIDs(videos []model.Video, ids []string) []model.Video {
	byID := make(map[string]model.Video, len(videos))
	for _, v := range videos {
		byID[v.VodID] = v
	}
	out := make([]model.Video, 0, len(ids))
	for _, id := range ids {
		if v, ok := byID[id]; ok {
			out = append(out, v)
		}
	}
	return out
}

// categoryByTypeID 按本站苹果CMS type_id 找分类。
// 站点未返回 type_id 时分类可能以名字做键，此时 type_id 为空，匹配不到即不筛选。
func (s *TVBoxService) categoryByTypeID(sourceID uint, typeID string) (*model.Category, error) {
	cats, err := s.categories.List(sourceID)
	if err != nil {
		return nil, err
	}
	for i := range cats {
		if cats[i].TypeID == typeID {
			return &cats[i], nil
		}
	}
	return nil, nil
}

// typeNamesOf 预读本站 category_id → 分类名，避免逐条影片查库。
func (s *TVBoxService) typeNamesOf(sourceID uint) map[uint]string {
	out := map[uint]string{}
	cats, err := s.categories.List(sourceID)
	if err != nil {
		return out
	}
	for _, c := range cats {
		out[c.ID] = c.Name
	}
	return out
}
