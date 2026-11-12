package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

// VideoList 是影片分页查询的结果 DTO。
// 只返回本页卡片与分页参数：不返回 total（要全量 COUNT），也不返回 hasMore，
// 前端按「本页条数 >= size」判断是否还有下一页。
//
// Items 是轻量卡片投影而非完整 Video：列表不携带播放组、简介、扩展字段等大文本，
// 用户点开影片时前端按 id 调详情接口取完整记录。
type VideoList struct {
	Items []model.VideoCard `json:"items"`
	Page  int               `json:"page"`
	Size  int               `json:"size"`
}

// VideoListQuery 是影片列表的查询入参（关键词 + 站点 + 分类 + 排序 + 分页）。
// 标签 / 地域 / 年份筛选与评分排序已移除，但影片上的这些原始字段仍保留。
type VideoListQuery struct {
	SourceID   uint
	CategoryID uint
	Keyword    string
	Sort       string
	Page       int
	Size       int
}

// VideoService 处理入库影片的查询与分页规则。
type VideoService struct {
	videos  repository.VideoRepository
	sources repository.SourceRepository
}

func NewVideoService(videos repository.VideoRepository, sources repository.SourceRepository) *VideoService {
	return &VideoService{videos: videos, sources: sources}
}

// sourceRewriter 汇总一个站点的两类改写器：播放地址与封面图片。
// 两者来自同一份站点配置，互不影响（图片域名通常和播放域名不是同一个）。
type sourceRewriter struct {
	play URLRewriter
	pic  URLRewriter
}

func (r sourceRewriter) enabled() bool { return r.play.Enabled() || r.pic.Enabled() }

// rewriters 读取站点配置，返回 sourceID → 改写器。
// 只有配置了「解析地址 / 域名」的站点才会出现在结果里，
// 未配置时整批查询连 JSON 都不解析，不影响性能。
func (s *VideoService) rewriters() (map[uint]sourceRewriter, error) {
	if s.sources == nil {
		return nil, nil
	}
	srcs, err := s.sources.ListSources()
	if err != nil {
		return nil, err
	}
	out := make(map[uint]sourceRewriter, len(srcs))
	for _, src := range srcs {
		r := sourceRewriter{play: NewPlayURLRewriterFrom(src), pic: NewPicURLRewriterFrom(src)}
		if r.enabled() {
			out[src.ID] = r
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// applyPlayRewrite 就地改写影片播放组里的地址（只改内存副本，不回写数据库）。
func applyPlayRewrite(v *model.Video, r URLRewriter) {
	if v.VodPlayList == "" || !r.Enabled() {
		return
	}
	var groups []model.PlayGroup
	if err := json.Unmarshal([]byte(v.VodPlayList), &groups); err != nil {
		return // 播放组数据损坏时保持原样，交由详情页提示
	}
	changed := false
	frame := r.UsesFrame()
	for i := range groups {
		for j := range groups[i].Episodes {
			ep := &groups[i].Episodes[j]
			if next := r.Rewrite(ep.Url); next != ep.Url {
				ep.Url = next
				changed = true
			}
			// 拼了解析地址后得到的是解析页面 URL，前端必须走 iframe，
			// 不能按 .m3u8 / .mp4 后缀交给 <video> 播放。
			if frame && ep.Url != "" && !ep.Frame {
				ep.Frame = true
				changed = true
			}
		}
	}
	if !changed {
		return
	}
	if b, err := json.Marshal(groups); err == nil {
		v.VodPlayList = string(b)
	}
}

// applyCardPicRewrite 就地改写列表卡片的封面地址。
// 卡片只携带三张卡片用得到的图，详情里的原图 / 截图集由 applyPicRewrite 处理。
func applyCardPicRewrite(c *model.VideoCard, r URLRewriter) {
	if !r.Enabled() {
		return
	}
	c.VodPic = r.Rewrite(c.VodPic)
	c.VodPicThumb = r.Rewrite(c.VodPicThumb)
	c.VodPicSlide = r.Rewrite(c.VodPicSlide)
}

// applyPicRewrite 就地改写影片的封面类图片地址。
// 与播放地址一样只改内存副本；vod_pic_screenshot 是 # 连接的截图集合，
// 逐段改写后按原分隔符拼回。
func applyPicRewrite(v *model.Video, r URLRewriter) {
	if !r.Enabled() {
		return
	}
	v.VodPic = r.Rewrite(v.VodPic)
	v.VodPicThumb = r.Rewrite(v.VodPicThumb)
	v.VodPicSlide = r.Rewrite(v.VodPicSlide)
	v.VodPicOriginal = r.Rewrite(v.VodPicOriginal)
	if v.VodPicScreenshot != "" {
		parts := strings.Split(v.VodPicScreenshot, "#")
		for i, p := range parts {
			parts[i] = r.Rewrite(p)
		}
		v.VodPicScreenshot = strings.Join(parts, "#")
	}
}

// applyRewrite 按站点配置改写一条影片的播放地址与图片地址。
func applyRewrite(v *model.Video, r sourceRewriter) {
	applyPlayRewrite(v, r.play)
	applyPicRewrite(v, r.pic)
}

// rewriteVideoCards 按站点配置批量改写列表卡片的封面地址。
// 列表不再携带播放组，因此只做图片改写；播放地址改写留给详情接口。
func (s *VideoService) rewriteVideoCards(items []model.VideoCard) error {
	rewriters, err := s.rewriters()
	if err != nil {
		return err
	}
	for i := range items {
		if r, ok := rewriters[items[i].SourceID]; ok {
			applyCardPicRewrite(&items[i], r.pic)
		}
	}
	return nil
}

func (s *VideoService) List(query VideoListQuery, p ...Principal) (VideoList, error) {
	page, size := query.Page, query.Size
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 20
	}
	items, err := s.videos.ListVideos(repository.VideoQuery{
		SourceID:         query.SourceID,
		CategoryID:       query.CategoryID,
		Keyword:          strings.TrimSpace(query.Keyword),
		Sort:             strings.TrimSpace(query.Sort),
		Page:             page,
		Size:             size,
		VisibleSourceIDs: principalOf(p...).Visible(),
	})
	if err != nil {
		return VideoList{}, err
	}
	if err := s.rewriteVideoCards(items); err != nil {
		return VideoList{}, err
	}
	return VideoList{Items: items, Page: page, Size: size}, nil
}

// Get 返回单条影片完整记录（含原始 JSON 与扩展字段）；不可见站点按不存在处理。
func (s *VideoService) Get(id uint, p ...Principal) (*model.Video, error) {
	v, err := s.videos.GetVideo(id, principalOf(p...).Visible())
	if err != nil {
		return nil, wrapRepo(err)
	}
	rewriters, err := s.rewriters()
	if err != nil {
		return nil, err
	}
	if r, ok := rewriters[v.SourceID]; ok {
		applyRewrite(v, r)
	}
	return v, nil
}

// GetPlayGroups 返回单条影片按苹果CMS 字段解析出的结构化播放组。
// 若影片无播放地址（vod_play_list 为空），返回空切片而非错误。
// 播放地址会按所属站点配置替换域名 / 拼接解析地址。
func (s *VideoService) GetPlayGroups(id uint, p ...Principal) ([]model.PlayGroup, error) {
	v, err := s.videos.GetVideo(id, principalOf(p...).Visible())
	if err != nil {
		return nil, wrapRepo(err)
	}
	if v.VodPlayList == "" {
		return []model.PlayGroup{}, nil
	}
	rewriters, err := s.rewriters()
	if err != nil {
		return nil, err
	}
	if r, ok := rewriters[v.SourceID]; ok {
		applyPlayRewrite(v, r.play)
	}
	var groups []model.PlayGroup
	if err := json.Unmarshal([]byte(v.VodPlayList), &groups); err != nil {
		return nil, fmt.Errorf("播放组数据损坏: %w", err)
	}
	return groups, nil
}
