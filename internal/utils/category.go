package utils

import "strings"

// CategoryKey 把站点返回的分类信息归一化成稳定键。
//
// 优先用 type_id（同一站点内唯一）；站点未给 type_id 或给了 "0" 时回退到分类名，
// 避免把不同分类错误合并。返回空串表示这条视频没有可用分类，不建分类也不做关联。
// 该键同时被 repository 与 service 使用，必须保持两侧一致。
func CategoryKey(typeID, typeName string) string {
	id := strings.TrimSpace(typeID)
	if id != "" && id != "0" {
		return "id:" + id
	}
	name := strings.TrimSpace(typeName)
	if name != "" {
		return "name:" + name
	}
	return ""
}
