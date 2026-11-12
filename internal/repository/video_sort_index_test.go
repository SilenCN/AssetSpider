package repository

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SilenCN/AssetSpider/internal/model"
	"gorm.io/gorm"
)

func waitVideoSortIndexes(t *testing.T, r *Repositories) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for videoSortIndexesMissing(r.db) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if videoSortIndexesMissing(r.db) {
		t.Fatal("后台维护任务未在 15 秒内创建评分排序索引")
	}
}

func videoSortPlan(t *testing.T, r *Repositories, sort string) string {
	t.Helper()
	var plan []struct {
		Detail string `gorm:"column:detail"`
	}
	if err := r.db.Raw("EXPLAIN QUERY PLAN SELECT id FROM videos ORDER BY " + videoOrder(sort) + " LIMIT 20").
		Scan(&plan).Error; err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, p := range plan {
		b.WriteString(p.Detail)
		b.WriteString(" ")
	}
	return b.String()
}

func videoNames(items []model.Video) []string {
	out := make([]string, 0, len(items))
	for _, v := range items {
		out = append(out, v.VodName)
	}
	return out
}

// 后台维护创建表达式索引：两条评分排序都要命中索引，且结果顺序与原先一致。
func TestVideoScoreSortUsesExpressionIndex(t *testing.T) {
	r := newTestRepos(t)
	if _, err := r.Videos.UpsertVideos([]model.Video{
		{SourceID: 1, VodID: "s1", VodName: "低分影片", VodScore: "3.2", VodTime: "2026-01-01 10:00:00", Hash: "h1"},
		{SourceID: 1, VodID: "s2", VodName: "高分影片", VodScore: "9.8", VodTime: "2026-01-02 10:00:00", Hash: "h2"},
		{SourceID: 1, VodID: "s3", VodName: "无评分影片", VodScore: "", VodTime: "2026-01-03 10:00:00", Hash: "h3"},
	}); err != nil {
		t.Fatal(err)
	}
	waitVideoSortIndexes(t, r)

	for _, c := range []struct{ sort, index string }{
		{"score_desc", "idx_videos_score_desc"},
		{"score_asc", "idx_videos_score_asc"},
	} {
		if p := videoSortPlan(t, r, c.sort); !strings.Contains(p, c.index) {
			t.Errorf("%s 未命中 %s，实际计划: %s", c.sort, c.index, p)
		}
	}

	// 空评分经 NULLIF 转 NULL：DESC 排最后、ASC 排最前，索引不改变这个语义。
	desc, err := r.Videos.ListVideos(VideoQuery{Sort: "score_desc", Page: 1, Size: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got := videoNames(desc); len(got) != 3 || got[0] != "高分影片" || got[2] != "无评分影片" {
		t.Errorf("score_desc 顺序异常: %v", got)
	}
	asc, err := r.Videos.ListVideos(VideoQuery{Sort: "score_asc", Page: 1, Size: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got := videoNames(asc); len(got) != 3 || got[0] != "无评分影片" || got[2] != "高分影片" {
		t.Errorf("score_asc 顺序异常: %v", got)
	}
}

// 两条索引齐全且 bigram 已回灌完时，重启不应再启动后台维护任务。
func TestVideoMaintenanceSkippedWhenReady(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ready.db")
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	waitVideoSortIndexes(t, r)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if again.maintenance != nil {
		t.Error("索引与回灌都已就绪时不应再启动后台维护任务")
	}
}

// 索引缺失检测：少一条就应触发后台补齐，两条都在则空转。
func TestVideoSortIndexesMissingDetection(t *testing.T) {
	r := newTestRepos(t)
	waitVideoSortIndexes(t, r)
	if videoSortIndexesMissing(r.db) {
		t.Fatal("两条索引都在时不应报缺失")
	}
	if err := r.db.Exec("DROP INDEX " + videoSortIndexNames[0]).Error; err != nil {
		t.Fatal(err)
	}
	if !videoSortIndexesMissing(r.db) {
		t.Fatal("少一条索引时应报缺失")
	}
}

// 1 字 / 含通配符的关键词即使 FTS 可用也直接扫主表六列 LIKE，
// 不再绕道 FTS5 外部内容表（实测更慢）。
func TestShortKeywordFallsBackToMainTable(t *testing.T) {
	r := newTestRepos(t)
	for _, kw := range []string{"流", "流%"} {
		dry := applyVideoKeyword(r.db.Model(&model.Video{}), kw,
			videoSearchCaps{fts: true, bigram: true}).
			Session(&gorm.Session{DryRun: true}).Find(&[]model.Video{}).Statement
		sql := dry.SQL.String()
		if strings.Contains(sql, videoSearchTable) || strings.Contains(sql, videoBigramTable) {
			t.Errorf("短词 %q 回退不应引用索引表: %s", kw, sql)
		}
		if !strings.Contains(sql, "LIKE") {
			t.Errorf("短词 %q 应回退 LIKE: %s", kw, sql)
		}
	}
}
