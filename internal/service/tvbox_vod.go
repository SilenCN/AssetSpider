package service

import (
	"strings"

	"github.com/SilenCN/AssetSpider/internal/model"
)

// VodItem 是苹果CMS V10 provide/vod 接口 list 数组里的单个影片对象。
//
// 字段名沿用苹果CMS 原始命名（供 TVBox / 影视仓这类 type=1 客户端直接消费），
// 值来自采集时原样入库的 vod_* 字段——播放串 vod_play_url / vod_play_from 等
// 保持站点原始格式（$$$ 分组、# 分集、$ 分集名与地址），TVBox 自己解析。
// 只输出客户端真正会用到的字段，不把 90+ 个后台字段全量透出。
type VodItem struct {
	VodID       string `json:"vod_id"`
	TypeID      string `json:"type_id"`
	TypeID1     string `json:"type_id_1,omitempty"`
	TypeName    string `json:"type_name,omitempty"`
	VodName     string `json:"vod_name"`
	VodSub      string `json:"vod_sub,omitempty"`
	VodEn       string `json:"vod_en,omitempty"`
	VodPic      string `json:"vod_pic,omitempty"`
	VodRemarks  string `json:"vod_remarks,omitempty"`
	VodYear     string `json:"vod_year,omitempty"`
	VodArea     string `json:"vod_area,omitempty"`
	VodLang     string `json:"vod_lang,omitempty"`
	VodActor    string `json:"vod_actor,omitempty"`
	VodDirector string `json:"vod_director,omitempty"`
	VodWriter   string `json:"vod_writer,omitempty"`
	VodContent  string `json:"vod_content,omitempty"`
	VodTag      string `json:"vod_tag,omitempty"`
	VodClass    string `json:"vod_class,omitempty"`
	VodScore    string `json:"vod_score,omitempty"`
	VodTime     string `json:"vod_time,omitempty"`
	VodTotal    string `json:"vod_total,omitempty"`
	VodSerial   string `json:"vod_serial,omitempty"`
	// 播放四件套：原样透传，TVBox 按苹果CMS 规则解析。
	VodPlayFrom   string `json:"vod_play_from,omitempty"`
	VodPlayServer string `json:"vod_play_server,omitempty"`
	VodPlayNote   string `json:"vod_play_note,omitempty"`
	VodPlayURL    string `json:"vod_play_url,omitempty"`
	// 下载四件套同理，部分客户端会读 vod_down_url。
	VodDownFrom string `json:"vod_down_from,omitempty"`
	VodDownURL  string `json:"vod_down_url,omitempty"`
}

// vodItemOf 把入库影片映射成苹果CMS list 项。
// typeName 由调用方按 categoryID 预查好传入（避免逐条查分类）。
func vodItemOf(v model.Video, typeName string) VodItem {
	return VodItem{
		VodID:         v.VodID,
		TypeID:        v.TypeID,
		TypeID1:       v.TypeID1,
		TypeName:      firstNonEmpty(typeName, v.TypeName),
		VodName:       v.VodName,
		VodSub:        v.VodSub,
		VodEn:         v.VodEn,
		VodPic:        v.VodPic,
		VodRemarks:    v.VodRemarks,
		VodYear:       v.VodYear,
		VodArea:       v.VodArea,
		VodLang:       v.VodLang,
		VodActor:      v.VodActor,
		VodDirector:   v.VodDirector,
		VodWriter:     v.VodWriter,
		VodContent:    v.VodContent,
		VodTag:        v.VodTag,
		VodClass:      v.VodClass,
		VodScore:      v.VodScore,
		VodTime:       v.VodTime,
		VodTotal:      v.VodTotal,
		VodSerial:     v.VodSerial,
		VodPlayFrom:   v.VodPlayFrom,
		VodPlayServer: v.VodPlayServer,
		VodPlayNote:   v.VodPlayNote,
		VodPlayURL:    v.VodPlayURL,
		VodDownFrom:   v.VodDownFrom,
		VodDownURL:    v.VodDownURL,
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// splitIDs 把苹果CMS ids 参数（逗号分隔）拆成去重的字符串切片，限长防止滥用。
func splitIDs(raw string, max int) []string {
	seen := map[string]bool{}
	out := make([]string, 0)
	for _, part := range strings.Split(raw, ",") {
		id := strings.TrimSpace(part)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if len(out) >= max {
			break
		}
	}
	return out
}
