package model

import "time"

// Subscription 表示一个「站点配置网络订阅」：一个远程 JSON 地址，里面包含若干
// 站点配置（格式与本系统导出的站点 JSON 一致）。本地按订阅路径拉取后写进站点表，
// 站点由此带上 SubscriptionID 归属，下次刷新即可按订阅批量更新。
//
// 订阅只是「配置分发」通道，不参与采集：站点仍然按自己的接口地址与增量策略工作，
// 订阅刷新失败也不会影响已入库的站点与影片。
type Subscription struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"size:128;not null" json:"name"`

	// URL 是订阅地址（http/https），返回站点数组、单条站点对象或含 sources 的导出文件。
	URL string `gorm:"size:1024;not null" json:"url"`

	// IntervalHours 是自动刷新间隔（小时）。0 表示不自动刷新，只能手动点「刷新」。
	// 判据是「上次成功拉取时间 + 间隔 <= 现在」，因此进程重启不会丢下次刷新时刻。
	IntervalHours int  `gorm:"not null;default:0" json:"intervalHours"`
	Enabled       bool `gorm:"not null;index" json:"enabled"`

	// 刷新状态（由刷新流程回写，前端只读）。
	LastSyncAt *time.Time `json:"lastSyncAt"`
	LastStatus string     `gorm:"size:16" json:"lastStatus"` // success / failed / never
	LastError  string     `gorm:"size:1024" json:"lastError"`

	// 上次刷新实际做了什么：新增 / 更新 / 停用（订阅里已移除）/ 跳过（校验失败）。
	LastCreated  int `gorm:"not null;default:0" json:"lastCreated"`
	LastUpdated  int `gorm:"not null;default:0" json:"lastUpdated"`
	LastDisabled int `gorm:"not null;default:0" json:"lastDisabled"`
	LastSkipped  int `gorm:"not null;default:0" json:"lastSkipped"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
