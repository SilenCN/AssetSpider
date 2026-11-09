package model

import "time"

// 采集模式。
const (
	ModeFull        = "full"        // 全量：遍历所有分页
	ModeIncremental = "incremental" // 增量：按 h 参数 + 时间游标只取近期更新
)

// 采集任务状态。
//
// queued 表示已进入队列、正在等待并发槽位；paused 表示被手动暂停，
// 进度（增量游标 / 全量续采页码）已保存，可从 ResumePage 继续。
const (
	JobQueued  = "queued"
	JobRunning = "running"
	JobPaused  = "paused"
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

	// ResumePage 是「下一次该从第几页开始」：全量采集被暂停或失败时写入，
	// 继续采集 / 手动重试时从这里续采，避免从头再拉一遍。
	// 采集正常结束或从第 1 页重新运行时为 1（表示从头开始）。
	ResumePage int `json:"resumePage"`

	// Attempt 是这条任务实际执行过的尝试次数（含失败后的自动重试），
	// 1 表示首次尝试就结束。用于在记录里区分「重试后成功」与「一次成功」。
	Attempt int `json:"attempt"`

	// RetryOf 是历史字段：旧版本「重新运行」会新开一条记录并在这里记下来源任务。
	// 现在继续 / 重试 / 重新运行都复用原记录，新记录不再产生该值（0）。
	// 仅用于展示来源，不参与调度。
	RetryOf uint `gorm:"index" json:"retryOf"`

	Message    string     `gorm:"size:1024" json:"message"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	CreatedAt  time.Time  `json:"createdAt"`
}
