package service

import (
	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

// listVideosForTest 是服务层测试用的适配函数。
// 影片列表接口不再返回总数，测试断言沿用「命中 N 条」的写法，这里以本页条数代替。
// 测试数据都在一页以内，两者等价。
func listVideosForTest(repo repository.VideoRepository, q repository.VideoQuery) ([]model.Video, int64, error) {
	items, err := repo.ListVideos(q)
	return items, int64(len(items)), err
}
