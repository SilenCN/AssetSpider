package service

import (
	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

// AreaView 在地域上附加该地域下的影片数量。
type AreaView struct {
	model.Area
	VideoCount int64 `json:"videoCount"`
}

// AreaService 提供地域查询；地域本身由采集过程自动维护，不提供手工增删改。
//
// 与标签服务同构：列表带影片数，按站点隔离，受可见性约束。
type AreaService struct {
	areas repository.AreaRepository
}

func NewAreaService(areas repository.AreaRepository) *AreaService {
	return &AreaService{areas: areas}
}

// List 按站点返回地域（sourceID 为 0 时返回全部站点），并附带影片数。
func (s *AreaService) List(sourceID uint, p ...Principal) ([]AreaView, error) {
	visible := principalOf(p...).Visible()
	items, err := s.areas.List(sourceID, visible)
	if err != nil {
		return nil, err
	}
	counts, err := s.areas.CountVideosByArea(visible)
	if err != nil {
		return nil, err
	}
	out := make([]AreaView, 0, len(items))
	for _, area := range items {
		out = append(out, AreaView{Area: area, VideoCount: counts[area.ID]})
	}
	return out, nil
}
