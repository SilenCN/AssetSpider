package service

import (
	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

// listVideosForTest 是服务层测试用的适配函数。
// 生产列表只返回 VideoCard 卡片投影，完整记录由 GetVideo 按 id 返回；这里按卡片 ID
// 逐条补全成 model.Video，让「命中 N 条」与全字段断言继续有效。测试数据都在一页内，
// total 用本页条数代替，不做全表 COUNT。
func listVideosForTest(repo repository.VideoRepository, q repository.VideoQuery) ([]model.Video, int64, error) {
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
