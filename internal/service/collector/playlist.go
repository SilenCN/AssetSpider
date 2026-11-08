package collector

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/SilenCN/AssetSpider/internal/model"
)

// ParsePlayGroups 把苹果CMS 的播放字段解析成结构化播放组。
//
// 苹果CMS V10 的格式约定（Provide 接口返回）：
//   - vod_play_from：播放组（线路）名称，用 $$$ 分隔，如 "gugukk$$$yjm3u8$$$ffm3u8"；
//   - vod_play_url：与播放组一一对应的地址串，组间同样用 $$$ 分隔，组内用 #
//     分隔不同集，每集用 $ 分隔「集名$地址」；
//   - vod_play_server / vod_play_note：可选的服务器组与备注，按 $$$ 与播放组对齐。
//
// 解析严格遵循上述分隔符：$$$ 切分播放组、# 切分集、$ 切分集名与地址。
// 个别站点会在地址里混入多余分隔符或缺失线路名，这里做了容错：
//   - 播放组数取 from / url 中较大者，避免地址被丢掉；
//   - 缺失线路名时单组标为「默认」、多组标为「线路N」；
//   - 每集缺 $ 时整段视为地址、集名为空。
//
// 解析失败或为空时返回 nil，调用方据此回退到接口原始 vod_play_list。
func ParsePlayGroups(from, playURL, server, note string) []model.PlayGroup {
	fromParts := strings.Split(from, "$$$")
	urlParts := strings.Split(playURL, "$$$")
	serverParts := strings.Split(server, "$$$")
	noteParts := strings.Split(note, "$$$")
	multiFrom := len(fromParts) > 1

	n := len(fromParts)
	if len(urlParts) > n {
		n = len(urlParts)
	}

	groups := make([]model.PlayGroup, 0, n)
	for i := 0; i < n; i++ {
		name := ""
		if i < len(fromParts) {
			name = strings.TrimSpace(fromParts[i])
		}
		if name == "" {
			if !multiFrom {
				name = "默认"
			} else {
				name = "线路" + strconv.Itoa(i+1)
			}
		}
		group := model.PlayGroup{
			From:     name,
			Server:   atPart(serverParts, i),
			Note:     atPart(noteParts, i),
			Episodes: parseEpisodes(atPart(urlParts, i)),
		}
		groups = append(groups, group)
	}

	// 仅当没有任何播放组或所有组都没有集时，才视为空（调用方回退原始值）。
	if len(groups) == 0 {
		return nil
	}
	hasEpisode := false
	for _, g := range groups {
		if len(g.Episodes) > 0 {
			hasEpisode = true
			break
		}
	}
	if !hasEpisode {
		return nil
	}
	return groups
}

// atPart 安全取第 i 段，越界返回空串（用于 server / note / url 与 from 对齐）。
func atPart(parts []string, i int) string {
	if i < 0 || i >= len(parts) {
		return ""
	}
	return strings.TrimSpace(parts[i])
}

// parseEpisodes 解析单个播放组内的分集地址。
// 组内用 # 分隔集，每集用 $ 分隔「集名$地址」；缺 $ 时整段作地址。
func parseEpisodes(group string) []model.PlayEpisode {
	group = strings.TrimSpace(group)
	if group == "" {
		return nil
	}
	raw := strings.Split(group, "#")
	out := make([]model.PlayEpisode, 0, len(raw))
	for _, ep := range raw {
		ep = strings.TrimSpace(ep)
		if ep == "" {
			continue
		}
		name, url := "", ep
		if idx := strings.Index(ep, "$"); idx >= 0 {
			name, url = strings.TrimSpace(ep[:idx]), strings.TrimSpace(ep[idx+1:])
		}
		out = append(out, model.PlayEpisode{Name: name, Url: url})
	}
	return out
}

// MarshalPlayGroups 把解析后的播放组序列化为存储用的 JSON 文本。
func MarshalPlayGroups(groups []model.PlayGroup) string {
	if len(groups) == 0 {
		return ""
	}
	b, err := json.Marshal(groups)
	if err != nil {
		return ""
	}
	return string(b)
}
