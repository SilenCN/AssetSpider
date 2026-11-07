package model

import "time"

// Video 是一条从站点采集并入库的影片记录。
//
// (SourceID, VodID) 为复合唯一索引：全量/增量重复采集同一影片时执行 upsert，
// 不会产生重复行。Hash 用于判断内容是否变化，避免无意义的写库。
type Video struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	SourceID uint   `gorm:"not null;uniqueIndex:idx_source_vod,priority:1" json:"sourceId"`
	VodID    string `gorm:"size:64;not null;uniqueIndex:idx_source_vod,priority:2" json:"vodId"`

	Name     string `gorm:"size:255;index" json:"name"`
	TypeID   string `gorm:"size:32" json:"typeId"`
	TypeName string `gorm:"size:64" json:"typeName"`
	En       string `gorm:"size:128" json:"en"`
	Pic      string `gorm:"size:512" json:"pic"`
	Remarks  string `gorm:"size:128" json:"remarks"`
	PlayFrom string `gorm:"size:128" json:"playFrom"`
	Area     string `gorm:"size:64" json:"area"`
	Lang     string `gorm:"size:64" json:"lang"`
	Year     string `gorm:"size:16" json:"year"`
	Serial   string `gorm:"size:16" json:"serial"`
	Actor    string `gorm:"size:512" json:"actor"`
	Director string `gorm:"size:256" json:"director"`
	Content  string `gorm:"type:text" json:"content"`
	PlayURL  string `gorm:"type:text" json:"playUrl"`
	DownURL  string `gorm:"type:text" json:"downUrl"`

	VodTime string `gorm:"size:32;index" json:"vodTime"` // 站点原始更新时间
	Hash    string `gorm:"size:64;index" json:"-"`       // 内容指纹
	Raw     string `gorm:"type:text" json:"-"`           // 原始 JSON 记录，备查

	FirstSeenAt time.Time `json:"firstSeenAt"`
	LastSeenAt  time.Time `json:"lastSeenAt"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
