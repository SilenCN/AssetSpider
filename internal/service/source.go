package service

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

// SourceInput 是站点配置中允许前端提交的字段，也是 controller → service 的 DTO。
type SourceInput struct {
	Name             string `json:"name"`
	APIBase          string `json:"apiBase"`
	Format           string `json:"format"`
	ExtraParams      string `json:"extraParams"`
	Enabled          *bool  `json:"enabled"`
	IncrementalHours int    `json:"incrementalHours"`
	MaxPages         int    `json:"maxPages"`
	ParseAPI         string `json:"parseApi"`   // 视频解析地址，播放时拼在播放地址前
	PlayDomain       string `json:"playDomain"` // 视频播放域名，播放时替换原地址域名
	PicAPI           string `json:"picApi"`     // 图片解析/代理地址，展示时拼在图片地址前
	PicDomain        string `json:"picDomain"`  // 图片域名，展示时替换原图片地址域名
}

func (in SourceInput) validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return invalid("站点名称不能为空")
	}
	raw := strings.TrimSpace(in.APIBase)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return invalid("接口地址需为完整的 http(s) URL，例如 https://site.com/api.php/provide/vod/")
	}
	if f := strings.TrimSpace(in.Format); f != "" && f != model.FormatSeaCMSJSON && f != model.FormatMacCMSJSON {
		return invalid("暂不支持的采集格式：%s（可选 %s / %s）", f, model.FormatSeaCMSJSON, model.FormatMacCMSJSON)
	}
	if in.IncrementalHours < 0 || in.MaxPages < 0 {
		return invalid("增量小时数与最大页数不能为负")
	}
	if _, err := NormalizeParseAPI(in.ParseAPI); err != nil {
		return invalid("%s", err.Error())
	}
	if _, err := NormalizePlayDomain(in.PlayDomain); err != nil {
		return invalid("%s", err.Error())
	}
	if _, err := NormalizePicAPI(in.PicAPI); err != nil {
		return invalid("%s", err.Error())
	}
	if _, err := NormalizePicDomain(in.PicDomain); err != nil {
		return invalid("%s", err.Error())
	}
	return nil
}

func (in SourceInput) apply(dst *model.Source) *model.Source {
	dst.Name = strings.TrimSpace(in.Name)
	dst.APIBase = strings.TrimSpace(in.APIBase)
	dst.Format = strings.TrimSpace(in.Format)
	if dst.Format == "" {
		dst.Format = model.FormatMacCMSJSON
	}
	dst.ExtraParams = strings.TrimSpace(in.ExtraParams)
	// 规范化后入库：域名补全协议、去掉尾斜杠，展示/播放时可直接拼接。
	for _, f := range []struct {
		raw  string
		norm func(string) (string, error)
		dst  *string
	}{
		{in.ParseAPI, NormalizeParseAPI, &dst.ParseAPI},
		{in.PlayDomain, NormalizePlayDomain, &dst.PlayDomain},
		{in.PicAPI, NormalizePicAPI, &dst.PicAPI},
		{in.PicDomain, NormalizePicDomain, &dst.PicDomain},
	} {
		if v, err := f.norm(f.raw); err == nil {
			*f.dst = v
		}
	}
	if in.Enabled != nil {
		dst.Enabled = *in.Enabled
	} else if dst.ID == 0 {
		dst.Enabled = true // 新建时默认启用
	}
	dst.IncrementalHours = in.IncrementalHours
	dst.MaxPages = in.MaxPages
	return dst
}

// SourceView 是站点配置的对外视图。
//
// 影片总数 videoCount 随 model.Source 一起返回：它是采集任务维护的快照列
// （运行中每分钟刷新、结束时刷新），因此列表查询不再对 videos 表做 COUNT(*)。
type SourceView struct {
	model.Source
}

// ClearResult 是一次「清空站点数据」实际删除的各表条数。
type ClearResult struct {
	Videos     int64 `json:"videos"`
	Categories int64 `json:"categories"`
	Tags       int64 `json:"tags"`
	Jobs       int64 `json:"jobs"`
}

// ActiveTaskChecker 让站点服务在清空数据前询问调度层：该站点是否还有
// 排队中 / 运行中的采集任务。
//
// 清空与采集并发执行会互相破坏数据（采集刚把影片与标签写回来，清空又按快照
// 删掉分类），因此有任务在跑时必须拒绝，由用户先暂停或等待结束。
type ActiveTaskChecker interface {
	HasActiveTask(sourceID uint) bool
}

// SourceService 处理站点配置的校验、默认值与读写编排。
type SourceService struct {
	sources repository.SourceRepository
	active  ActiveTaskChecker // 可选：未注入时不做采集冲突检查
}

// NewSourceService 构造站点服务；active 是可选依赖（采集调度层），
// 只有需要「清空数据前检查在跑任务」时才有意义。
func NewSourceService(sources repository.SourceRepository, active ...ActiveTaskChecker) *SourceService {
	svc := &SourceService{sources: sources}
	if len(active) > 0 {
		svc.active = active[0]
	}
	return svc
}

// List 返回当前登录主体可见的站点配置；管理员不传主体时可见全部。
func (s *SourceService) List(p ...Principal) ([]SourceView, error) {
	srcs, err := s.sources.ListSources(principalOf(p...).Visible())
	if err != nil {
		return nil, err
	}
	out := make([]SourceView, 0, len(srcs))
	for _, src := range srcs {
		out = append(out, SourceView{Source: src})
	}
	return out, nil
}

func (s *SourceService) Create(in SourceInput) (*model.Source, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	if taken, err := s.nameTakenByOther(in.Name, 0); err != nil {
		return nil, err
	} else if taken {
		return nil, conflict("站点名称已存在：%s", strings.TrimSpace(in.Name))
	}
	src := in.apply(&model.Source{})
	if err := s.sources.CreateSource(src); err != nil {
		return nil, wrapRepo(err)
	}
	return src, nil
}

func (s *SourceService) Update(id uint, in SourceInput) (*model.Source, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	existing, err := s.sources.GetSource(id)
	if err != nil {
		return nil, wrapRepo(err)
	}
	if taken, err := s.nameTakenByOther(in.Name, id); err != nil {
		return nil, err
	} else if taken {
		return nil, conflict("站点名称已存在：%s", strings.TrimSpace(in.Name))
	}
	updated, err := s.sources.UpdateSource(id, in.apply(existing))
	if err != nil {
		return nil, wrapRepo(err)
	}
	return updated, nil
}

// nameTakenByOther 判断归一化后的名称是否已被「另一条」站点占用。
// exceptID 表示排除自身（更新场景），0 表示不排除（新建场景）。
func (s *SourceService) nameTakenByOther(name string, exceptID uint) (bool, error) {
	other, err := s.sources.GetSourceByName(name)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return false, nil
		}
		return false, wrapRepo(err)
	}
	return other.ID != exceptID, nil
}

func (s *SourceService) Delete(id uint) error {
	return wrapRepo(s.sources.DeleteSource(id))
}

// Clear 清空站点名下的数据（影片、分类、标签、采集记录），站点配置保持不变。
//
// 该站点还有排队中 / 运行中的采集任务时拒绝执行：二者并发会互相覆盖。
func (s *SourceService) Clear(id uint) (ClearResult, error) {
	if s.active != nil && s.active.HasActiveTask(id) {
		return ClearResult{}, conflict("站点正在采集中，请先暂停采集或等待其结束再清空数据")
	}
	stats, err := s.sources.ClearSourceData(id)
	if err != nil {
		return ClearResult{}, wrapRepo(err)
	}
	return ClearResult{
		Videos:     stats.Videos,
		Categories: stats.Categories,
		Tags:       stats.Tags,
		Jobs:       stats.Jobs,
	}, nil
}

// Get 供 CrawlService 判断站点是否存在（仍在 service 层内调用，不跨层）。
func (s *SourceService) Get(id uint) (*model.Source, error) {
	src, err := s.sources.GetSource(id)
	if err != nil {
		return nil, wrapRepo(err)
	}
	return src, nil
}

// ---------- JSON 导入 / 导出 ----------

const SourceBundleVersion = 1

// SourceBundle 是站点配置的 JSON 交换格式，只含「可编辑字段」。
// ID、影片数、上次采集状态、增量游标、时间戳等运行时数据都不导出：
// 文件因此可以直接导入到另一个实例，也不会把旧实例的状态带过去。
type SourceBundle struct {
	Version    int           `json:"version"`
	ExportedAt string        `json:"exportedAt,omitempty"`
	Sources    []SourceInput `json:"sources"`
}

// ImportIssue 记录某一条配置导入失败的原因，不影响其它条目继续导入。
type ImportIssue struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
	Error string `json:"error"`
}

type ImportResult struct {
	Total   int           `json:"total"`
	Created int           `json:"created"`
	Updated int           `json:"updated"`
	Failed  int           `json:"failed"`
	Issues  []ImportIssue `json:"issues,omitempty"`
}

func (r *ImportResult) fail(index int, name string, err error) {
	r.Failed++
	r.Issues = append(r.Issues, ImportIssue{
		Index: index, Name: strings.TrimSpace(name), Error: err.Error(),
	})
}

func sourceInputOf(src model.Source) SourceInput {
	enabled := src.Enabled
	return SourceInput{
		Name:             src.Name,
		APIBase:          src.APIBase,
		Format:           src.Format,
		ExtraParams:      src.ExtraParams,
		Enabled:          &enabled,
		IncrementalHours: src.IncrementalHours,
		MaxPages:         src.MaxPages,
		ParseAPI:         src.ParseAPI,
		PlayDomain:       src.PlayDomain,
		PicAPI:           src.PicAPI,
		PicDomain:        src.PicDomain,
	}
}

// Export 导出当前登录主体可见的站点配置（管理员为全部）。
func (s *SourceService) Export(p ...Principal) (SourceBundle, error) {
	srcs, err := s.sources.ListSources(principalOf(p...).Visible())
	if err != nil {
		return SourceBundle{}, wrapRepo(err)
	}
	bundle := SourceBundle{
		Version:    SourceBundleVersion,
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Sources:    make([]SourceInput, 0, len(srcs)),
	}
	for _, src := range srcs {
		bundle.Sources = append(bundle.Sources, sourceInputOf(src))
	}
	return bundle, nil
}

// Import 批量导入站点配置：按站点名称（忽略大小写与首尾空白）匹配已有配置，
// 命中则更新（保留 ID、采集状态与游标），未命中则新建，因此重复导入不会堆叠重复站点。
// 单条校验或写库失败只记入 Issues，其余条目照常处理。
func (s *SourceService) Import(items []SourceInput, p ...Principal) (ImportResult, error) {
	if !principalOf(p...).IsAdmin {
		return ImportResult{}, fmt.Errorf("%w: 只有管理员可以导入站点配置", ErrForbidden)
	}
	if len(items) == 0 {
		return ImportResult{}, invalid("导入内容里没有站点配置")
	}
	existing, err := s.sources.ListSources()
	if err != nil {
		return ImportResult{}, wrapRepo(err)
	}
	byName := make(map[string]uint, len(existing))
	for _, src := range existing {
		byName[strings.ToLower(strings.TrimSpace(src.Name))] = src.ID
	}
	result := ImportResult{Total: len(items)}
	for i, in := range items {
		if err := in.validate(); err != nil {
			result.fail(i, in.Name, err)
			continue
		}
		key := strings.ToLower(strings.TrimSpace(in.Name))
		if id, ok := byName[key]; ok {
			if _, err := s.Update(id, in); err != nil {
				result.fail(i, in.Name, err)
			} else {
				result.Updated++
			}
			continue
		}
		created, err := s.Create(in)
		if err != nil {
			result.fail(i, in.Name, err)
			continue
		}
		result.Created++
		// 同一个文件里出现同名配置时，后续条目更新刚创建的那条，而不是再建一条。
		byName[key] = created.ID
	}
	return result, nil
}
