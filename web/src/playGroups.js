// 播放线路（播放组）的展示规则。
//
// 苹果CMS 的 vod_play_from 是站点自己给播放组起的名字（如 ffm3u8 / lzm3u8 / 量子资源），
// 播放页直接沿用原名，比「线路 1 / 线路 2」更能对应站点的实际线路；只有站点没给名字时
// 才退回编号。名称过长做截断，完整信息放在悬浮提示里，避免撑破选项卡行。

// 名称最多展示的字符数，超出部分用省略号。
export const LABEL_MAX = 18

// groupLabel 返回线路选项卡上的主文案。
export function groupLabel(group, index) {
  const from = String(group?.from ?? '').trim()
  if (!from) return `线路 ${index + 1}`
  return from.length > LABEL_MAX ? `${from.slice(0, LABEL_MAX)}…` : from
}

// groupHint 返回悬浮提示：完整播放组名 + 备注 + 服务器组（"no" 是苹果CMS 的缺省值，无意义）。
export function groupHint(group) {
  const from = String(group?.from ?? '').trim()
  const server = String(group?.server ?? '').trim()
  const note = String(group?.note ?? '').trim()
  return [from, note, server && server !== 'no' ? server : ''].filter(Boolean).join(' · ')
}
