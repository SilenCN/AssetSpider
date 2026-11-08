package repository

import (
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
	"gorm.io/gorm"
)

// JobRepository 是 CrawlJob 实体的数据访问契约。
type JobRepository interface {
	CreateJob(job *model.CrawlJob) error
	StartJob(job *model.CrawlJob) error
	FinishJob(job *model.CrawlJob, status, message string) error
	GetJob(id uint) (*model.CrawlJob, error)
	LastJob(sourceID uint, mode string) (*model.CrawlJob, error)
	ResetInterrupted(message string) (int64, error)
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

// StartJob 把已排队的任务标记为运行中，并记录真正的开始时间。
func (r *jobRepository) StartJob(job *model.CrawlJob) error {
	job.Status = model.JobRunning
	job.StartedAt = time.Now()
	return r.db.Model(job).Updates(map[string]interface{}{
		"status":     job.Status,
		"started_at": job.StartedAt,
	}).Error
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
		"resume_page": job.ResumePage,
		"finished_at": now,
	}).Error
}

// GetJob 按主键读取单条采集记录。
func (r *jobRepository) GetJob(id uint) (*model.CrawlJob, error) {
	var job model.CrawlJob
	err := r.db.Where("id = ?", id).Limit(1).Find(&job).Error
	if err != nil {
		return nil, err
	}
	if job.ID == 0 {
		return nil, ErrNotFound
	}
	return &job, nil
}

// LastJob 返回某站点某模式下最近的一条采集记录，用于判断能否续采。
func (r *jobRepository) LastJob(sourceID uint, mode string) (*model.CrawlJob, error) {
	var job model.CrawlJob
	err := r.db.Where("source_id = ? AND mode = ?", sourceID, mode).
		Order("id DESC").Limit(1).Find(&job).Error
	if err != nil {
		return nil, err
	}
	if job.ID == 0 {
		return nil, ErrNotFound
	}
	return &job, nil
}

// ResetInterrupted 把上次进程遗留的 running / queued 任务标记为失败。
//
// 任务调度只存在于内存中，进程重启后这些记录不可能再推进；不清理的话
// 页面会一直显示「进行中」。已暂停（paused）的任务不在此列——它们可以继续。
func (r *jobRepository) ResetInterrupted(message string) (int64, error) {
	res := r.db.Model(&model.CrawlJob{}).
		Where("status IN ?", []string{model.JobRunning, model.JobQueued}).
		Updates(map[string]interface{}{
			"status":      model.JobFailed,
			"message":     message,
			"finished_at": time.Now(),
		})
	return res.RowsAffected, res.Error
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
