package service

import (
	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

// TagView 在标签上附加该标签下的影片数量。
type TagView struct {
	model.Tag
	VideoCount int64 `json:"videoCount"`
}

// TagService 提供标签查询；标签本身由采集过程自动维护，不提供手工增删改。
type TagService struct {
	tags repository.TagRepository
}

func NewTagService(tags repository.TagRepository) *TagService {
	return &TagService{tags: tags}
}

// List 按站点返回标签（sourceID 为 0 时返回全部站点），并附带影片数。
func (s *TagService) List(sourceID uint, p ...Principal) ([]TagView, error) {
	visible := principalOf(p...).Visible()
	items, err := s.tags.List(sourceID, visible)
	if err != nil {
		return nil, err
	}
	counts, err := s.tags.CountVideosByTag(visible)
	if err != nil {
		return nil, err
	}
	out := make([]TagView, 0, len(items))
	for _, tag := range items {
		out = append(out, TagView{Tag: tag, VideoCount: counts[tag.ID]})
	}
	return out, nil
}
