package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
	"github.com/SilenCN/AssetSpider/internal/service/collector"
	"github.com/SilenCN/AssetSpider/internal/utils"
)

// RunResult 是一次采集的结果摘要，也是 controller 直接序列化的 DTO。
type RunResult struct {
	JobID      uint   `json:"jobId"`
	SourceID   uint   `json:"sourceId"`
	SourceName string `json:"sourceName"`
	Mode       string `json:"mode"`
	Status     string `json:"status"`
	Fetched    int    `json:"fetched"`
	New        int    `json:"new"`
	Updated    int    `json:"updated"`
	Unchanged  int    `json:"unchanged"`
	Pages      int    `json:"pages"`
	Total      int    `json:"total"`
	// NoPlayURL 是本次采集中「接口没有返回任何播放地址」的记录数。
	// 只用于诊断：数值偏高通常说明接口走到了精简模式（ac=list / ExtraParams
	// 覆盖了 ac），或字段写法不是标准苹果CMS 键名，不代表入库失败。
	NoPlayURL int `json:"noPlayUrl"`
	// AutoAC 非空表示本次自动把采集动作切换成了该值（如 detail），
	// 用于在第一页完全没有播放地址时自动补齐播放数据。
	AutoAC     string    `json:"autoAc"`
	Cursor     string    `json:"cursor"`
	ResumePage int       `json:"resumePage"`
	Message    string    `json:"message"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
}

// hasExplicitAC 判断站点地址或附加参数里是否显式指定了 ac。
// 用户显式配置时不自动切换动作，尊重站点实际支持的接口形态。
func hasExplicitAC(src model.Source) bool {
	if u, err := url.Parse(strings.TrimSpace(src.APIBase)); err == nil && u.Query().Get("ac") != "" {
		return true
	}
	q, err := url.ParseQuery(strings.TrimPrefix(strings.TrimSpace(src.ExtraParams), "?"))
	return err == nil && q.Get("ac") != ""
}

// withAC 返回一个把 ac 强制设为指定动作的站点副本，保留其它附加参数（t / wd 等）。
func withAC(src model.Source, ac string) model.Source {
	q, err := url.ParseQuery(strings.TrimPrefix(strings.TrimSpace(src.ExtraParams), "?"))
	if err != nil {
		q = url.Values{}
	}
	q.Set("ac", ac)
	src.ExtraParams = q.Encode()
	return src
}

// listHasPlayURL 判断这一页是否有任何记录带播放地址（字段别名已在解析时归一化）。
func listHasPlayURL(list []collector.VideoItem) bool {
	for _, it := range list {
		if strings.TrimSpace(it.Get("vod_play_url")) != "" ||
			strings.TrimSpace(it.Get("vod_play_list")) != "" {
			return true
		}
	}
	return false
}

// NormalizeMode 解析采集模式：空值默认为增量，非法值返回 ErrInvalid。
// controller 只负责取出原始字符串，业务规则留在 service 层。
func NormalizeMode(raw string) (string, error) {
	mode := strings.TrimSpace(raw)
	if mode == "" {
		return model.ModeIncremental, nil
	}
	if mode != model.ModeFull && mode != model.ModeIncremental {
		return "", invalid("mode 仅支持 full / incremental")
	}
	return mode, nil
}

// CrawlService 编排采集：分页拉取 → 分类自动归档 → 去重入库 → 回写状态与采集记录。
//
// HTTP 抓取与协议解析由 service 层内部的 collector 子包完成；
// 持久化按实体分别注入 SourceRepository / VideoRepository / CategoryRepository /
// JobRepository，绝不越级。
//
// 任务调度（并发上限、排队、暂停）见 task.go：同一站点同一时刻只允许一个任务，
// 所有任务共享一个可运行时调整上限的并发限流器。
type CrawlService struct {
	sources    repository.SourceRepository
	videos     repository.VideoRepository
	categories repository.CategoryRepository
	jobs       repository.JobRepository
	fetcher    *collector.Fetcher
	cfg        config.CollectorConfig

	mu       sync.Mutex
	tasks    map[uint]*crawlTask // jobID → 运行时任务
	bySource map[uint]uint       // sourceID → jobID（防止同站点并发采集）
	limiter  *concurrencyLimiter
	wg       sync.WaitGroup // 追踪异步采集 goroutine，供 Close 时优雅收尾
}

func NewCrawlService(
	sources repository.SourceRepository,
	videos repository.VideoRepository,
	categories repository.CategoryRepository,
	jobs repository.JobRepository,
	cfg config.CollectorConfig,
) *CrawlService {
	return &CrawlService{
		sources:    sources,
		videos:     videos,
		categories: categories,
		jobs:       jobs,
		fetcher:    collector.NewFetcher(cfg),
		cfg:        cfg,
		tasks:      make(map[uint]*crawlTask),
		bySource:   make(map[uint]uint),
		limiter:    newConcurrencyLimiter(cfg.MaxConcurrent),
	}
}

// RunOne 同步执行一次采集，调用方阻塞直到任务结束（成功/暂停/失败）。
// 非管理员只能采集自己可见的站点，越权时按「站点不存在」返回，避免泄露站点信息。
func (s *CrawlService) RunOne(ctx context.Context, id uint, mode string, p ...Principal) (*RunResult, error) {
	if !principalOf(p...).CanSee(id) {
		return nil, notFound("站点 %d 不存在或无权访问", id)
	}
	task, src, err := s.begin(id, mode, startOptions{})
	if err != nil {
		return nil, err
	}
	return s.runTask(ctx, task, src)
}

// RunAll 对所有启用站点发起同模式采集，并发量由全局并发上限决定，
// 超出的站点任务进入排队。返回值顺序与站点列表一致。
func (s *CrawlService) RunAll(ctx context.Context, mode string, p ...Principal) ([]RunResult, error) {
	if mode != model.ModeFull && mode != model.ModeIncremental {
		return nil, invalid("不支持的采集模式 %q（可选 full / incremental）", mode)
	}
	// 批量采集只覆盖当前主体可见的启用站点，避免越权触发别人的采集任务。
	srcs, err := s.sources.ListEnabledSources(principalOf(p...).Visible())
	if err != nil {
		return nil, err
	}
	results := make([]RunResult, len(srcs))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for i, src := range srcs {
		wg.Add(1)
		go func(i int, src model.Source) {
			defer wg.Done()
			res, runErr := s.RunOne(ctx, src.ID, mode)
			if res != nil {
				results[i] = *res
			} else {
				results[i] = RunResult{
					SourceID: src.ID, SourceName: src.Name, Mode: mode,
					Status: model.JobFailed, StartedAt: time.Now(), FinishedAt: time.Now(),
				}
			}
			if runErr != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("站点「%s」: %w", src.Name, runErr)
				}
				mu.Unlock()
				if results[i].Message == "" {
					results[i].Message = runErr.Error()
				}
			}
		}(i, src)
	}
	wg.Wait()
	return results, firstErr
}

// crawl 执行分页循环与入库；startPage 为续采起点（全量被暂停后可从该页继续）。
func (s *CrawlService) crawl(
	ctx context.Context,
	src *model.Source,
	mode string,
	startPage int,
	task *crawlTask,
	res *RunResult,
) error {
	hours := 0
	if mode == model.ModeIncremental {
		hours = src.IncrementalHours
		if hours <= 0 {
			hours = s.cfg.DefaultHours
		}
		// 用上次采集时间兜底：时间窗取"配置小时数"与"距上次采集的小时数"的较大者，
		// 避免站点时间不准或采集间隔过长导致漏数据。
		if src.LastSyncAt != nil {
			elapsed := int(time.Since(*src.LastSyncAt).Hours()) + 2
			if elapsed > hours {
				hours = elapsed
			}
			if hours > 24*30 {
				hours = 24 * 30
			}
		}
	}

	maxPages := s.cfg.MaxPages
	if src.MaxPages > 0 {
		maxPages = src.MaxPages
	}
	if maxPages <= 0 {
		maxPages = 1000
	}
	if startPage < 1 {
		startPage = 1
	}

	pageCount := 0
	maxCursor := src.LastCursor
	// 无论正常结束还是中途暂停，已处理到的游标都要带回（暂停也要保存进度）。
	defer func() { res.Cursor = maxCursor }()
	for page := startPage; page <= maxPages; page++ {
		// 页边界检查暂停：已完成页的数据已入库，续采页码指向本页。
		if task.paused() {
			res.ResumePage = page
			return ErrPaused
		}
		if err := ctx.Err(); err != nil {
			if task.paused() {
				res.ResumePage = page
				return ErrPaused
			}
			res.ResumePage = page
			return err
		}
		// 抓取前先把「当前页」报到任务视图，列表能看到逐页推进。
		task.setPage(page)
		list, err := s.fetcher.FetchList(ctx, *src, page, hours)
		if err != nil {
			// 暂停会取消在途请求，此处不能当失败处理。
			res.ResumePage = page // 失败页即续采页，手动/自动重试都从这里开始
			if task.paused() {
				return ErrPaused
			}
			return fmt.Errorf("第 %d 页采集失败: %w", page, err)
		}
		res.Pages++

		// 自动探测播放数据：苹果CMS 的 ac=videolist 在部分站点只返回精简列表，
		// 不含 vod_play_url；官方 ac=detail（不带 ids）则返回含播放地址的详情列表。
		// 仅在第一页整页没有播放地址、且站点未显式指定 ac 时尝试一次，
		// 只有探测结果确实带播放地址才切换，后续所有页沿用；失败保持原样，
		// 由任务消息给出排查提示，不影响既有采集行为。
		// 只在全新采集的第 1 页尝试：续采 / 重试 / 重新运行都不额外请求，
		// 避免改变断点续采的请求序列（也避免重复探测）。
		if page == 1 && res.Pages == 1 && len(list.List) > 0 && !hasExplicitAC(*src) &&
			!listHasPlayURL(list.List) {
			probeSrc := withAC(*src, "detail")
			if probeList, err := s.fetcher.FetchList(ctx, probeSrc, page, hours); err == nil &&
				len(probeList.List) > 0 && listHasPlayURL(probeList.List) {
				src, list = &probeSrc, probeList
				res.AutoAC = "detail"
			}
		}

		if page == startPage {
			res.Total = list.Total.Int()
			pageCount = list.PageCount.Int()
			if pageCount <= 0 && len(list.List) > 0 {
				// 站点未返回总页数时，按"拉到空页为止"处理。
				pageCount = maxPages
			}
		}
		if len(list.List) == 0 {
			break
		}

		videos := make([]model.Video, 0, len(list.List))
		for _, item := range list.List {
			v := collector.ToVideo(src.ID, item)
			if v.VodID == "" {
				continue // 无 vod_id 的记录无法去重，跳过
			}
			// 增量采集的第二道过滤：时间游标之前的旧数据不再处理。
			// 站点更新影片会同步刷新 vod_time，因此该过滤是安全的。
			if mode == model.ModeIncremental && src.LastCursor != "" &&
				v.VodTime != "" && v.VodTime <= src.LastCursor {
				continue
			}
			if v.VodTime > maxCursor {
				maxCursor = v.VodTime
			}
			// 播放地址为空时记为诊断项：接口没给 ≠ 入库丢了，便于在任务记录里定位。
			if v.VodPlayURL == "" && v.VodPlayList == "" {
				res.NoPlayURL++
			}
			videos = append(videos, v)
		}
		// 按视频自带的 type_id / type_name 幂等创建本站分类，并建立关联。
		// 分类在入库前确定，保证 Video.CategoryID 与分类记录同批写回。
		ids, err := s.categories.EnsureForVideos(src.ID, videos)
		if err != nil {
			res.ResumePage = page
			return fmt.Errorf("第 %d 页分类归档失败: %w", page, err)
		}
		for i := range videos {
			key := utils.CategoryKey(videos[i].TypeID, videos[i].TypeName)
			if key != "" {
				videos[i].CategoryID = ids[key]
			}
		}

		stats, err := s.videos.UpsertVideos(videos)
		if err != nil {
			res.ResumePage = page
			return fmt.Errorf("第 %d 页入库失败: %w", page, err)
		}
		res.Fetched += len(videos)
		res.New += stats.New
		res.Updated += stats.Updated
		res.Unchanged += stats.Unchanged
		task.progress(res, pageCount, page+1) // 让任务列表实时看到进度与总页数

		if pageCount > 0 && page >= pageCount {
			break
		}
	}
	// 正常结束（或按 maxPages 上限结束）：下次从头开始。
	res.ResumePage = 1
	return nil
}
