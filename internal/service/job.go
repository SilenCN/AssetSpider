package service

import (
	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

// LiveSource 提供运行中任务的实时进度，由调度层（CrawlService）实现。
// 采集记录本身只存终态统计，实时进度只存在于内存，因此用接口旁挂注入，
// 避免 JobService 反向依赖采集调度。
type LiveSource interface {
	LiveTask(jobID uint) *TaskView
}

// JobView 是一条采集记录加上（可选的）实时进度。
// 任务结束后 Live 为空，统计值全部来自落库的终态数据。
type JobView struct {
	model.CrawlJob
	Live *TaskView `json:"live,omitempty"`
}

// JobService 提供采集记录查询。
type JobService struct {
	jobs repository.JobRepository
	live LiveSource
}

func NewJobService(jobs repository.JobRepository) *JobService {
	return &JobService{jobs: jobs}
}

// SetLiveSource 注入实时进度来源（可为空，为空时只返回落库数据）。
func (s *JobService) SetLiveSource(live LiveSource) { s.live = live }

func (s *JobService) List(sourceID uint, limit int) ([]JobView, error) {
	items, err := s.jobs.ListJobs(sourceID, limit)
	if err != nil {
		return nil, err
	}
	views := make([]JobView, len(items))
	for i := range items {
		views[i] = JobView{CrawlJob: items[i]}
		if s.live != nil {
			views[i].Live = s.live.LiveTask(items[i].ID)
		}
	}
	return views, nil
}
