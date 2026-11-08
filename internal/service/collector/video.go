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
	// 优先用 vod_play_from / vod_play_url 等解析；解析为空且接口自带合法的
	// vod_play_list 时，保留接口原始值，避免丢数据。
	if groups := ParsePlayGroups(v.VodPlayFrom, v.VodPlayURL, v.VodPlayServer, v.VodPlayNote); len(groups) > 0 {
		v.VodPlayList = MarshalPlayGroups(groups)
	} else if raw, ok := item.RawFields["vod_play_list"]; ok {
		var probe []json.RawMessage
		if json.Unmarshal(raw, &probe) == nil {
			v.VodPlayList = string(raw)
		}
	}

	v.VodTime = utils.NormalizeTime(v.VodTime) // 游标需要可字典序比较的时间
	v.Hash = utils.Fingerprint(item.Fields)
	return v
}
