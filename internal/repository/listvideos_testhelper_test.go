package repository

import "github.com/SilenCN/AssetSpider/internal/model"

// listVideosForTest 是仓储层测试用的适配函数。
//
// 生产接口已不返回总数（避免 COUNT 全表扫描），但大量旧断言写的是「命中 N 条」。
// 这里把本页条数当作 total 返回，让这些断言继续表达「查到了几条」。测试用例的数据
// 都在一页以内，因此两者等价；跨页场景请直接调用 ListVideos 断言 items 长度。
func listVideosForTest(repo VideoRepository, q VideoQuery) ([]model.Video, int64, error) {
	items, err := repo.ListVideos(q)
	return items, int64(len(items)), err
}
