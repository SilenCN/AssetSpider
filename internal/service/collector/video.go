package collector

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/utils"
)

// ToVideo 把接口记录全量转换为入库模型并计算内容指纹。
//
// 映射完全由 model.VodFields 驱动，新增字段只需改清单与实体，无需改动这里；
// 清单之外的字段（站点自定义字段、嵌套对象等）按原始 JSON 值写入 Extra，
// 因此接口返回的任何字段都不会丢失（标准字段进列、其余进 Extra，不再冗余存整条 raw）。
//
// 纯数据转换，不访问数据库；编排与入库由上层 service 负责。
// 时间与指纹计算复用横切工具层 utils。
func ToVideo(sourceID uint, item VideoItem) model.Video { return toVideo(sourceID, item) }

func toVideo(sourceID uint, item VideoItem) model.Video {
	v := model.Video{SourceID: sourceID}
	rv := reflect.ValueOf(&v).Elem()
	for _, f := range model.VodFields {
		val, ok := item.Fields[f.Key]
		if !ok {
			continue
		}
		fv := rv.FieldByName(f.Go)
		if fv.IsValid() && fv.CanSet() && fv.Kind() == reflect.String {
			fv.SetString(val)
		} else {
			// 清单与实体脱节属于代码错误，宁可显式失败也不静默丢字段。
			panic(fmt.Sprintf("Video 缺少字段 %s（接口键 %s）", f.Go, f.Key))
		}
	}

	extra := make(map[string]json.RawMessage)
	for k, raw := range item.RawFields {
		if !model.IsVodField(k) {
			extra[k] = raw
		}
	}
	if len(extra) > 0 {
		if b, err := json.Marshal(extra); err == nil {
			v.Extra = string(b)
		}
	}

	// 按苹果CMS 字段解析播放组，回填 vod_play_list（结构化 JSON）。
	// 兼容四种来源，按优先级依次尝试：
	//   1. vod_play_url 本身就是结构化 JSON 数组（部分站点 / 二开模板）；
	//   2. 苹果CMS 的 vod_play_from / vod_play_url $$$ 串（接口明确给了就优先用它）；
	//   3. 接口自带的 vod_play_list 结构化数组（无标准播放串时兜底）；
	//   4. 接口自带的 vod_play_list 原始合法数组值。
	// 结构化来源成功时同时规范化 from / url 两列，保证三列语义一致。
	if groups := ParseStructuredPlayGroups(v.VodPlayURL); len(groups) > 0 {
		applyStructuredGroups(&v, groups)
	} else if groups := ParsePlayGroups(v.VodPlayFrom, v.VodPlayURL, v.VodPlayServer, v.VodPlayNote); len(groups) > 0 {
		v.VodPlayList = MarshalPlayGroups(groups)
	} else if groups := ParseStructuredPlayGroups(v.VodPlayList); len(groups) > 0 {
		applyStructuredGroups(&v, groups)
	} else if raw, ok := item.RawFields["vod_play_list"]; ok {
		var probe []json.RawMessage
		if json.Unmarshal(raw, &probe) == nil {
			v.VodPlayList = string(raw)
		}
	}

	v.VodTime = utils.NormalizeTime(v.VodTime) // 游标需要可字典序比较的时间
	v.Hash = utils.Fingerprint(item.Fields)

	// 少数海洋CMS 系站点不返回 vod_id，旧逻辑会整条跳过，导致「采集了却没影片」。
	// 只要记录有可识别内容（名称或播放地址），就用内容指纹生成稳定伪 ID 兜底，
	// 同一条影片重复采集仍会命中同一 ID，去重语义不变。
	if v.VodID == "" && (v.VodName != "" || v.VodPlayURL != "" || v.VodPlayList != "") {
		v.VodID = "auto-" + utils.Fingerprint(map[string]string{
			"name": v.VodName, "time": v.VodTime, "hash": v.Hash,
		})[:16]
	}
	return v
}

// applyStructuredGroups 把结构化播放组同步写回三列，避免结构化数组只落在
// vod_play_list、而 vod_play_url 里仍是一整段 JSON 文本导致无法按串查询。
func applyStructuredGroups(v *model.Video, groups []model.PlayGroup) {
	from, playURL := FlattenPlayGroups(groups)
	v.VodPlayFrom, v.VodPlayURL = from, playURL
	v.VodPlayList = MarshalPlayGroups(groups)
}
