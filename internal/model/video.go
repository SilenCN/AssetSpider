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
// 列类型按 MacCMS V10 安装表结构的实际长度设定（见 model.VodFields 的 Size）：
// 短字段是 VARCHAR(N)，长文本 / 解析后数组是 MEDIUMTEXT；所有值在 Go 侧仍是 string
// （保留站点原始值），因此不会把数字 / 时间戳字段改成数值列而丢原始格式。
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
	//   idx_videos_vod_year     年份筛选与年份下拉的 GROUP BY（单值列，无需拆表）；
	//   idx_videos_category_id 分类筛选与按分类聚合影片数。
	//
	// vod_name / hash 不再建单列索引：关键词检索走 FTS5 trigram（≥3 字）或
	// video_bigrams（2 字），前置通配符的 LIKE 用不上 B-Tree；hash 只在 Go 侧比对
	// 内容指纹，SQL 里没有按 hash 过滤的查询。历史库里已建出的这两个索引由
	// dropRetiredVideoIndexes 幂等删除。
	ID       uint `gorm:"primaryKey;index:idx_videos_source_id,priority:2" json:"id"`
	SourceID uint `gorm:"column:source_id;not null;uniqueIndex:idx_source_vod,priority:1;index:idx_videos_source_time,priority:1;index:idx_videos_source_id,priority:1" json:"sourceId"`

	// CategoryID 由采集时按本站分类自动归档回填；0 表示接口未返回分类信息。
	CategoryID uint `gorm:"column:category_id;index" json:"categoryId"`

	// ---- 基础信息 ----
	VodID    string `gorm:"column:vod_id;type:varchar(64);not null;uniqueIndex:idx_source_vod,priority:2" json:"vodId"`
	TypeID   string `gorm:"column:type_id;type:varchar(64)" json:"typeId"`
	TypeID1  string `gorm:"column:type_id_1;type:varchar(64)" json:"typeId1"`
	GroupID  string `gorm:"column:group_id;type:varchar(64)" json:"groupId"`
	VodName  string `gorm:"column:vod_name;type:varchar(255)" json:"vodName"`
	VodSub   string `gorm:"column:vod_sub;type:varchar(255)" json:"vodSub"`
	VodEn    string `gorm:"column:vod_en;type:varchar(255)" json:"vodEn"`
	VodColor string `gorm:"column:vod_color;type:varchar(16)" json:"vodColor"`
	VodTag   string `gorm:"column:vod_tag;type:varchar(255)" json:"vodTag"`
	VodClass string `gorm:"column:vod_class;type:varchar(255)" json:"vodClass"`
	TypeName string `gorm:"column:type_name;type:varchar(255)" json:"typeName"`
	VodType  string `gorm:"column:type;type:mediumtext" json:"vodType"`

	// ---- 图片 ----
	VodPic           string `gorm:"column:vod_pic;type:varchar(1024)" json:"vodPic"`
	VodPicThumb      string `gorm:"column:vod_pic_thumb;type:varchar(1024)" json:"vodPicThumb"`
	VodPicSlide      string `gorm:"column:vod_pic_slide;type:varchar(1024)" json:"vodPicSlide"`
	VodPicOriginal   string `gorm:"column:vod_pic_original;type:varchar(1024)" json:"vodPicOriginal"`
	VodPicScreenshot string `gorm:"column:vod_pic_screenshot;type:mediumtext" json:"vodPicScreenshot"`

	// ---- 演职人员 ----
	VodActor    string `gorm:"column:vod_actor;type:varchar(255)" json:"vodActor"`
	VodDirector string `gorm:"column:vod_director;type:varchar(255)" json:"vodDirector"`
	VodWriter   string `gorm:"column:vod_writer;type:varchar(255)" json:"vodWriter"`
	VodBehind   string `gorm:"column:vod_behind;type:varchar(255)" json:"vodBehind"`

	// ---- 简介与备注 ----
	VodBlurb   string `gorm:"column:vod_blurb;type:mediumtext" json:"vodBlurb"`
	VodRemarks string `gorm:"column:vod_remarks;type:varchar(255)" json:"vodRemarks"`
	VodContent string `gorm:"column:vod_content;type:mediumtext" json:"vodContent"`

	// ---- 上映与集数 ----
	VodPubdate     string `gorm:"column:vod_pubdate;type:varchar(255)" json:"vodPubdate"`
	VodPublishTime string `gorm:"column:vod_publish_time;type:varchar(32)" json:"vodPublishTime"`
	VodTotal       string `gorm:"column:vod_total;type:varchar(32)" json:"vodTotal"`
	VodSerial      string `gorm:"column:vod_serial;type:varchar(64)" json:"vodSerial"`
	VodTv          string `gorm:"column:vod_tv;type:varchar(128)" json:"vodTv"`
	VodWeekday     string `gorm:"column:vod_weekday;type:varchar(128)" json:"vodWeekday"`

	// ---- 属性 ----
	VodArea     string `gorm:"column:vod_area;type:varchar(128)" json:"vodArea"` // 原始文本保留用于展示；筛选走 video_areas
	VodLang     string `gorm:"column:vod_lang;type:varchar(64)" json:"vodLang"`
	VodYear     string `gorm:"column:vod_year;type:varchar(32);index" json:"vodYear"` // 站点原始年份文本；筛选与下拉都按它等值聚合
	VodVersion  string `gorm:"column:vod_version;type:varchar(128)" json:"vodVersion"`
	VodState    string `gorm:"column:vod_state;type:varchar(128)" json:"vodState"`
	VodAuthor   string `gorm:"column:vod_author;type:varchar(128)" json:"vodAuthor"`
	VodJumpurl  string `gorm:"column:vod_jumpurl;type:varchar(512)" json:"vodJumpurl"`
	VodDuration string `gorm:"column:vod_duration;type:varchar(64)" json:"vodDuration"`

	// ---- 模板与标志 ----
	VodTplPlay string `gorm:"column:vod_tpl_play;type:varchar(128)" json:"vodTplPlay"`
	VodTplDown string `gorm:"column:vod_tpl_down;type:varchar(128)" json:"vodTplDown"`
	VodIsend   string `gorm:"column:vod_isend;type:varchar(32)" json:"vodIsend"`

	// ---- 积分与统计 ----
	VodHits      string `gorm:"column:vod_hits;type:varchar(64)" json:"vodHits"`
	VodHitsDay   string `gorm:"column:vod_hits_day;type:varchar(64)" json:"vodHitsDay"`
	VodHitsWeek  string `gorm:"column:vod_hits_week;type:varchar(64)" json:"vodHitsWeek"`
	VodHitsMonth string `gorm:"column:vod_hits_month;type:varchar(64)" json:"vodHitsMonth"`
	VodUp        string `gorm:"column:vod_up;type:varchar(64)" json:"vodUp"`
	VodDown      string `gorm:"column:vod_down;type:varchar(64)" json:"vodDown"`
	VodScore     string `gorm:"column:vod_score;type:varchar(64)" json:"vodScore"`
	VodScoreAll  string `gorm:"column:vod_score_all;type:varchar(64)" json:"vodScoreAll"`
	VodScoreNum  string `gorm:"column:vod_score_num;type:varchar(64)" json:"vodScoreNum"`

	// ---- 时间 ----
	VodTime     string `gorm:"column:vod_time;type:varchar(32);index;index:idx_videos_source_time,priority:2" json:"vodTime"` // 站点原始更新时间，归一化后可字典序比较
	VodTimeAdd  string `gorm:"column:vod_time_add;type:varchar(32)" json:"vodTimeAdd"`
	VodTimeMake string `gorm:"column:vod_time_make;type:varchar(32)" json:"vodTimeMake"`

	// ---- 来源与关联 ----
	VodDoubanID    string `gorm:"column:vod_douban_id;type:varchar(64)" json:"vodDoubanId"`
	VodDoubanScore string `gorm:"column:vod_douban_score;type:varchar(64)" json:"vodDoubanScore"`

	// ---- 播放 ----
	VodPlayFrom   string `gorm:"column:vod_play_from;type:varchar(255)" json:"vodPlayFrom"`
	VodPlayServer string `gorm:"column:vod_play_server;type:varchar(255)" json:"vodPlayServer"`
	VodPlayNote   string `gorm:"column:vod_play_note;type:varchar(255)" json:"vodPlayNote"`
	VodPlayURL    string `gorm:"column:vod_play_url;type:mediumtext" json:"vodPlayUrl"`
	VodPlayList   string `gorm:"column:vod_play_list;type:mediumtext" json:"vodPlayList"`

	// ---- 下载 ----
	VodDownFrom   string `gorm:"column:vod_down_from;type:varchar(255)" json:"vodDownFrom"`
	VodDownServer string `gorm:"column:vod_down_server;type:varchar(255)" json:"vodDownServer"`
	VodDownNote   string `gorm:"column:vod_down_note;type:varchar(255)" json:"vodDownNote"`
	VodDownURL    string `gorm:"column:vod_down_url;type:mediumtext" json:"vodDownUrl"`
	VodDownList   string `gorm:"column:vod_down_list;type:mediumtext" json:"vodDownList"`

	// ---- 分集剧情 ----
	VodPlot       string `gorm:"column:vod_plot;type:varchar(32)" json:"vodPlot"`
	VodPlotName   string `gorm:"column:vod_plot_name;type:mediumtext" json:"vodPlotName"`
	VodPlotDetail string `gorm:"column:vod_plot_detail;type:mediumtext" json:"vodPlotDetail"`

	// ---- 采集元数据 ----
	Hash  string `gorm:"column:hash;type:varchar(64)" json:"-"`               // 全字段内容指纹
	Extra string `gorm:"column:extra;type:mediumtext" json:"extra,omitempty"` // 未映射字段（原始 JSON 值）

	FirstSeenAt time.Time `json:"firstSeenAt"`
	LastSeenAt  time.Time `json:"lastSeenAt"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
