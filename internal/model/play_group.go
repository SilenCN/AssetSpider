package model

// PlayEpisode 是一条播放地址（某一集 / 某一资源）。
// Name 是苹果CMS 中的集名（如「第1集」「HD」），Url 为真实播放地址。
type PlayEpisode struct {
	Name string `json:"name"`
	Url  string `json:"url"`
}

// PlayGroup 对应苹果CMS 的一个播放组（播放源 / 线路）。
//
// 苹果CMS 把多个播放源与对应地址用特定分隔符拼装：
//   - vod_play_from：播放组名称，用 $$$ 分隔（如 "gugukk$$$yjm3u8"）；
//   - vod_play_url：每组地址，组间用 $$$ 分隔，组内用 # 分隔集，
//     每集用 $ 分隔「集名$地址」（如 "第1集$http://a/1.m3u8#第2集$http://a/2.m3u8"）；
//   - vod_play_server / vod_play_note：可选的服务器组与备注，同样用 $$$ 与播放组对齐。
//
// 采集时由 collector.ParsePlayGroups 解析后填充到 Video.VodPlayList（JSON 文本），
// 既保留苹果CMS 的原始字段，又提供可直接渲染的结构化数据。
type PlayGroup struct {
	From     string        `json:"from"`     // 播放组 / 播放器名称（线路名）
	Server   string        `json:"server"`   // 服务器组（缺省为 "no"）
	Note     string        `json:"note"`     // 播放备注
	Episodes []PlayEpisode `json:"episodes"` // 该播放组下的分集地址
}
