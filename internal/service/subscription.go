package service

import (
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

// SubscriptionInput 是订阅配置中允许前端提交的字段。
type SubscriptionInput struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	// IntervalHours 为 0 表示不自动刷新（只能手动刷新）。
	IntervalHours int `json:"intervalHours"`
	// 用指针：未带该字段的旧客户端 / 局部 PUT 不应把订阅误停用。
	Enabled *bool `json:"enabled"`
}

func (in SubscriptionInput) validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return invalid("订阅名称不能为空")
	}
	raw := strings.TrimSpace(in.URL)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return invalid("订阅地址需为完整的 http(s) URL，例如 https://example.com/sources.json")
	}
	if in.IntervalHours < 0 {
		return invalid("刷新间隔不能为负")
	}
	return nil
}

func (in SubscriptionInput) apply(dst *model.Subscription) *model.Subscription {
	dst.Name = strings.TrimSpace(in.Name)
	dst.URL = strings.TrimSpace(in.URL)
	dst.IntervalHours = in.IntervalHours
	if in.Enabled != nil {
		dst.Enabled = *in.Enabled
	} else if dst.ID == 0 {
		dst.Enabled = true
	}
	return dst
}

// SubscriptionView 是订阅的对外视图：除配置外带上按间隔现算的下次刷新时刻。
type SubscriptionView struct {
	model.Subscription
	NextRefreshAt *time.Time `json:"nextRefreshAt"`
}

// RefreshResult 是一次订阅刷新的结果统计。
type RefreshResult struct {
	Total    int           `json:"total"`
	Created  int           `json:"created"`
	Updated  int           `json:"updated"`
	Disabled int           `json:"disabled"`
	Skipped  int           `json:"skipped"`
	Issues   []ImportIssue `json:"issues,omitempty"`
}

// 订阅刷新状态（与采集记录复用同一套取值，前端直接显示）。
const (
	SubscriptionNever   = "never"
	SubscriptionSuccess = model.JobSuccess
	SubscriptionFailed  = model.JobFailed
)

// SubscriptionService 负责订阅的增删改查与「拉取 → 写站点表」的同步编排。
//
// 同步语义（与手动导入刻意保持一致的地方）：
//   - 同一订阅内按接口地址匹配已有站点，地址变了再按名称兜底；都不命中则新建；
//   - 站点名称全局唯一，若与「手动添加的站点 / 其它订阅的站点」撞名则跳过该条
//     并记入 issues，而不是悄悄改掉别人的配置；
//   - 已有站点的启用状态以本地为准：订阅不该把用户手动停用的站点重新打开；
//   - 订阅里已被移除的站点只停用、不删除，避免连带删掉已采影片与采集记录。
type SubscriptionService struct {
	subs      repository.SubscriptionRepository
	sources   repository.SourceRepository
	timeout   time.Duration
	userAgent string

	// mu 串行化刷新：手动刷新与定时刷新可能同时命中同一个订阅，
	// 并发 upsert 会互相覆盖，也会让「上次刷新结果」计数错乱。
	mu sync.Mutex
	// dueMu 防止上一轮到期检查还没跑完、下一分钟又起一轮。
	dueMu sync.Mutex
}

func NewSubscriptionService(subs repository.SubscriptionRepository, sources repository.SourceRepository, cfg config.CollectorConfig) *SubscriptionService {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	ua := strings.TrimSpace(cfg.UserAgent)
	if ua == "" {
		ua = "AssetSpider/1.0 (+subscription)"
	}
	return &SubscriptionService{subs: subs, sources: sources, timeout: timeout, userAgent: ua}
}

func (s *SubscriptionService) List() ([]SubscriptionView, error) {
	subs, err := s.subs.ListSubscriptions()
	if err != nil {
		return nil, wrapRepo(err)
	}
	now := time.Now()
	out := make([]SubscriptionView, 0, len(subs))
	for _, sub := range subs {
		out = append(out, SubscriptionView{Subscription: sub, NextRefreshAt: nextRefreshAt(sub, now)})
	}
	return out, nil
}

// nextRefreshAt 计算下次自动刷新时刻；未启用 / 间隔为 0 时返回 nil。
// 从未刷新过时以创建时间起算，因此「新建订阅 + 间隔 1 小时」会在 1 小时后自动拉一次。
func nextRefreshAt(sub model.Subscription, now time.Time) *time.Time {
	if !sub.Enabled || sub.IntervalHours <= 0 {
		return nil
	}
	base := sub.CreatedAt
	if sub.LastSyncAt != nil {
		base = *sub.LastSyncAt
	}
	next := base.Add(time.Duration(sub.IntervalHours) * time.Hour)
	if next.Before(now) {
		// 已过期（例如停机跨过了刷新点）：下一分钟就会执行，这里显示当前时刻。
		return &now
	}
	return &next
}

func (s *SubscriptionService) Create(in SubscriptionInput) (*SubscriptionView, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	if err := s.ensureNameFree(in.Name, 0); err != nil {
		return nil, err
	}
	sub := in.apply(&model.Subscription{})
	if err := s.subs.CreateSubscription(sub); err != nil {
		return nil, wrapRepo(err)
	}
	// 直接回带下次刷新时刻：前端新增后立刻能显示「下次几点刷」，不必再拉一次列表。
	return &SubscriptionView{Subscription: *sub, NextRefreshAt: nextRefreshAt(*sub, time.Now())}, nil
}

func (s *SubscriptionService) Update(id uint, in SubscriptionInput) (*SubscriptionView, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	if _, err := s.subs.GetSubscription(id); err != nil {
		return nil, wrapRepo(err)
	}
	if err := s.ensureNameFree(in.Name, id); err != nil {
		return nil, err
	}
	updated, err := s.subs.UpdateSubscription(id, in.apply(&model.Subscription{ID: id}))
	if err != nil {
		return nil, wrapRepo(err)
	}
	return &SubscriptionView{Subscription: *updated, NextRefreshAt: nextRefreshAt(*updated, time.Now())}, nil
}

// Delete 删除订阅并把它名下的站点解绑为手动站点（站点与影片都保留）。
func (s *SubscriptionService) Delete(id uint) error {
	if _, err := s.subs.GetSubscription(id); err != nil {
		return wrapRepo(err)
	}
	return wrapRepo(s.subs.DeleteSubscription(id))
}

// ensureNameFree 校验订阅名称唯一（忽略大小写与首尾空白）；exceptID 为更新时的自身 ID。
func (s *SubscriptionService) ensureNameFree(name string, exceptID uint) error {
	subs, err := s.subs.ListSubscriptions()
	if err != nil {
		return wrapRepo(err)
	}
	key := strings.ToLower(strings.TrimSpace(name))
	for _, sub := range subs {
		if sub.ID != exceptID && strings.ToLower(strings.TrimSpace(sub.Name)) == key {
			return conflict("订阅名称已存在：%s", strings.TrimSpace(name))
		}
	}
	return nil
}

// RefreshDue 检查所有到期订阅并逐个刷新，返回成功刷新的个数。
//
// 由调度器每分钟调用一次：间隔以小时计，分钟级检查足以命中；判定依据是
// 「上次刷新时间 + 间隔」，因此进程重启不会丢掉刷新点。
func (s *SubscriptionService) RefreshDue(now time.Time) int {
	if !s.dueMu.TryLock() {
		return 0 // 上一轮还在跑（例如订阅地址很慢），本轮直接跳过
	}
	defer s.dueMu.Unlock()

	subs, err := s.subs.ListSubscriptions()
	if err != nil {
		log.Printf("定时刷新订阅：读取订阅失败: %v", err)
		return 0
	}
	refreshed := 0
	for _, sub := range subs {
		if !subscriptionDue(sub, now) {
			continue
		}
		if _, err := s.Refresh(sub.ID); err != nil {
			log.Printf("订阅 #%d（%s）定时刷新失败: %v", sub.ID, sub.Name, err)
			continue
		}
		log.Printf("订阅 #%d（%s）定时刷新完成", sub.ID, sub.Name)
		refreshed++
	}
	return refreshed
}

// subscriptionDue 判断订阅是否到了该刷新的时刻。
//
// 刷新失败也会写入 LastSyncAt：坏掉的订阅最多按间隔重试一次，
// 不至于每分钟都去敲一个已经挂掉的地址。
func subscriptionDue(sub model.Subscription, now time.Time) bool {
	if !sub.Enabled || sub.IntervalHours <= 0 {
		return false
	}
	base := sub.CreatedAt
	if sub.LastSyncAt != nil {
		base = *sub.LastSyncAt
	}
	return !now.Before(base.Add(time.Duration(sub.IntervalHours) * time.Hour))
}

// summary 把一次刷新的计数拼成一句人话，供页面提示与日志使用。
func (r RefreshResult) summary() string {
	parts := []string{fmt.Sprintf("共 %d 条", r.Total)}
	for _, p := range []struct {
		n    int
		text string
	}{
		{r.Created, "新增"}, {r.Updated, "更新"}, {r.Disabled, "停用"}, {r.Skipped, "跳过"},
	} {
		if p.n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d 个", p.text, p.n))
		}
	}
	return strings.Join(parts, "，")
}
