package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

// Refresh 手动刷新一个订阅：拉取 → 解析 → 写站点表 → 回写结果。
//
// 失败时错误文案已经是「人话 + 技术细节」，同时会写进订阅的 lastError，
// 因此页面既能弹提示，也能在列表上看到失败原因。
func (s *SubscriptionService) Refresh(id uint) (RefreshResult, error) {
	sub, err := s.subs.GetSubscription(id)
	if err != nil {
		return RefreshResult{}, wrapRepo(err)
	}
	return s.refreshOne(*sub)
}

func (s *SubscriptionService) refreshOne(sub model.Subscription) (RefreshResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	fail := func(msg string) (RefreshResult, error) {
		// 失败也写 LastSyncAt：坏地址最多按间隔重试，不会被每分钟敲一次。
		_ = s.subs.SaveSubscriptionResult(sub.ID, SubscriptionFailed, msg, now, repository.SubscriptionStats{})
		return RefreshResult{}, fmt.Errorf("刷新订阅「%s」失败：%s", sub.Name, msg)
	}

	body, err := s.fetch(sub.URL)
	if err != nil {
		return fail(explainCrawlError(err))
	}
	items, err := DecodeSourceBundle(body)
	if err != nil {
		return fail("订阅内容不是合法的站点配置 JSON：" + err.Error())
	}
	if len(items) == 0 {
		return fail("订阅内容里没有任何站点配置")
	}

	stats, issues, err := s.applyItems(sub.ID, items)
	if err != nil {
		return fail("写入站点配置失败：" + err.Error())
	}
	if err := s.subs.SaveSubscriptionResult(sub.ID, SubscriptionSuccess, "", now, stats); err != nil {
		return RefreshResult{}, wrapRepo(err)
	}
	return RefreshResult{
		Total: len(items), Created: stats.Created, Updated: stats.Updated,
		Disabled: stats.Disabled, Skipped: stats.Skipped, Issues: issues,
	}, nil
}

// fetch 拉取订阅内容。超时取采集配置里的单次请求超时，避免一个卡住的订阅
// 拖住调度循环。
func (s *SubscriptionService) fetch(rawURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("订阅地址无效: %w", err)
	}
	req.Header.Set("User-Agent", s.userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求订阅地址失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("订阅地址返回 HTTP %d，请确认路径正确且可匿名访问", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxSourceBundleBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取订阅内容失败: %w", err)
	}
	if len(body) > MaxSourceBundleBytes {
		return nil, fmt.Errorf("订阅内容过大（上限 %d MB）", MaxSourceBundleBytes>>20)
	}
	return body, nil
}

// applyItems 把订阅里的站点配置写进站点表，返回各项计数与逐条问题。
func (s *SubscriptionService) applyItems(subID uint, items []SourceInput) (repository.SubscriptionStats, []ImportIssue, error) {
	var stats repository.SubscriptionStats
	var issues []ImportIssue

	existing, err := s.sources.ListSources()
	if err != nil {
		return stats, nil, err
	}
	byAPI := make(map[string]model.Source, len(items))
	byName := make(map[string]model.Source, len(existing))
	for _, src := range existing {
		byName[sourceNameKey(src.Name)] = src
		// 只索引本订阅的站点：同一地址若被别的订阅引用，不该被这里顺手接管。
		if src.SubscriptionID == subID {
			byAPI[sourceAPIKey(src.APIBase)] = src
		}
	}

	kept := make(map[uint]bool, len(items))
	for i, in := range items {
		name := strings.TrimSpace(in.Name)
		if err := in.validate(); err != nil {
			stats.Skipped++
			issues = append(issues, ImportIssue{Index: i, Name: name, Error: err.Error()})
			continue
		}
		target, ok := byAPI[sourceAPIKey(in.APIBase)]
		if !ok {
			if other, hit := byName[sourceNameKey(name)]; hit {
				if other.SubscriptionID != subID {
					stats.Skipped++
					issues = append(issues, ImportIssue{Index: i, Name: name,
						Error: "同名站点已存在且不归本订阅（手动添加或来自其它订阅），已跳过"})
					continue
				}
				target, ok = other, true
			}
		}
		if ok {
			updated, err := s.updateFromSubscription(target, in)
			if err != nil {
				stats.Skipped++
				issues = append(issues, ImportIssue{Index: i, Name: name, Error: err.Error()})
				continue
			}
			stats.Updated++
			kept[updated.ID] = true
			byAPI[sourceAPIKey(updated.APIBase)] = *updated
			byName[sourceNameKey(updated.Name)] = *updated
			continue
		}
		created := in.apply(&model.Source{SubscriptionID: subID})
		if err := s.sources.CreateSource(created); err != nil {
			stats.Skipped++
			issues = append(issues, ImportIssue{Index: i, Name: name, Error: err.Error()})
			continue
		}
		stats.Created++
		kept[created.ID] = true
		byAPI[sourceAPIKey(created.APIBase)] = *created
		byName[sourceNameKey(created.Name)] = *created
	}

	disabled, err := s.disableRemoved(subID, kept)
	if err != nil {
		return stats, issues, err
	}
	stats.Disabled = disabled
	return stats, issues, nil
}

// updateFromSubscription 用订阅里的配置覆盖已有站点。
//
// 启用状态保留本地值：订阅是配置分发通道，不该把用户因为站点抽风而手动停用的
// 站点又悄悄打开。其余字段（接口地址、解析、图片改写、定时等）以订阅为准。
func (s *SubscriptionService) updateFromSubscription(src model.Source, in SourceInput) (*model.Source, error) {
	enabled := src.Enabled
	updated := in.apply(&src)
	updated.Enabled = enabled
	updated.SubscriptionID = src.SubscriptionID
	return s.sources.UpdateSource(src.ID, updated)
}

// disableRemoved 停用「本订阅曾经创建、这次订阅里已经不在了」的站点。
// 只停用不删除：删除会连带清空已采影片与采集记录，代价远大于收益。
func (s *SubscriptionService) disableRemoved(subID uint, kept map[uint]bool) (int, error) {
	owned, err := s.sources.ListSourcesBySubscription(subID)
	if err != nil {
		return 0, err
	}
	var ids []uint
	for _, src := range owned {
		if !kept[src.ID] && src.Enabled {
			ids = append(ids, src.ID)
		}
	}
	if err := s.sources.SetSourcesEnabled(ids, false); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// sourceNameKey 是站点 / 订阅名称的归一化键：忽略大小写与首尾空白。
func sourceNameKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// sourceAPIKey 归一化接口地址：忽略大小写、首尾空白与结尾斜杠。
// 订阅维护者常在末尾多写 / 少写 /，不归一化会被当成两个不同站点。
func sourceAPIKey(apiBase string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(apiBase), "/"))
}
