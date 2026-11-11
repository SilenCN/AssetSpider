package model

import (
	"reflect"
	"strings"
)

// VodField 描述一个苹果CMS / 海洋CMS 采集接口字段与本地 Video 实体的映射。
//
// 字段清单依据 MacCMS V10 官方「视频字段」表（application/install/sql/install.sql），
// 并补充资源站接口常见的 type_name / type / vod_play_list / vod_down_list。
// 这份清单是采集解析、入库更新、API 元数据与前端展示的唯一数据源，放在 model 包
// 作为横切领域定义，避免 service/collector 与 repository 之间产生循环依赖。
type VodField struct {
	Key    string // 接口 JSON 键，如 vod_name
	Go     string // Video 结构体字段名
	Column string // 数据库列名
	Desc   string // 中文含义
	Group  string // 分组 key
	Long   bool   // 是否长文本（简介、播放地址、解析后的列表等）
}

// VodGroupLabels 是分组的中文名，顺序即前端详情展示顺序。
var VodGroupLabels = []struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}{
	{"basic", "基础信息"},
	{"image", "图片"},
	{"cast", "演职人员"},
	{"content", "简介与备注"},
	{"release", "上映与集数"},
	{"attribute", "属性"},
	{"flags", "模板与标志"},
	{"stats", "积分与统计"},
	{"time", "时间"},
	{"relation", "来源与关联"},
	{"play", "播放"},
	{"download", "下载"},
	{"plot", "分集剧情"},
}

// VodFields 覆盖接口返回的全部标准字段。
// 新增字段只需在此追加，并在 Video 中补上同名 Go 字段即可全链路生效。
var VodFields = []VodField{
	// 基础信息
	{"vod_id", "VodID", "vod_id", "视频 ID", "basic", false},
	{"type_id", "TypeID", "type_id", "分类 ID", "basic", false},
	{"type_id_1", "TypeID1", "type_id_1", "一级分类 ID", "basic", false},
	{"group_id", "GroupID", "group_id", "用户组 ID", "basic", false},
	{"vod_name", "VodName", "vod_name", "视频名称", "basic", false},
	{"vod_sub", "VodSub", "vod_sub", "副标题", "basic", false},
	{"vod_en", "VodEn", "vod_en", "别名 / 英文名", "basic", false},
	{"vod_color", "VodColor", "vod_color", "颜色", "basic", false},
	{"vod_tag", "VodTag", "vod_tag", "标签", "basic", false},
	{"vod_class", "VodClass", "vod_class", "扩展分类", "basic", false},
	{"type_name", "TypeName", "type_name", "分类名称", "basic", false},
	{"type", "VodType", "type", "分类对象 / 名称", "basic", true},

	// 图片
	{"vod_pic", "VodPic", "vod_pic", "图片", "image", false},
	{"vod_pic_thumb", "VodPicThumb", "vod_pic_thumb", "缩略图", "image", false},
	{"vod_pic_slide", "VodPicSlide", "vod_pic_slide", "幻灯图", "image", false},
	{"vod_pic_original", "VodPicOriginal", "vod_pic_original", "原图", "image", false},
	{"vod_pic_screenshot", "VodPicScreenshot", "vod_pic_screenshot", "截图（多个用 # 连接）", "image", true},

	// 演职人员
	{"vod_actor", "VodActor", "vod_actor", "主演", "cast", false},
	{"vod_director", "VodDirector", "vod_director", "导演", "cast", false},
	{"vod_writer", "VodWriter", "vod_writer", "编剧", "cast", false},
	{"vod_behind", "VodBehind", "vod_behind", "幕后", "cast", false},

	// 简介与备注
	{"vod_blurb", "VodBlurb", "vod_blurb", "简介", "content", true},
	{"vod_remarks", "VodRemarks", "vod_remarks", "备注", "content", false},
	{"vod_content", "VodContent", "vod_content", "详细介绍", "content", true},

	// 上映与集数
	{"vod_pubdate", "VodPubdate", "vod_pubdate", "上映日期", "release", false},
	{"vod_publish_time", "VodPublishTime", "vod_publish_time", "定时上架时间戳", "release", false},
	{"vod_total", "VodTotal", "vod_total", "总集数", "release", false},
	{"vod_serial", "VodSerial", "vod_serial", "连载数", "release", false},
	{"vod_tv", "VodTv", "vod_tv", "上映电视台", "release", false},
	{"vod_weekday", "VodWeekday", "vod_weekday", "节目周期", "release", false},

	// 属性
	{"vod_area", "VodArea", "vod_area", "地区", "attribute", false},
	{"vod_lang", "VodLang", "vod_lang", "语言", "attribute", false},
	{"vod_year", "VodYear", "vod_year", "年代", "attribute", false},
	{"vod_version", "VodVersion", "vod_version", "版本", "attribute", false},
	{"vod_state", "VodState", "vod_state", "资源类别", "attribute", false},
	{"vod_author", "VodAuthor", "vod_author", "编辑人员", "attribute", false},
	{"vod_jumpurl", "VodJumpurl", "vod_jumpurl", "跳转地址", "attribute", false},
	{"vod_duration", "VodDuration", "vod_duration", "时长", "attribute", false},

	// 模板与标志
	{"vod_tpl_play", "VodTplPlay", "vod_tpl_play", "播放页模板", "flags", false},
	{"vod_tpl_down", "VodTplDown", "vod_tpl_down", "下载页模板", "flags", false},
	{"vod_isend", "VodIsend", "vod_isend", "是否完结", "flags", false},

	// 积分与统计
	{"vod_hits", "VodHits", "vod_hits", "总点击量", "stats", false},
	{"vod_hits_day", "VodHitsDay", "vod_hits_day", "日点击量", "stats", false},
	{"vod_hits_week", "VodHitsWeek", "vod_hits_week", "周点击量", "stats", false},
	{"vod_hits_month", "VodHitsMonth", "vod_hits_month", "月点击量", "stats", false},
	{"vod_up", "VodUp", "vod_up", "顶 / 赞次数", "stats", false},
	{"vod_down", "VodDown", "vod_down", "踩次数", "stats", false},
	{"vod_score", "VodScore", "vod_score", "平均分", "stats", false},
	{"vod_score_all", "VodScoreAll", "vod_score_all", "总评分", "stats", false},
	{"vod_score_num", "VodScoreNum", "vod_score_num", "评分次数", "stats", false},

	// 时间
	{"vod_time", "VodTime", "vod_time", "更新时间", "time", false},
	{"vod_time_add", "VodTimeAdd", "vod_time_add", "添加时间", "time", false},
	{"vod_time_make", "VodTimeMake", "vod_time_make", "生成时间", "time", false},

	// 来源与关联
	{"vod_douban_id", "VodDoubanID", "vod_douban_id", "豆瓣 ID", "relation", false},
	{"vod_douban_score", "VodDoubanScore", "vod_douban_score", "豆瓣评分", "relation", false},

	// 播放
	{"vod_play_from", "VodPlayFrom", "vod_play_from", "播放组", "play", false},
	{"vod_play_server", "VodPlayServer", "vod_play_server", "播放服务器组", "play", false},
	{"vod_play_note", "VodPlayNote", "vod_play_note", "播放备注", "play", false},
	{"vod_play_url", "VodPlayURL", "vod_play_url", "播放地址", "play", true},
	{"vod_play_list", "VodPlayList", "vod_play_list", "播放列表（解析后数组）", "play", true},

	// 下载
	{"vod_down_from", "VodDownFrom", "vod_down_from", "下载组", "download", false},
	{"vod_down_server", "VodDownServer", "vod_down_server", "下载服务器组", "download", false},
	{"vod_down_note", "VodDownNote", "vod_down_note", "下载备注", "download", false},
	{"vod_down_url", "VodDownURL", "vod_down_url", "下载地址", "download", true},
	{"vod_down_list", "VodDownList", "vod_down_list", "下载列表（解析后数组）", "download", true},

	// 分集剧情
	{"vod_plot", "VodPlot", "vod_plot", "是否含分集剧情", "plot", false},
	{"vod_plot_name", "VodPlotName", "vod_plot_name", "分集剧情名称", "plot", true},
	{"vod_plot_detail", "VodPlotDetail", "vod_plot_detail", "分集剧情详情", "plot", true},
}

// vodFieldKeys 供采集器快速判断某字段是否已被显式映射。
var vodFieldKeys = func() map[string]struct{} {
	m := make(map[string]struct{}, len(VodFields))
	for _, f := range VodFields {
		m[f.Key] = struct{}{}
	}
	return m
}()

// IsVodField 报告 key 是否是已显式映射到 Video 列的标准字段。
func IsVodField(key string) bool {
	_, ok := vodFieldKeys[key]
	return ok
}

// VodFieldMeta 是供前端展示的字段元数据（含中文含义与分组）。
type VodFieldMeta struct {
	Key   string `json:"key"`   // 接口原始键，如 vod_name
	Json  string `json:"json"`  // 前端取值键，如 vodName
	Desc  string `json:"desc"`  // 中文含义
	Group string `json:"group"` // 分组 key
	Label string `json:"label"` // 分组中文名
	Long  bool   `json:"long"`  // 是否长文本
}

// VodFieldMetas 由 VodFields + Video 的 json tag 生成字段元数据。
// Json 通过反射读取，保证前端取值键与 API 序列化结果始终一致。
func VodFieldMetas() []VodFieldMeta {
	t := reflect.TypeOf(Video{})
	labels := make(map[string]string, len(VodGroupLabels))
	for _, g := range VodGroupLabels {
		labels[g.Key] = g.Label
	}
	out := make([]VodFieldMeta, 0, len(VodFields))
	for _, f := range VodFields {
		jsonKey := f.Key
		if sf, ok := t.FieldByName(f.Go); ok {
			if tag := sf.Tag.Get("json"); tag != "" {
				jsonKey = strings.Split(tag, ",")[0]
			}
		}
		out = append(out, VodFieldMeta{
			Key: f.Key, Json: jsonKey, Desc: f.Desc,
			Group: f.Group, Label: labels[f.Group], Long: f.Long,
		})
	}
	return out
}
