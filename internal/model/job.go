package model

import "time"

// 采集模式。
const (
	ModeFull        = "full"        // 全量：遍历所有分页
	ModeIncremental = "incremental" // 增量：按 h 参数 + 时间游标只取近期更新
)

// 采集任务状态。
const (
	JobRunning = "running"
	JobSuccess = "success"
	JobFailed  = "failed"
)

// CrawlJob 记录一次采集的执行结果，供前端查看采集历史。
type CrawlJob struct {
	ID         uint   `gorm:"primaryKey" json:"id"`
	SourceID   uint   `gorm:"index" json:"sourceId"`
	SourceName string `gorm:"size:128" json:"sourceName"`
	Mode       string `gorm:"size:16;not null;index" json:"mode"`
	Status     string `gorm:"size:16;not null;index" json:"status"`

	Fetched   int `json:"fetched"`   // 从接口拉取的总条数
	New       int `json:"new"`       // 新入库条数
	Updated   int `json:"updated"`   // 内容变化被更新条数
	Unchanged int `json:"unchanged"` // 无变化条数
	Pages     int `json:"pages"`     // 实际请求页数
	Total     int `json:"total"`     // 站点报告的总量

	Message    string     `gorm:"size:1024" json:"message"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	CreatedAt  time.Time  `json:"createdAt"`
}
