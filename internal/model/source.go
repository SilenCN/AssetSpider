package model

import "time"

// 采集格式常量。苹果CMS V10 的 Provide 接口（api.php/provide/vod/）使用同一套
// JSON 协议，seacms_json 与 maccms_json 由同一套解析器处理；旧数据可能仍是
// seacms_json，新建站点默认使用 maccms_json。
const (
	FormatSeaCMSJSON = "seacms_json"
	FormatMacCMSJSON = "maccms_json"
)

// Source 表示一个采集站点配置。
//
// 站点信息保存在数据库里，可由前端增删改；采集器按其 JSON 接口（苹果CMS V10
// Provide 协议：api.php/provide/vod/?ac=videolist）抓取并零丢失解析。
type Source struct {
	ID      uint   `gorm:"primaryKey" json:"id"`
	Name    string `gorm:"size:128;not null" json:"name"`
	APIBase string `gorm:"size:512;not null" json:"apiBase"`
	Format  string `gorm:"size:32;not null;default:maccms_json" json:"format"`

	// ExtraParams 是附加查询串，例如 "t=1&wd=电影"，可覆盖默认的 ac=videolist。
	ExtraParams string `gorm:"size:512" json:"extraParams"`

	// ParseAPI 是视频解析地址（如 https://jx.example.com/?url=）。
	// 配置后播放时把解析地址拼在播放地址前面，交给解析服务跳转播放。
	// 支持用 {url} 占位符指定插入位置，未包含占位符时直接前缀拼接。
	ParseAPI string `gorm:"size:512" json:"parseApi"`

	// PlayDomain 是视频播放域名（如 https://cdn.example.com）。
	// 配置后播放时用它替换原播放地址中的「协议 + 域名」，只保留原地址的路径与查询串。
	PlayDomain string `gorm:"size:255" json:"playDomain"`

	// PicAPI 是图片解析/代理地址（如 https://img.example.com/proxy?url=）。
	// 与解析播放地址同理：配置后封面等图片地址会拼在它的后面，支持 {url} 占位符。
	PicAPI string `gorm:"size:512" json:"picApi"`

	// PicDomain 是图片域名（如 https://img.example.com）。
	// 配置后封面等图片地址会用它替换原来的「协议 + 域名」。
	PicDomain string `gorm:"size:255" json:"picDomain"`

	// 不设 default:true，否则 GORM 会把显式的 false 当作零值跳过并套用默认值。
	Enabled          bool `gorm:"not null;index" json:"enabled"`
	IncrementalHours int  `gorm:"not null;default:24" json:"incrementalHours"`
	MaxPages         int  `gorm:"not null;default:0" json:"maxPages"` // 0 表示使用全局配置

	// 定时增量采集：ScheduleEnabled 打开且 ScheduleCron 命中当前分钟时，
	// 后台调度器自动发起一次 incremental 任务。cron 为 5 段标准表达式
	// （分 时 日 月 周），见 utils.ParseCron。ScheduleLastAt 记录上次自动触发
	// 的时刻，用于同一分钟内去重（进程重启后也不会重复触发）。
	ScheduleEnabled bool       `gorm:"not null;default:false" json:"scheduleEnabled"`
	ScheduleCron    string     `gorm:"size:64" json:"scheduleCron"`
	ScheduleLastAt  *time.Time `json:"scheduleLastAt"`

	// SubscriptionID 是站点来源订阅；0 表示手动添加。
	// 刷新订阅时按「同一订阅 + 接口地址」匹配已有站点，因此该字段决定了
	// 「这条配置归谁管」：改它会影响下次刷新是否覆盖本站点。
	SubscriptionID uint `gorm:"not null;default:0" json:"subscriptionId"`

	// 采集状态与增量游标（由采集器回写，前端只读）。
	LastSyncAt     *time.Time `json:"lastSyncAt"`
	LastFullSyncAt *time.Time `json:"lastFullSyncAt"`
	LastMode       string     `gorm:"size:16" json:"lastMode"`
	LastCursor     string     `gorm:"size:32" json:"lastCursor"` // 已采到的最大 vod_time
	LastStatus     string     `gorm:"size:16" json:"lastStatus"` // success / failed / never
	LastError      string     `gorm:"size:1024" json:"lastError"`

	// VideoCount 是该站点当前已入库的影片总数（只读快照，由采集任务维护）。
	//
	// 采集任务运行期间按进度落库间隔（默认 60 秒）刷新一次，任务结束时再刷新一次；
	// 服务启动时也会按 videos 表整体重算一次，避免升级后历史站点停留在默认值 0。
	// 存成列而不是每次列表都 COUNT(*)，让站点列表的查询代价与影片总量无关。
	VideoCount int64 `gorm:"not null;default:0" json:"videoCount"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
