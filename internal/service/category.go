package service

import (
	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

// CategoryView 在分类上附加该分类下的影片数量。
type CategoryView struct {
	model.Category
	VideoCount int64 `json:"videoCount"`
}

// CategoryService 提供分类查询；分类本身由采集过程自动维护，不提供手工增删改。
type CategoryService struct {
	categories repository.CategoryRepository
	videos     repository.VideoRepository
}

func NewCategoryService(categories repository.CategoryRepository, videos repository.VideoRepository) *CategoryService {
	return &CategoryService{categories: categories, videos: videos}
}

// List 按站点返回分类（sourceID 为 0 时返回全部站点），并附带影片数。
func (s *CategoryService) List(sourceID uint) ([]CategoryView, error) {
	items, err := s.categories.List(sourceID)
	if err != nil {
		return nil, err
	}
	counts, err := s.videos.CountVideosByCategory()
	if err != nil {
		return nil, err
	}
	out := make([]CategoryView, 0, len(items))
	for _, cat := range items {
		out = append(out, CategoryView{Category: cat, VideoCount: counts[cat.ID]})
	}
	return out, nil
}

func (s *CategoryService) Get(id uint) (*model.Category, error) {
	cat, err := s.categories.Get(id)
	if err != nil {
		return nil, wrapRepo(err)
	}
	return cat, nil
}
