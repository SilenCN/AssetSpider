package model

import "time"

// Tag 是一个资源站下的标签。
//
// 标签来自苹果CMS / 海洋CMS 的 vod_tag 字段（多个标签通常用逗号分隔），
// 由采集过程按影片自带的标签自动拆分、按源去重创建，每个资源站一套：
// (source_id, name) 复合唯一，因此不同站点即使使用相同的标签名也互不干扰。
// 标签与影片是多对多关系，经 video_tags 连接表关联。
type Tag struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	SourceID uint   `gorm:"column:source_id;not null;uniqueIndex:idx_source_tag,priority:1;index" json:"sourceId"`
	Name     string `gorm:"column:name;size:128;not null;uniqueIndex:idx_source_tag,priority:2" json:"name"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// VideoTag 是影片与标签的多对多连接表。
//
// 一条影片可带多个标签，一个标签可出现在多部影片上，因此用独立连接表承载，
// 主键为 (video_id, tag_id) 的复合键，避免重复关联。
type VideoTag struct {
	VideoID uint `gorm:"column:video_id;primaryKey" json:"videoId"`
	TagID   uint `gorm:"column:tag_id;primaryKey" json:"tagId"`
}
