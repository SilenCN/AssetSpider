package model

import "time"

// Category 是一个资源站下的视频分类。
//
// 分类由采集过程按视频自带的 type_id / type_name 自动创建，每个资源站一套：
// (source_id, cat_key) 复合唯一，因此不同站点即使使用相同的 type_id 也互不干扰。
// cat_key 由 utils.CategoryKey 生成，站点未返回 type_id 时回退用分类名做键。
type Category struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	SourceID uint   `gorm:"column:source_id;not null;uniqueIndex:idx_source_cat,priority:1;index" json:"sourceId"`
	Key      string `gorm:"column:cat_key;size:160;not null;uniqueIndex:idx_source_cat,priority:2" json:"key"`
	TypeID   string `gorm:"column:type_id;size:64" json:"typeId"`
	Name     string `gorm:"column:name;size:128;not null" json:"name"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
