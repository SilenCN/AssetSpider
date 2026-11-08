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

// CategoryNode 是分类树的节点，自带子分类列表（最多二级）。
type CategoryNode struct {
	CategoryView
	Children []CategoryNode `json:"children,omitempty"`
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

// Tree 按站点返回分类的二级树（sourceID 为 0 时返回全部站点），父分类在前、子分类嵌套其下。
// 苹果CMS 分类最多两级，因此用指针索引一次组装即可，避免值拷贝导致子节点丢失。
func (s *CategoryService) Tree(sourceID uint) ([]CategoryNode, error) {
	items, err := s.categories.List(sourceID)
	if err != nil {
		return nil, err
	}
	counts, err := s.videos.CountVideosByCategory()
	if err != nil {
		return nil, err
	}
	byID := make(map[uint]*CategoryNode, len(items))
	for _, cat := range items {
		byID[cat.ID] = &CategoryNode{CategoryView: CategoryView{Category: cat, VideoCount: counts[cat.ID]}}
	}
	roots := make([]*CategoryNode, 0, len(items))
	for _, cat := range items {
		node := byID[cat.ID]
		if cat.ParentID != 0 {
			if p, ok := byID[cat.ParentID]; ok {
				p.Children = append(p.Children, *node)
				continue
			}
		}
		roots = append(roots, node)
	}
	out := make([]CategoryNode, len(roots))
	for i, n := range roots {
		out[i] = *n
	}
	return out, nil
}
