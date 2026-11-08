package service

import (
	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

// JobService 提供采集记录查询。
type JobService struct {
	jobs repository.JobRepository
}

func NewJobService(jobs repository.JobRepository) *JobService {
	return &JobService{jobs: jobs}
}

func (s *JobService) List(sourceID uint, limit int) ([]model.CrawlJob, error) {
	return s.jobs.ListJobs(sourceID, limit)
}
