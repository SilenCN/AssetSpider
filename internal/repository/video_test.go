package repository

import (
	"errors"
	"reflect"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
)

func TestUpsertVideosDedup(t *testing.T) {
	r := newTestRepos(t)
	mk := func(hash, name string) model.Video {
		return model.Video{
			SourceID: 1, VodID: "21", VodName: name, TypeName: "动作片",
			Hash: hash, VodTime: "2026-11-07 10:00:00", VodPlayURL: "https://x/1.m3u8",
		}
	}
	stats, err := r.Videos.UpsertVideos([]model.Video{mk("h1", "测试片")})
	if err != nil || stats.New != 1 {
		t.Fatalf("首次写入异常: %+v %v", stats, err)
	}
	stats, err = r.Videos.UpsertVideos([]model.Video{mk("h1", "测试片")})
	if err != nil || stats.Unchanged != 1 || stats.New != 0 {
		t.Fatalf("重复写入应判定未变化: %+v %v", stats, err)
	}
	stats, err = r.Videos.UpsertVideos([]model.Video{mk("h2", "测试片 更新版")})
	if err != nil || stats.Updated != 1 {
		t.Fatalf("内容变化应被更新: %+v %v", stats, err)
	}

	items, total, err := r.Videos.ListVideos(NewVideoQuery(0, "", 1, 20))
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("去重后应只有 1 条: %v total=%d", err, total)
	}
	if items[0].VodName != "测试片 更新版" || items[0].FirstSeenAt.IsZero() {
		t.Fatalf("更新后内容异常: %+v", items[0])
	}
	if _, total, _ = r.Videos.ListVideos(NewVideoQuery(0, "不存在", 1, 20)); total != 0 {
		t.Errorf("关键词过滤异常: total=%d", total)
	}
	if _, total, _ = r.Videos.ListVideos(NewVideoQuery(2, "", 1, 20)); total != 0 {
		t.Errorf("站点过滤异常: total=%d", total)
	}
	counts, err := r.Videos.CountVideosBySource()
	if err != nil || counts[1] != 1 {
		t.Fatalf("CountVideosBySource 异常: %v %+v", err, counts)
	}
}

// mkFullVideo 把所有标准字段填成带标记的值，用于验证全字段写入与更新。
func mkFullVideo(mark, hash string) model.Video {
	v := model.Video{
		SourceID: 1, VodID: "21", Hash: hash,
		VodTime: "2026-11-07 10:00:00",
		Extra:   `{"custom":"` + mark + `"}`,
	}
	rv := reflect.ValueOf(&v).Elem()
	for _, f := range model.VodFields {
		fv := rv.FieldByName(f.Go)
		if fv.IsValid() && fv.CanSet() && fv.Kind() == reflect.String && fv.String() == "" {
			fv.SetString(mark + "_" + f.Key)
		}
	}
	return v
}

func TestVideoUpdateMapCoversAllFields(t *testing.T) {
	v := mkFullVideo("a", "h1")
	m := videoUpdateMap(v)
	for _, f := range model.VodFields {
		got, ok := m[f.Column]
		if !ok {
			t.Errorf("更新 map 缺少列 %s", f.Column)
			continue
		}
		if got != "a_"+f.Key && f.Key != "vod_id" && f.Key != "vod_time" {
			t.Errorf("列 %s 取值异常: %v", f.Column, got)
		}
	}
	for _, key := range []string{"hash", "extra", "category_id", "last_seen_at"} {
		if _, ok := m[key]; !ok {
			t.Errorf("更新 map 缺少 %s", key)
		}
	}
	if _, ok := m["raw"]; ok {
		t.Error("更新 map 不应再包含 raw 列")
	}
	if len(m) != len(model.VodFields)+4 {
		t.Errorf("更新列数 %d != %d", len(m), len(model.VodFields)+4)
	}
}

func TestUpsertVideosBackfillsPlayListWhenUnchanged(t *testing.T) {
	r := newTestRepos(t)
	first := model.Video{
		SourceID: 1, VodID: "21", VodName: "测试片", TypeName: "动作片",
		Hash: "h1", VodTime: "2026-11-07 10:00:00", VodPlayURL: "https://x/1.m3u8",
		// 首采时尚未解析出播放组（上游未返回 / 解析失败），库中 vod_play_list 为空。
		VodPlayList: "",
	}
	if _, err := r.Videos.UpsertVideos([]model.Video{first}); err != nil {
		t.Fatal(err)
	}
	// 内容指纹与上次一致 → 命中 Unchanged 分支，但本次解析出了播放组，应回写。
	second := first
	second.VodPlayList = `[{"from":"youku","episodes":[{"name":"第1集","url":"https://x/1.m3u8"}]}]`
	stats, err := r.Videos.UpsertVideos([]model.Video{second})
	if err != nil || stats.Unchanged != 1 {
		t.Fatalf("指纹未变应判定未变化: %+v %v", stats, err)
	}
	items, _, err := r.Videos.ListVideos(NewVideoQuery(0, "", 1, 20))
	if err != nil || len(items) != 1 {
		t.Fatalf("查询异常: %v n=%d", err, len(items))
	}
	if items[0].VodPlayList == "" {
		t.Fatalf("播放组未在 Unchanged 分支回写，沿用旧空值: %+v", items[0])
	}
}

func TestUpsertVideosUpdatesAllFields(t *testing.T) {
	r := newTestRepos(t)
	if _, err := r.Videos.UpsertVideos([]model.Video{mkFullVideo("first", "h1")}); err != nil {
		t.Fatal(err)
	}
	stats, err := r.Videos.UpsertVideos([]model.Video{mkFullVideo("second", "h2")})
	if err != nil || stats.Updated != 1 {
		t.Fatalf("全字段内容变化应触发更新: %+v %v", stats, err)
	}

	items, total, err := r.Videos.ListVideos(NewVideoQuery(0, "", 1, 20))
	if err != nil || total != 1 {
		t.Fatalf("总数异常: %v total=%d", err, total)
	}
	v := items[0]
	rv := reflect.ValueOf(v)
	skipped := map[string]bool{"vod_id": true, "vod_time": true}
	for _, f := range model.VodFields {
		if skipped[f.Key] {
			continue
		}
		if got := rv.FieldByName(f.Go).String(); got != "second_"+f.Key {
			t.Errorf("更新后字段 %s = %q，期望 second_%s", f.Column, got, f.Key)
		}
	}
	if v.Hash != "h2" || v.Extra != `{"custom":"second"}` {
		t.Errorf("元数据字段未随更新写回: hash=%s extra=%s", v.Hash, v.Extra)
	}
	got, err := r.Videos.GetVideo(v.ID)
	if err != nil || got.VodName != "second_vod_name" {
		t.Fatalf("GetVideo 异常: %v %+v", err, got)
	}
	if _, err := r.Videos.GetVideo(999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("不存在的影片应返回 ErrNotFound，实际 %v", err)
	}
	// 多字段搜索：演员、导演、分类也应可检索
	if _, total, _ := r.Videos.ListVideos(NewVideoQuery(0, "second_vod_actor", 1, 20)); total != 1 {
		t.Error("按演员字段搜索未命中")
	}
}
