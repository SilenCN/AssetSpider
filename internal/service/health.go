package service

import "github.com/SilenCN/AssetSpider/internal/repository"

// HealthStatus 是健康检查响应。
type HealthStatus struct {
	Status   string `json:"status"`
	Database string `json:"database"`
	Sources  int    `json:"sources"`
	Videos   int64  `json:"videos"`
}

// HealthService 汇总运行态概览，只读两个实体仓储。
type HealthService struct {
	sources repository.SourceRepository
	videos  repository.VideoRepository
	dbPath  string
}

func NewHealthService(sources repository.SourceRepository, videos repository.VideoRepository, dbPath string) *HealthService {
	return &HealthService{sources: sources, videos: videos, dbPath: dbPath}
}

func (s *HealthService) Status() HealthStatus {
	srcs, _ := s.sources.ListSources()
	_, total, _ := s.videos.ListVideos(repository.VideoQuery{Page: 1, Size: 1})
	return HealthStatus{
		Status:   "ok",
		Database: s.dbPath,
		Sources:  len(srcs),
		Videos:   total,
	}
}
