package model

import "time"

// Source 表示一个采集站点配置。
//
// 站点信息保存在数据库里，可由前端增删改；采集器按 Format 解析其 JSON 接口。
// 目前支持 seacms_json（海洋CMS / 苹果CMS 系 api.php/provide/vod/?ac=list）。
type Source struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	Name    string `gorm:"size:128;not null" json:"name"`
	APIBase string `gorm:"size:512;not null" json:"apiBase"`
	Format  string `gorm:"size:32;not null;default:seacms_json" json:"format"`

	// ExtraParams 是附加查询串，例如 "t=1&wd=电影"，可覆盖默认的 ac=list。
	ExtraParams string `gorm:"size:512" json:"extraParams"`

	// 不设 default:true，否则 GORM 会把显式的 false 当作零值跳过并套用默认值。
	Enabled          bool `gorm:"not null;index" json:"enabled"`
	IncrementalHours int  `gorm:"not null;default:24" json:"incrementalHours"`
	MaxPages         int  `gorm:"not null;default:0" json:"maxPages"` // 0 表示使用全局配置

	// 采集状态与增量游标（由采集器回写，前端只读）。
	LastSyncAt     *time.Time `json:"lastSyncAt"`
	LastFullSyncAt *time.Time `json:"lastFullSyncAt"`
	LastMode       string     `gorm:"size:16" json:"lastMode"`
	LastCursor     string     `gorm:"size:32" json:"lastCursor"` // 已采到的最大 vod_time
	LastStatus     string     `gorm:"size:16" json:"lastStatus"` // success / failed / never
	LastError      string     `gorm:"size:1024" json:"lastError"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
