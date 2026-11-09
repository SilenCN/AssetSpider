package model

import "time"

// Video 是一条从站点采集并入库的影片记录。
//
// 字段与 VodFields 清单一一对应，覆盖苹果CMS / 海洋CMS V10 资源站接口的全部标准字段。
// 数据完整性由两层保证：
//   - 结构化的标准字段列：便于 SQL 查询、筛选与展示；
//   - Extra：接口返回但不在标准字段清单内的字段（自定义字段、嵌套对象、数组等），按原始 JSON 值保存。
//
// 不再保存整条记录的原始 JSON 全文（原 raw 列）：标准列 + Extra 已覆盖全部字段，
// 再存一份全文只会重复占用存储。
//
// (SourceID, VodID) 为复合唯一索引：全量/增量重复采集同一影片时执行 upsert，
// 不会产生重复行。Hash 由该条记录的全部键值计算，任何字段变化都会触发更新。
type Video struct {
	// 索引说明（读路径的性能关键）：
	//   idx_source_vod       唯一键，采集 upsert 与 vod_id 反查主键都走它；
	//   idx_videos_source_time  (source_id, vod_time)：单站点按更新时间排序/翻页直接读索引，
	//                          不再「先按站点过滤再全量排序」；SQLite 的索引末尾隐含 rowid
	//                          （即 id），所以 vod_time 相同时的 id 次序也能一并满足；
	//   idx_videos_source_id    (source_id, id)：单站点 + 默认 id 倒序；
	//   （vod_area 不再单独建索引：地域已拆成 areas + video_areas，筛选项与筛选都走连接表；
	//    关键词检索里的 vod_area LIKE '%x%' 本来也用不上索引）
	//   idx_videos_category_id 分类筛选与按分类聚合影片数。
	ID       uint `gorm:"primaryKey;index:idx_videos_source_id,priority:2" json:"id"`
	SourceID uint `gorm:"column:source_id;not null;uniqueIndex:idx_source_vod,priority:1;index:idx_videos_source_time,priority:1;index:idx_videos_source_id,priority:1" json:"sourceId"`

	// CategoryID 由采集时按本站分类自动归档回填；0 表示接口未返回分类信息。
	CategoryID uint `gorm:"column:category_id;index" json:"categoryId"`

	// ---- 基础信息 ----
	VodID          string `gorm:"column:vod_id;size:64;not null;uniqueIndex:idx_source_vod,priority:2" json:"vodId"`
	TypeID         string `gorm:"column:type_id" json:"typeId"`
	TypeID1        string `gorm:"column:type_id_1" json:"typeId1"`
	GroupID        string `gorm:"column:group_id" json:"groupId"`
	VodName        string `gorm:"column:vod_name;index" json:"vodName"`
	VodSub         string `gorm:"column:vod_sub" json:"vodSub"`
	VodEn          string `gorm:"column:vod_en" json:"vodEn"`
	VodStatus      string `gorm:"column:vod_status" json:"vodStatus"`
	VodAuditRemark string `gorm:"column:vod_audit_remark" json:"vodAuditRemark"`
	VodLetter      string `gorm:"column:vod_letter" json:"vodLetter"`
	VodColor       string `gorm:"column:vod_color" json:"vodColor"`
	VodTag         string `gorm:"column:vod_tag" json:"vodTag"`
	VodClass       string `gorm:"column:vod_class" json:"vodClass"`
	TypeName       string `gorm:"column:type_name" json:"typeName"`
	VodType        string `gorm:"column:type;type:text" json:"vodType"`

	// ---- 图片 ----
	VodPic           string `gorm:"column:vod_pic" json:"vodPic"`
	VodPicThumb      string `gorm:"column:vod_pic_thumb" json:"vodPicThumb"`
	VodPicSlide      string `gorm:"column:vod_pic_slide" json:"vodPicSlide"`
	VodPicOriginal   string `gorm:"column:vod_pic_original" json:"vodPicOriginal"`
	VodPicScreenshot string `gorm:"column:vod_pic_screenshot;type:text" json:"vodPicScreenshot"`

	// ---- 演职人员 ----
	VodActor    string `gorm:"column:vod_actor" json:"vodActor"`
	VodDirector string `gorm:"column:vod_director" json:"vodDirector"`
	VodWriter   string `gorm:"column:vod_writer" json:"vodWriter"`
	VodBehind   string `gorm:"column:vod_behind" json:"vodBehind"`

	// ---- 简介与备注 ----
	VodBlurb   string `gorm:"column:vod_blurb;type:text" json:"vodBlurb"`
	VodRemarks string `gorm:"column:vod_remarks" json:"vodRemarks"`
	VodContent string `gorm:"column:vod_content;type:text" json:"vodContent"`

	// ---- 上映与集数 ----
	VodPubdate     string `gorm:"column:vod_pubdate" json:"vodPubdate"`
	VodPublishTime string `gorm:"column:vod_publish_time" json:"vodPublishTime"`
	VodTotal       string `gorm:"column:vod_total" json:"vodTotal"`
	VodSerial      string `gorm:"column:vod_serial" json:"vodSerial"`
	VodTv          string `gorm:"column:vod_tv" json:"vodTv"`
	VodWeekday     string `gorm:"column:vod_weekday" json:"vodWeekday"`

	// ---- 属性 ----
	VodArea     string `gorm:"column:vod_area" json:"vodArea"` // 原始文本保留用于展示；筛选走 video_areas
	VodLang     string `gorm:"column:vod_lang" json:"vodLang"`
	VodYear     string `gorm:"column:vod_year" json:"vodYear"`
	VodVersion  string `gorm:"column:vod_version" json:"vodVersion"`
	VodState    string `gorm:"column:vod_state" json:"vodState"`
	VodAuthor   string `gorm:"column:vod_author" json:"vodAuthor"`
	VodJumpurl  string `gorm:"column:vod_jumpurl" json:"vodJumpurl"`
	VodDuration string `gorm:"column:vod_duration" json:"vodDuration"`

	// ---- 模板与标志 ----
	VodTpl       string `gorm:"column:vod_tpl" json:"vodTpl"`
	VodTplPlay   string `gorm:"column:vod_tpl_play" json:"vodTplPlay"`
	VodTplDown   string `gorm:"column:vod_tpl_down" json:"vodTplDown"`
	VodIsend     string `gorm:"column:vod_isend" json:"vodIsend"`
	VodLock      string `gorm:"column:vod_lock" json:"vodLock"`
	VodLevel     string `gorm:"column:vod_level" json:"vodLevel"`
	VodCopyright string `gorm:"column:vod_copyright" json:"vodCopyright"`

	// ---- 积分与统计 ----
	VodPoints     string `gorm:"column:vod_points" json:"vodPoints"`
	VodPointsPlay string `gorm:"column:vod_points_play" json:"vodPointsPlay"`
	VodPointsDown string `gorm:"column:vod_points_down" json:"vodPointsDown"`
	VodHits       string `gorm:"column:vod_hits" json:"vodHits"`
	VodHitsDay    string `gorm:"column:vod_hits_day" json:"vodHitsDay"`
	VodHitsWeek   string `gorm:"column:vod_hits_week" json:"vodHitsWeek"`
	VodHitsMonth  string `gorm:"column:vod_hits_month" json:"vodHitsMonth"`
	VodUp         string `gorm:"column:vod_up" json:"vodUp"`
	VodDown       string `gorm:"column:vod_down" json:"vodDown"`
	VodScore      string `gorm:"column:vod_score" json:"vodScore"`
	VodScoreAll   string `gorm:"column:vod_score_all" json:"vodScoreAll"`
	VodScoreNum   string `gorm:"column:vod_score_num" json:"vodScoreNum"`
	VodShareCount string `gorm:"column:vod_share_count" json:"vodShareCount"`

	// ---- 时间 ----
	VodTime        string `gorm:"column:vod_time;size:32;index;index:idx_videos_source_time,priority:2" json:"vodTime"` // 站点原始更新时间，归一化后可字典序比较
	VodTimeAdd     string `gorm:"column:vod_time_add" json:"vodTimeAdd"`
	VodTimeHits    string `gorm:"column:vod_time_hits" json:"vodTimeHits"`
	VodTimeMake    string `gorm:"column:vod_time_make" json:"vodTimeMake"`
	VodRecycleTime string `gorm:"column:vod_recycle_time" json:"vodRecycleTime"`
	VodTrysee      string `gorm:"column:vod_trysee" json:"vodTrysee"`

	// ---- 来源与关联 ----
	VodDoubanID    string `gorm:"column:vod_douban_id" json:"vodDoubanId"`
	VodDoubanScore string `gorm:"column:vod_douban_score" json:"vodDoubanScore"`
	VodReurl       string `gorm:"column:vod_reurl" json:"vodReurl"`
	VodRelVod      string `gorm:"column:vod_rel_vod" json:"vodRelVod"`
	VodRelArt      string `gorm:"column:vod_rel_art" json:"vodRelArt"`

	// ---- 密码 ----
	VodPwd        string `gorm:"column:vod_pwd" json:"vodPwd"`
	VodPwdURL     string `gorm:"column:vod_pwd_url" json:"vodPwdUrl"`
	VodPwdPlay    string `gorm:"column:vod_pwd_play" json:"vodPwdPlay"`
	VodPwdPlayURL string `gorm:"column:vod_pwd_play_url" json:"vodPwdPlayUrl"`
	VodPwdDown    string `gorm:"column:vod_pwd_down" json:"vodPwdDown"`
	VodPwdDownURL string `gorm:"column:vod_pwd_down_url" json:"vodPwdDownUrl"`

	// ---- 播放 ----
	VodPlayFrom   string `gorm:"column:vod_play_from" json:"vodPlayFrom"`
	VodPlayServer string `gorm:"column:vod_play_server" json:"vodPlayServer"`
	VodPlayNote   string `gorm:"column:vod_play_note" json:"vodPlayNote"`
	VodPlayURL    string `gorm:"column:vod_play_url;type:text" json:"vodPlayUrl"`
	VodPlayList   string `gorm:"column:vod_play_list;type:text" json:"vodPlayList"`

	// ---- 下载 ----
	VodDownFrom   string `gorm:"column:vod_down_from" json:"vodDownFrom"`
	VodDownServer string `gorm:"column:vod_down_server" json:"vodDownServer"`
	VodDownNote   string `gorm:"column:vod_down_note" json:"vodDownNote"`
	VodDownURL    string `gorm:"column:vod_down_url;type:text" json:"vodDownUrl"`
	VodDownList   string `gorm:"column:vod_down_list;type:text" json:"vodDownList"`

	// ---- 分集剧情 ----
	VodPlot       string `gorm:"column:vod_plot" json:"vodPlot"`
	VodPlotName   string `gorm:"column:vod_plot_name;type:text" json:"vodPlotName"`
	VodPlotDetail string `gorm:"column:vod_plot_detail;type:text" json:"vodPlotDetail"`

	// ---- 采集元数据 ----
	Hash  string `gorm:"column:hash;size:64;index" json:"-"`            // 全字段内容指纹
	Extra string `gorm:"column:extra;type:text" json:"extra,omitempty"` // 未映射字段（原始 JSON 值）

	FirstSeenAt time.Time `json:"firstSeenAt"`
	LastSeenAt  time.Time `json:"lastSeenAt"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
