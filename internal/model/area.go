package model

import "time"

// Area 是一个资源站下的地域（如「美国」「大陆」「日本」）。
//
// 地域与标签同构：它们都来自影片身上的一个文本字段（vod_area / vod_tag），
// 由采集过程拆分、按源去重后单独成表，再经连接表与影片建立多对多关系。
// 之所以不把地域继续当字符串留在 videos 上：
//   - 前端筛选下拉要的是「出现过哪些地域 + 各自多少部影片」，字符串列只能靠
//     DISTINCT 全表扫描，数据量一大就是几十上百毫秒；
//   - 按地域筛选要模糊匹配（「大陆」「中国大陆」写法不一），命中率不可控。
//
// (source_id, name) 复合唯一，因此不同站点即使使用相同的地域名也互不干扰。
type Area struct {
	ID       uint   `gorm:"primaryKey" json:"id"`
	SourceID uint   `gorm:"column:source_id;not null;uniqueIndex:idx_source_area,priority:1;index" json:"sourceId"`
	Name     string `gorm:"column:name;size:128;not null;uniqueIndex:idx_source_area,priority:2" json:"name"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// VideoArea 是影片与地域的多对多连接表。
//
// 站点实际数据里一条影片通常只有一个地域，但接口并不保证（存在「美国/英国」
// 这类写法），因此仍用连接表承载，与标签保持一致。
type VideoArea struct {
	VideoID uint `gorm:"column:video_id;primaryKey;index:idx_video_areas_area,priority:2" json:"videoId"`
	// idx_video_areas_area (area_id, video_id)：按地域筛选影片时先定位到该地域的
	// 全部 video_id，索引内天然有序，配合 ORDER BY videos.id DESC 可直接倒序读。
	AreaID uint `gorm:"column:area_id;primaryKey;index:idx_video_areas_area,priority:1" json:"areaId"`
}
