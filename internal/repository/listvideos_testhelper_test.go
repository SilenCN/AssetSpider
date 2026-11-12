package repository

import "github.com/SilenCN/AssetSpider/internal/model"

// listVideosForTest 是仓储层测试用的适配函数。
//
// 生产列表接口只返回 VideoCard 卡片投影，完整记录由 GetVideo 按 id 单独返回
// （列表列裁剪后，播放组 / 简介 / 扩展字段等不再随列表下发）。大量旧断言写的是
// 「命中 N 条」并直接检查全字段，这里按卡片 ID 逐条补全成 model.Video，让这些
// 断言继续表达「查到了几条 / 字段是否写对」。测试数据都在一页以内，因此 total
// 用本页条数代替，不做全表 COUNT；跨页场景请直接调用 ListVideos 断言卡片长度。
func listVideosForTest(repo VideoRepository, q VideoQuery) ([]model.Video, int64, error) {
	cards, err := repo.ListVideos(q)
	if err != nil {
		return nil, 0, err
	}
	items := make([]model.Video, 0, len(cards))
	for _, c := range cards {
		v, err := repo.GetVideo(c.ID)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, *v)
	}
	return items, int64(len(items)), nil
}
