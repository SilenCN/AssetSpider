package model

import "time"

// Category 是一个资源站下的视频分类。
//
// 分类由采集过程按视频自带的 type_id / type_name 自动创建，每个资源站一套：
// (source_id, cat_key) 复合唯一，因此不同站点即使使用相同的 type_id 也互不干扰。
// cat_key 由 utils.CategoryKey 生成，站点未返回 type_id 时回退用分类名做键。
//
// 遵循苹果CMS 的二级分类模型：分类树以 type_pid（父级分类 ID）表达层级，
// TypePID 为空表示一级分类；ParentID 指向同站内的父分类主键（0 表示无），
// Level 标记 1=一级 / 2=二级。苹果CMS 视频用 type_id_1 携带所属一级分类 ID，
// 采集时据此把二级分类挂到一级分类之下。
type Category struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	SourceID uint   `gorm:"column:source_id;not null;uniqueIndex:idx_source_cat,priority:1;index" json:"sourceId"`
	Key      string `gorm:"column:cat_key;size:160;not null;uniqueIndex:idx_source_cat,priority:2" json:"key"`
	TypeID   string `gorm:"column:type_id;size:64" json:"typeId"`
	Name     string `gorm:"column:name;size:128;not null" json:"name"`

	// 二级分类支持（对齐苹果CMS type_pid 模型）
	TypePID  string `gorm:"column:type_pid;size:64" json:"typePid"` // 父级分类 ID（苹果CMS type_pid），空表示一级分类
	ParentID uint   `gorm:"column:parent_id;index" json:"parentId"` // 父分类主键（同站），0 表示无
	Level    int    `gorm:"column:level;default:1" json:"level"`    // 1=一级 2=二级

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
