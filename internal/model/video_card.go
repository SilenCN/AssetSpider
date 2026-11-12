package model

import "time"

// VideoCard 是资源库列表项，只包含卡片渲染需要的字段。
//
// 列表接口不再返回完整的 Video（90+ 标准字段、播放组、扩展字段等大文本），
// 否则每页 24 条都要把这些重字段从 SQLite 读出来、序列化再经网络传输，
// 其中 vod_play_list / vod_content / extra 往往有好几 KB。
// 点开影片时前端按 id 调详情接口取完整 Video，所以列表只需要能画出卡片。
//
// 字段与前端卡片一一对应：posterOf（封面三级回退）、scoreOf、episodeLabel
// （备注 / 集数 / 总集数）、metaLine（地域 / 年份 / 分类名）、tagListOf（标签）、
// 时间行（更新时间 / 首次入库时间）、以及详情跳转用的 id 与站点归属 sourceId。
//
// 这里是跨层传输的只读投影，不参与 AutoMigrate，也不挂在 Repositories 上。
type VideoCard struct {
	ID          uint      `gorm:"column:id" json:"id"`
	SourceID    uint      `gorm:"column:source_id" json:"sourceId"`
	VodID       string    `gorm:"column:vod_id" json:"vodId"`
	VodName     string    `gorm:"column:vod_name" json:"vodName"`
	VodPic      string    `gorm:"column:vod_pic" json:"vodPic"`
	VodPicThumb string    `gorm:"column:vod_pic_thumb" json:"vodPicThumb"`
	VodPicSlide string    `gorm:"column:vod_pic_slide" json:"vodPicSlide"`
	VodScore    string    `gorm:"column:vod_score" json:"vodScore"`
	VodRemarks  string    `gorm:"column:vod_remarks" json:"vodRemarks"`
	VodSerial   string    `gorm:"column:vod_serial" json:"vodSerial"`
	VodTotal    string    `gorm:"column:vod_total" json:"vodTotal"`
	VodArea     string    `gorm:"column:vod_area" json:"vodArea"`
	VodYear     string    `gorm:"column:vod_year" json:"vodYear"`
	VodTag      string    `gorm:"column:vod_tag" json:"vodTag"`
	CategoryID  uint      `gorm:"column:category_id" json:"categoryId"`
	VodTime     string    `gorm:"column:vod_time" json:"vodTime"`
	FirstSeenAt time.Time `gorm:"column:first_seen_at" json:"firstSeenAt"`
}
