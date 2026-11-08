package collector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/store"
)

// RunResult 是一次采集的结果摘要。
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

// Runner 编排采集：分页拉取 → 去重入库 → 回写状态与采集记录。
type Runner struct {
	st      *store.Store
	fetcher *Fetcher
	cfg     config.CollectorConfig

	mu      sync.Mutex
	running map[uint]bool // 防止同一站点并发采集
}

// NewRunner 创建采集编排器。
func NewRunner(st *store.Store, cfg config.CollectorConfig) *Runner {
	return &Runner{
		st:      st,
		fetcher: NewFetcher(cfg),
		cfg:     cfg,
		running: make(map[uint]bool),
	}
}

func (r *Runner) lock(id uint) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running[id] {
		return false
	}
	r.running[id] = true
	return true
}

func (r *Runner) unlock(id uint) {
	r.mu.Lock()
	delete(r.running, id)
	r.mu.Unlock()
}

// RunOne 对单个站点执行一次全量或增量采集。
func (r *Runner) RunOne(ctx context.Context, id uint, mode string) (*RunResult, error) {
	if mode != model.ModeFull && mode != model.ModeIncremental {
		return nil, fmt.Errorf("不支持的采集模式 %q（可选 full / incremental）", mode)
	}
	src, err := r.st.GetSource(id)
	if err != nil {
		return nil, fmt.Errorf("站点 %d 不存在: %w", id, err)
	}
	if !r.lock(id) {
		return nil, fmt.Errorf("站点「%s」正在采集中，请稍后再试", src.Name)
	}
	defer r.unlock(id)

	job := &model.CrawlJob{
		SourceID: src.ID, SourceName: src.Name, Mode: mode,
		Status: model.JobRunning, StartedAt: time.Now(),
	}
	if err := r.st.CreateJob(job); err != nil {
		return nil, fmt.Errorf("创建采集记录失败: %w", err)
	}

	res := &RunResult{
		SourceID: src.ID, SourceName: src.Name, Mode: mode,
		StartedAt: job.StartedAt,
	}
	crawlErr := r.crawl(ctx, src, mode, res)

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
	if err := r.st.FinishJob(job, status, msg); err != nil {
		return res, fmt.Errorf("写入采集记录失败: %w", err)
	}

	cursor := ""
	if crawlErr == nil {
		cursor = res.Cursor // 失败时不推进游标，避免漏采
	}
	if err := r.st.UpdateSourceResult(src.ID, mode, status, cursor, errMsg, now); err != nil {
		return res, fmt.Errorf("更新站点状态失败: %w", err)
	}
	if crawlErr != nil {
		return res, crawlErr
	}
	return res, nil
}

// RunAll 对所有启用站点执行同一模式的采集（管理员视角）。
func (r *Runner) RunAll(ctx context.Context, mode string) ([]RunResult, error) {
	return r.RunAllForSources(ctx, mode, nil)
}

// RunAllForSources 对指定的启用站点执行采集。
// allowed == nil 表示全部启用站点；非 nil（含空切片）时只采集集合内的启用站点，
// 供普通用户把批量采集严格限制在自己可见的站点范围内。
func (r *Runner) RunAllForSources(ctx context.Context, mode string, allowed []uint) ([]RunResult, error) {
	if mode != model.ModeFull && mode != model.ModeIncremental {
		return nil, fmt.Errorf("不支持的采集模式 %q（可选 full / incremental）", mode)
	}
	srcs, err := r.st.ListEnabledSources()
	if err != nil {
		return nil, err
	}
	if allowed != nil {
		set := make(map[uint]bool, len(allowed))
		for _, id := range allowed {
			set[id] = true
		}
		filtered := make([]model.Source, 0, len(srcs))
		for _, src := range srcs {
			if set[src.ID] {
				filtered = append(filtered, src)
			}
		}
		srcs = filtered
	}
	parallel := r.cfg.Parallel
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
			res, runErr := r.RunOne(ctx, src.ID, mode)
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
func (r *Runner) crawl(ctx context.Context, src *model.Source, mode string, res *RunResult) error {
	hours := 0
	if mode == model.ModeIncremental {
		hours = src.IncrementalHours
		if hours <= 0 {
			hours = r.cfg.DefaultHours
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

	maxPages := r.cfg.MaxPages
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
		list, err := r.fetcher.FetchList(ctx, *src, page, hours)
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
			v := toVideo(src.ID, item)
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
		stats, err := r.st.UpsertVideos(videos)
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

// toVideo 把接口记录全量转换为入库模型并计算内容指纹。
//
// 映射完全由 model.VodFields 驱动，新增字段只需改清单与实体，无需改动这里；
// 清单之外的字段（站点自定义字段、嵌套对象等）按原始 JSON 值写入 Extra，
// 原始单条 JSON 全文写入 Raw，因此接口返回的任何字段都不会丢失。
func toVideo(sourceID uint, item VideoItem) model.Video {
	v := model.Video{SourceID: sourceID}
	rv := reflect.ValueOf(&v).Elem()
	for _, f := range model.VodFields {
		val, ok := item.Fields[f.Key]
		if !ok {
			continue
		}
		fv := rv.FieldByName(f.Go)
		if fv.IsValid() && fv.CanSet() && fv.Kind() == reflect.String {
			fv.SetString(val)
		} else {
			// 清单与实体脱节属于代码错误，宁可显式失败也不静默丢字段。
			panic(fmt.Sprintf("Video 缺少字段 %s（接口键 %s）", f.Go, f.Key))
		}
	}

	extra := make(map[string]json.RawMessage)
	for k, raw := range item.RawFields {
		if !model.IsVodField(k) {
			extra[k] = raw
		}
	}
	if len(extra) > 0 {
		if b, err := json.Marshal(extra); err == nil {
			v.Extra = string(b)
		}
	}

	v.VodTime = normalizeTime(v.VodTime) // 游标需要可字典序比较的时间
	v.Raw = item.RawJSON
	v.Hash = fingerprint(item)
	return v
}

// fingerprint 基于该条记录的全部键值（含未映射的自定义字段）计算指纹。
// 键排序后拼接，字段顺序或空白变化不会误判为内容更新。
func fingerprint(item VideoItem) string {
	keys := make([]string, 0, len(item.Fields))
	for k := range item.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(item.Fields[k])
		b.WriteByte('\x1f')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:16])
}

// normalizeTime 把站点时间统一为可字典序比较的本地时间字符串。
func normalizeTime(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05", "2006-01-02T15:04:05",
		"2006-01-02 15:04", "2006-01-02",
	} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.Format("2006-01-02 15:04:05")
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 1e12 { // 毫秒时间戳
			n /= 1000
		}
		return time.Unix(n, 0).Local().Format("2006-01-02 15:04:05")
	}
	return s
}
