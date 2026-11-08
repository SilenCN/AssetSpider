package service

import (
	"net/url"
	"strings"

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
	// 规范化后入库：域名补全协议、去掉尾斜杠，播放时可直接拼接。
	if v, err := NormalizeParseAPI(in.ParseAPI); err == nil {
		dst.ParseAPI = v
	}
	if v, err := NormalizePlayDomain(in.PlayDomain); err == nil {
		dst.PlayDomain = v
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

// SourceView 在站点配置上附加该站已入库的影片数量。
type SourceView struct {
	model.Source
	VideoCount int64 `json:"videoCount"`
}

// SourceService 处理站点配置的校验、默认值与读写编排。
type SourceService struct {
	sources repository.SourceRepository
	videos  repository.VideoRepository
}

func NewSourceService(sources repository.SourceRepository, videos repository.VideoRepository) *SourceService {
	return &SourceService{sources: sources, videos: videos}
}

func (s *SourceService) List() ([]SourceView, error) {
	srcs, err := s.sources.ListSources()
	if err != nil {
		return nil, err
	}
	counts, err := s.videos.CountVideosBySource()
	if err != nil {
		return nil, err
	}
	out := make([]SourceView, 0, len(srcs))
	for _, src := range srcs {
		out = append(out, SourceView{Source: src, VideoCount: counts[src.ID]})
	}
	return out, nil
}

func (s *SourceService) Create(in SourceInput) (*model.Source, error) {
	if err := in.validate(); err != nil {
		return nil, err
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
	updated, err := s.sources.UpdateSource(id, in.apply(existing))
	if err != nil {
		return nil, wrapRepo(err)
	}
	return updated, nil
}

func (s *SourceService) Delete(id uint) error {
	return wrapRepo(s.sources.DeleteSource(id))
}

// Get 供 CrawlService 判断站点是否存在（仍在 service 层内调用，不跨层）。
func (s *SourceService) Get(id uint) (*model.Source, error) {
	src, err := s.sources.GetSource(id)
	if err != nil {
		return nil, wrapRepo(err)
	}
	return src, nil
}
