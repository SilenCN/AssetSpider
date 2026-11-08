package repository

import (
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
	"gorm.io/gorm"
)

// JobRepository 是 CrawlJob 实体的数据访问契约。
type JobRepository interface {
	CreateJob(job *model.CrawlJob) error
	FinishJob(job *model.CrawlJob, status, message string) error
	ListJobs(sourceID uint, limit int) ([]model.CrawlJob, error)
}

// jobRepository 是 CrawlJob 实体专属的仓储对象。
type jobRepository struct {
	db *gorm.DB
}

func NewJobRepository(db *gorm.DB) JobRepository {
	return &jobRepository{db: db}
}

var _ JobRepository = (*jobRepository)(nil)

// CreateJob 创建一条 running 状态的任务记录。
func (r *jobRepository) CreateJob(job *model.CrawlJob) error {
	return r.db.Create(job).Error
}

// FinishJob 更新任务终态。
func (r *jobRepository) FinishJob(job *model.CrawlJob, status, message string) error {
	now := time.Now()
	return r.db.Model(job).Updates(map[string]interface{}{
		"status":      status,
		"message":     message,
		"fetched":     job.Fetched,
		"new":         job.New,
		"updated":     job.Updated,
		"unchanged":   job.Unchanged,
		"pages":       job.Pages,
		"total":       job.Total,
		"finished_at": now,
	}).Error
}

// ListJobs 返回最近的采集记录。
func (r *jobRepository) ListJobs(sourceID uint, limit int) ([]model.CrawlJob, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	q := r.db.Model(&model.CrawlJob{})
	if sourceID > 0 {
		q = q.Where("source_id = ?", sourceID)
	}
	var items []model.CrawlJob
	if err := q.Order("id DESC").Limit(limit).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}
