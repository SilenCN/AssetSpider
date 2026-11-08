package repository

import (
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
	"gorm.io/gorm"
)

// JobRepository 是 CrawlJob 实体的数据访问契约。
type JobRepository interface {
	CreateJob(job *model.CrawlJob) error
	ReopenJob(job *model.CrawlJob) error
	StartJob(job *model.CrawlJob) error
	SaveProgress(job *model.CrawlJob) error
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

// ReopenJob 把一条已结束的记录重新放回队列（手动重试复用原记录，不新开任务）。
//
// 统计值（拉取/新增/更新/未变/页数/总量）与 attempt 都按传入的 job 原样写回，
// 调用方先把它们置为「历史累计值」再调用，重试后继续累加。
// 这里必须用 map 更新：finished_at 要写回 NULL、message 要清空，
// 用结构体 Updates 会被 GORM 当成零值跳过。
func (r *jobRepository) ReopenJob(job *model.CrawlJob) error {
	job.Status = model.JobQueued
	job.StartedAt = time.Now()
	job.FinishedAt = nil
	job.Message = ""
	return r.db.Model(job).Updates(map[string]interface{}{
		"status":      job.Status,
		"started_at":  job.StartedAt,
		"finished_at": nil,
		"message":     "",
		"resume_page": job.ResumePage,
		"pages":       job.Pages,
		"fetched":     job.Fetched,
		"new":         job.New,
		"updated":     job.Updated,
		"unchanged":   job.Unchanged,
		"total":       job.Total,
		"attempt":     job.Attempt,
	}).Error
}

// SaveProgress 把运行中任务的进度写库（由调度层定期调用）。
//
// 只写统计、页码、续采页与尝试次数，**不动 status / finished_at / message**：
// 这些属于终态，必须由 FinishJob 独占写入，否则定期落库会把已完成的任务
// 覆盖回 running。进程意外退出后，重启时 ResetInterrupted 会把 running 记
// 录标为中断，此时 resume_page 已指向下一页，重试即可从断点续采。
func (r *jobRepository) SaveProgress(job *model.CrawlJob) error {
	return r.db.Model(&model.CrawlJob{}).Where("id = ?", job.ID).
		Updates(map[string]interface{}{
			"pages":       job.Pages,
			"fetched":     job.Fetched,
			"new":         job.New,
			"updated":     job.Updated,
			"unchanged":   job.Unchanged,
			"total":       job.Total,
			"resume_page": job.ResumePage,
			"attempt":     job.Attempt,
		}).Error
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
		"attempt":     job.Attempt,
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
