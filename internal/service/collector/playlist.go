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

// ParseStructuredPlayGroups 解析「JSON 数组」形式的播放地址。
//
// 部分资源站 / 二次开发模板不返回苹果CMS 的 $$$ 串，而是直接给结构化数组，
// 例如：[{"from":"m3u8","episodes":[{"name":"第1集","url":"https://x/1.m3u8"}]}]
// 或 [{"from":"m3u8","url":"第1集$https://x/1.m3u8#第2集$https://x/2.m3u8"}]。
// 统一转成标准 PlayGroup，避免整段 JSON 被当成一条播放地址入库。
func ParseStructuredPlayGroups(raw string) []model.PlayGroup {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw[0] != '[' {
		return nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		return nil
	}
	groups := make([]model.PlayGroup, 0, len(arr))
	for i, el := range arr {
		var obj map[string]json.RawMessage
		if json.Unmarshal(el, &obj) != nil {
			// 纯字符串元素：整段作为一个播放组的地址串。
			var s string
			if json.Unmarshal(el, &s) == nil && strings.TrimSpace(s) != "" {
				groups = append(groups, model.PlayGroup{
					From: defaultGroupName("", i, len(arr)), Episodes: parseEpisodes(s),
				})
			}
			continue
		}
		name := firstString(obj, "from", "name", "play_from", "flag")
		eps := parseStructuredEpisodes(obj)
		if len(eps) > 0 {
			groups = append(groups, model.PlayGroup{
				From: defaultGroupName(name, i, len(arr)), Episodes: eps,
			})
		}
	}
	if len(groups) == 0 {
		return nil
	}
	return groups
}

// parseStructuredEpisodes 从结构化播放组里提取分集：优先 episodes/list，
// 其次把 url 字段按苹果CMS 的 #/$ 规则解析。
func parseStructuredEpisodes(obj map[string]json.RawMessage) []model.PlayEpisode {
	for _, key := range []string{"episodes", "list", "urls", "vod_play_url"} {
		raw, ok := obj[key]
		if !ok {
			continue
		}
		var list []json.RawMessage
		if json.Unmarshal(raw, &list) != nil {
			continue
		}
		out := make([]model.PlayEpisode, 0, len(list))
		for _, el := range list {
			var epObj map[string]json.RawMessage
			if json.Unmarshal(el, &epObj) != nil {
				var s string
				if json.Unmarshal(el, &s) == nil && strings.TrimSpace(s) != "" {
					out = append(out, model.PlayEpisode{Url: strings.TrimSpace(s)})
				}
				continue
			}
			u := firstString(epObj, "url", "play_url", "link", "src")
			if u == "" {
				continue
			}
			out = append(out, model.PlayEpisode{
				Name: firstString(epObj, "name", "title", "episode"), Url: u,
			})
		}
		if len(out) > 0 {
			return out
		}
	}
	if u := firstString(obj, "url", "play_url", "vod_play_url"); u != "" {
		return parseEpisodes(u)
	}
	return nil
}

// firstString 按候选键顺序取第一个非空字符串值。
func firstString(obj map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		raw, ok := obj[k]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// defaultGroupName 给缺失线路名的结构化播放组一个可读名称。
func defaultGroupName(name string, i, total int) string {
	if name != "" {
		return name
	}
	if total == 1 {
		return "默认"
	}
	return "线路" + strconv.Itoa(i+1)
}

// FlattenPlayGroups 把结构化播放组还原成苹果CMS 的 $$$ 串，
// 让 vod_play_from / vod_play_url 两列与 vod_play_list 保持一致，
// 便于 SQL 查询与详情页展示（结构化数组原本只落在一列里）。
func FlattenPlayGroups(groups []model.PlayGroup) (from, playURL string) {
	froms := make([]string, 0, len(groups))
	urls := make([]string, 0, len(groups))
	for _, g := range groups {
		froms = append(froms, g.From)
		eps := make([]string, 0, len(g.Episodes))
		for _, e := range g.Episodes {
			if e.Name != "" {
				eps = append(eps, e.Name+"$"+e.Url)
			} else {
				eps = append(eps, e.Url)
			}
		}
		urls = append(urls, strings.Join(eps, "#"))
	}
	return strings.Join(froms, "$$$"), strings.Join(urls, "$$$")
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
