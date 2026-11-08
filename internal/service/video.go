package service

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

// VideoList 是影片分页查询的结果 DTO。
type VideoList struct {
	Items []model.Video `json:"items"`
	Total int64         `json:"total"`
	Page  int           `json:"page"`
	Size  int           `json:"size"`
}

// VideoService 处理入库影片的查询与分页规则。
type VideoService struct {
	videos repository.VideoRepository
}

func NewVideoService(videos repository.VideoRepository) *VideoService {
	return &VideoService{videos: videos}
}

func (s *VideoService) List(sourceID, categoryID uint, keyword string, page, size int) (VideoList, error) {
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 200 {
		size = 20
	}
	items, total, err := s.videos.ListVideos(repository.VideoQuery{
		SourceID:   sourceID,
		CategoryID: categoryID,
		Keyword:    strings.TrimSpace(keyword),
		Page:       page,
		Size:       size,
	})
	if err != nil {
		return VideoList{}, err
	}
	return VideoList{Items: items, Total: total, Page: page, Size: size}, nil
}

// Get 返回单条影片完整记录（含原始 JSON 与扩展字段）。
func (s *VideoService) Get(id uint) (*model.Video, error) {
	v, err := s.videos.GetVideo(id)
	if err != nil {
		return nil, wrapRepo(err)
	}
	return v, nil
}

// GetPlayGroups 返回单条影片按苹果CMS 字段解析出的结构化播放组。
// 若影片无播放地址（vod_play_list 为空），返回空切片而非错误。
func (s *VideoService) GetPlayGroups(id uint) ([]model.PlayGroup, error) {
	v, err := s.videos.GetVideo(id)
	if err != nil {
		return nil, wrapRepo(err)
	}
	if v.VodPlayList == "" {
		return []model.PlayGroup{}, nil
	}
	var groups []model.PlayGroup
	if err := json.Unmarshal([]byte(v.VodPlayList), &groups); err != nil {
		return nil, fmt.Errorf("播放组数据损坏: %w", err)
	}
	return groups, nil
}
