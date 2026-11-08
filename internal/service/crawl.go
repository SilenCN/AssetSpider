package service

import (
	"context"
	"fmt"
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
	SourceID   uint      `json:"sourceId"`
	SourceName string    `json:"sourceName"`
	Mode       string    `json:"mode"`
	Status     string    `json:"status"`
	Fetched    int       `json:"fetched"`
	New        int       `json:"new"`
	Updated    int       `json:"updated"`
	Unchanged  int       `json:"unchanged"`
	Pages      int       `json:"pages"`
	Total      int       `json:"total"`
	Cursor     string    `json:"cursor"`
	Message    string    `json:"message"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
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
type CrawlService struct {
	sources    repository.SourceRepository
	videos     repository.VideoRepository
	categories repository.CategoryRepository
	jobs       repository.JobRepository
	fetcher    *collector.Fetcher
	cfg        config.CollectorConfig

	mu      sync.Mutex
	running map[uint]bool // 防止同一站点并发采集
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
		running:    make(map[uint]bool),
	}
}

func (s *CrawlService) lock(id uint) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[id] {
		return false
	}
	s.running[id] = true
	return true
}

func (s *CrawlService) unlock(id uint) {
	s.mu.Lock()
	delete(s.running, id)
	s.mu.Unlock()
}

// RunOne 对单个站点执行一次全量或增量采集。
func (s *CrawlService) RunOne(ctx context.Context, id uint, mode string) (*RunResult, error) {
	if mode != model.ModeFull && mode != model.ModeIncremental {
		return nil, invalid("不支持的采集模式 %q（可选 full / incremental）", mode)
	}
	src, err := s.sources.GetSource(id)
	if err != nil {
		return nil, wrapRepo(err)
	}
	if !s.lock(id) {
		return nil, conflict("站点「%s」正在采集中，请稍后再试", src.Name)
	}
	defer s.unlock(id)

	job := &model.CrawlJob{
		SourceID: src.ID, SourceName: src.Name, Mode: mode,
		Status: model.JobRunning, StartedAt: time.Now(),
	}
	if err := s.jobs.CreateJob(job); err != nil {
		return nil, fmt.Errorf("创建采集记录失败: %w", err)
	}

	res := &RunResult{
		SourceID: src.ID, SourceName: src.Name, Mode: mode,
		StartedAt: job.StartedAt,
	}
	crawlErr := s.crawl(ctx, src, mode, res)

	now := time.Now()
	res.FinishedAt = now
	status, msg := model.JobSuccess, "采集完成"
	errMsg := ""
	if crawlErr != nil {
		status, msg = model.JobFailed, crawlErr.Error()
		errMsg = crawlErr.Error()
	}
	res.Status, res.Message = status, msg

	job.Fetched, job.New, job.Updated = res.Fetched, res.New, res.Updated
	job.Unchanged, job.Pages, job.Total = res.Unchanged, res.Pages, res.Total
	if err := s.jobs.FinishJob(job, status, msg); err != nil {
		return res, fmt.Errorf("写入采集记录失败: %w", err)
	}

	cursor := ""
	if crawlErr == nil {
		cursor = res.Cursor // 失败时不推进游标，避免漏采
	}
	if err := s.sources.UpdateSourceResult(src.ID, mode, status, cursor, errMsg, now); err != nil {
		return res, fmt.Errorf("更新站点状态失败: %w", err)
	}
	if crawlErr != nil {
		return res, crawlErr
	}
	return res, nil
}

// RunAll 对所有启用站点执行同一模式的采集。
func (s *CrawlService) RunAll(ctx context.Context, mode string) ([]RunResult, error) {
	if mode != model.ModeFull && mode != model.ModeIncremental {
		return nil, invalid("不支持的采集模式 %q（可选 full / incremental）", mode)
	}
	srcs, err := s.sources.ListEnabledSources()
	if err != nil {
		return nil, err
	}
	parallel := s.cfg.Parallel
	if parallel < 1 {
		parallel = 1
	}
	results := make([]RunResult, len(srcs))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for i, src := range srcs {
		wg.Add(1)
		go func(i int, src model.Source) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
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

// crawl 执行分页循环与入库。
func (s *CrawlService) crawl(ctx context.Context, src *model.Source, mode string, res *RunResult) error {
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

	pageCount := 0
	maxCursor := src.LastCursor
	for page := 1; page <= maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		list, err := s.fetcher.FetchList(ctx, *src, page, hours)
		if err != nil {
			return fmt.Errorf("第 %d 页采集失败: %w", page, err)
		}
		res.Pages++
		if page == 1 {
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
			// 海洋CMS 系站点更新影片会同步刷新 vod_time，因此该过滤是安全的。
			if mode == model.ModeIncremental && src.LastCursor != "" &&
				v.VodTime != "" && v.VodTime <= src.LastCursor {
				continue
			}
			if v.VodTime > maxCursor {
				maxCursor = v.VodTime
			}
			videos = append(videos, v)
		}
		// 按视频自带的 type_id / type_name 幂等创建本站分类，并建立关联。
		// 分类在入库前确定，保证 Video.CategoryID 与分类记录同批写回。
		ids, err := s.categories.EnsureForVideos(src.ID, videos)
		if err != nil {
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
			return fmt.Errorf("第 %d 页入库失败: %w", page, err)
		}
		res.Fetched += len(videos)
		res.New += stats.New
		res.Updated += stats.Updated
		res.Unchanged += stats.Unchanged

		if page >= pageCount {
			break
		}
	}
	res.Cursor = maxCursor
	return nil
}
