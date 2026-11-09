package service

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

// TestPlayURLRewriterDomain 验证播放域名替换：只换协议与域名，保留路径与查询串。
func TestPlayURLRewriterDomain(t *testing.T) {
	r := NewPlayURLRewriter("", "https://cdn.example.com/")
	cases := []struct{ in, want string }{
		{"https://old.example.net/vod/1/index.m3u8", "https://cdn.example.com/vod/1/index.m3u8"},
		{"http://old.example.net/a.mp4?token=1#t=3", "https://cdn.example.com/a.mp4?token=1#t=3"},
		{"old.example.net/a.m3u8", "https://cdn.example.com/old.example.net/a.m3u8"},
		{"", ""},
	}
	for _, c := range cases {
		if got := r.Rewrite(c.in); got != c.want {
			t.Errorf("Rewrite(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

// TestPlayURLRewriterParseAPI 验证解析地址拼接：默认前缀拼接，支持 {url} 占位符。
func TestPlayURLRewriterParseAPI(t *testing.T) {
	prefix := NewPlayURLRewriter("https://jx.example.com/?url=", "")
	if got := prefix.Rewrite("https://old.example.net/1.m3u8"); got != "https://jx.example.com/?url=https://old.example.net/1.m3u8" {
		t.Errorf("前缀拼接异常: %q", got)
	}
	placeholder := NewPlayURLRewriter("https://jx.example.com/play?src={url}&q=1", "")
	if got := placeholder.Rewrite("https://a.com/b.m3u8"); got != "https://jx.example.com/play?src=https://a.com/b.m3u8&q=1" {
		t.Errorf("占位符拼接异常: %q", got)
	}
	// 同时配置：先换域名，再拼解析地址
	both := NewPlayURLRewriter("https://jx.example.com/?url=", "https://cdn.example.com")
	got := both.Rewrite("https://dead.example.net/v/1/index.m3u8")
	if got != "https://jx.example.com/?url=https://cdn.example.com/v/1/index.m3u8" {
		t.Errorf("组合改写异常: %q", got)
	}
	// 未配置时原样返回
	if off := NewPlayURLRewriter("", ""); off.Enabled() || off.Rewrite("https://a.com/b.m3u8") != "https://a.com/b.m3u8" {
		t.Errorf("未配置时不应改写")
	}
}

// TestNormalizePlayConfig 验证解析地址与播放域名的校验与规范化。
func TestNormalizePlayConfig(t *testing.T) {
	if v, err := NormalizePlayDomain(" cdn.example.com/ "); err != nil || v != "https://cdn.example.com" {
		t.Errorf("域名规范化异常: %q %v", v, err)
	}
	if v, err := NormalizePlayDomain(""); err != nil || v != "" {
		t.Errorf("空域名应允许: %q %v", v, err)
	}
	if _, err := NormalizePlayDomain("ftp://cdn.example.com"); err == nil {
		t.Errorf("非 http(s) 域名应校验失败")
	}
	if v, err := NormalizeParseAPI("https://jx.example.com/?url={url}"); err != nil || !strings.Contains(v, "{url}") {
		t.Errorf("解析地址应允许占位符: %q %v", v, err)
	}
	if _, err := NormalizeParseAPI("jx.example.com"); err == nil {
		t.Errorf("缺少协议的解析地址应校验失败")
	}
}

// TestNormalizePicConfig 验证图片解析地址与图片域名的校验与规范化。
func TestNormalizePicConfig(t *testing.T) {
	if v, err := NormalizePicDomain(" img.example.com/ "); err != nil || v != "https://img.example.com" {
		t.Errorf("图片域名规范化异常: %q %v", v, err)
	}
	if v, err := NormalizePicDomain(""); err != nil || v != "" {
		t.Errorf("空图片域名应允许: %q %v", v, err)
	}
	if _, err := NormalizePicDomain("javascript:alert(1)"); err == nil {
		t.Errorf("非 http(s) 图片域名应校验失败")
	}
	if v, err := NormalizePicAPI("https://img.example.com/p?url={url}"); err != nil || !strings.Contains(v, "{url}") {
		t.Errorf("图片解析地址应允许占位符: %q %v", v, err)
	}
	if _, err := NormalizePicAPI("img.example.com/p"); err == nil {
		t.Errorf("缺少协议的图片解析地址应校验失败")
	}
}

// TestSourcePlayAndPicConfigRoundTrip 验证站点配置的两个新字段能保存并回读。
func TestSourcePlayAndPicConfigRoundTrip(t *testing.T) {
	st, err := repository.Open(filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	svc := NewSourceService(st.Sources)
	enabled := true
	created, err := svc.Create(SourceInput{
		Name: "图片改写", APIBase: "https://site.example.com/api.php/provide/vod/",
		Enabled: &enabled, IncrementalHours: 24,
		PicAPI: "https://img.example.com/proxy?url=", PicDomain: "img.example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 保存时已规范化：域名补协议、去尾斜杠
	if created.PicDomain != "https://img.example.com" || created.PicAPI != "https://img.example.com/proxy?url=" {
		t.Fatalf("图片配置规范化异常: %+v", created)
	}

	got, err := svc.Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PicAPI != created.PicAPI || got.PicDomain != created.PicDomain {
		t.Fatalf("图片配置回读异常: %+v", got)
	}

	// 非法配置必须被拒绝
	bad := *created
	bad.PicDomain = "ftp://img.example.com"
	in := SourceInput{
		Name: bad.Name, APIBase: bad.APIBase, Format: bad.Format,
		PicDomain: bad.PicDomain,
	}
	if _, err := svc.Update(created.ID, in); err == nil {
		t.Errorf("非法图片域名应校验失败")
	}
}

// TestVideoServiceAppliesPicRewrite 验证封面图片按站点配置改写，且原始地址不被污染。
func TestVideoServiceAppliesPicRewrite(t *testing.T) {
	st, err := repository.Open(filepath.Join(t.TempDir(), "pic.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	src := &model.Source{
		Name: "图片改写站点", APIBase: "https://site.example.com/api.php/provide/vod/",
		Format: model.FormatMacCMSJSON, Enabled: true, IncrementalHours: 24,
		PicAPI: "https://img.example.com/proxy?url=", PicDomain: "https://img2.example.com",
	}
	if err := st.Sources.CreateSource(src); err != nil {
		t.Fatal(err)
	}
	stats, err := st.Videos.UpsertVideos([]model.Video{{
		SourceID: src.ID, VodID: "9", VodName: "封面改写",
		VodPic:           "https://dead.example.net/p/1.jpg",
		VodPicScreenshot: "https://dead.example.net/s/1.jpg#https://dead.example.net/s/2.jpg",
	}})
	if err != nil || stats.New != 1 {
		t.Fatalf("入库失败: %+v %v", stats, err)
	}

	svc := NewVideoService(st.Videos, st.Sources)
	list, err := svc.List(VideoListQuery{Page: 1, Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	v := list.Items[0]
	// 先换域名，再拼代理地址
	want := "https://img.example.com/proxy?url=https://img2.example.com/p/1.jpg"
	if v.VodPic != want {
		t.Errorf("封面改写异常: %q，期望 %q", v.VodPic, want)
	}
	// 截图是 # 连接的集合，逐段改写后拼回
	if strings.Count(v.VodPicScreenshot, "#") != 1 || strings.Count(v.VodPicScreenshot, "img.example.com/proxy") != 2 {
		t.Errorf("截图集合改写异常: %q", v.VodPicScreenshot)
	}

	// 数据库里仍是原始地址
	stored, err := st.Videos.GetVideo(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.VodPic != "https://dead.example.net/p/1.jpg" {
		t.Errorf("原始封面地址不应被改写: %q", stored.VodPic)
	}
}

// TestVideoServiceAppliesPlayRewrite 验证影片列表/播放组会按站点配置改写地址，
// 且数据库中的原始播放地址保持不变（配置只影响读取）。
func TestVideoServiceAppliesPlayRewrite(t *testing.T) {
	st, err := repository.Open(filepath.Join(t.TempDir(), "rewrite.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	src := &model.Source{
		Name: "改写站点", APIBase: "https://site.example.com/api.php/provide/vod/",
		Format: model.FormatMacCMSJSON, Enabled: true, IncrementalHours: 24,
		ParseAPI:   "https://jx.example.com/?url=",
		PlayDomain: "https://cdn.example.com",
	}
	if err := st.Sources.CreateSource(src); err != nil {
		t.Fatal(err)
	}

	// 另一站点不配置改写，用于确认互不影响。
	plain := &model.Source{
		Name: "普通站点", APIBase: "https://plain.example.com/api.php/provide/vod/",
		Format: model.FormatMacCMSJSON, Enabled: true, IncrementalHours: 24,
	}
	if err := st.Sources.CreateSource(plain); err != nil {
		t.Fatal(err)
	}

	playList := `[{"from":"m3u8","server":"no","note":"","episodes":[` +
		`{"name":"第1集","url":"https://dead.example.net/v/1/index.m3u8"}]}]`
	stats, err := st.Videos.UpsertVideos([]model.Video{
		{SourceID: src.ID, VodID: "1", VodName: "待改写", VodPlayList: playList},
		{SourceID: plain.ID, VodID: "2", VodName: "不改写", VodPlayList: playList},
	})
	if err != nil || stats.New != 2 {
		t.Fatalf("入库失败: %+v %v", stats, err)
	}

	svc := NewVideoService(st.Videos, st.Sources)
	list, err := svc.List(VideoListQuery{Page: 1, Size: 10})
	if err != nil {
		t.Fatal(err)
	}
	want := "https://jx.example.com/?url=https://cdn.example.com/v/1/index.m3u8"
	var rewritten, untouched bool
	for _, v := range list.Items {
		switch v.SourceID {
		case src.ID:
			groups := decodeGroups(t, v.VodPlayList)
			if groups[0].Episodes[0].Url != want {
				t.Errorf("播放地址改写异常: %q", groups[0].Episodes[0].Url)
			}
			// 拼了解析地址后必须打 frame 标记，前端才会用 iframe 而不是把解析页当 m3u8 播。
			if !groups[0].Episodes[0].Frame {
				t.Error("使用视频解析地址时播放组应标记 frame=true")
			}
			rewritten = true
		case plain.ID:
			groups := decodeGroups(t, v.VodPlayList)
			if groups[0].Episodes[0].Url != "https://dead.example.net/v/1/index.m3u8" {
				t.Errorf("未配置改写的站点不应变化: %q", groups[0].Episodes[0].Url)
			}
			if groups[0].Episodes[0].Frame {
				t.Error("未配置解析地址时不应标记 frame")
			}
			untouched = true
		}
	}
	if !rewritten || !untouched {
		t.Fatalf("列表应同时包含两个站点的影片: %+v", list.Items)
	}

	// 播放组子资源同样带改写
	groups, err := svc.GetPlayGroups(list.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if groups[0].Episodes[0].Url != want && groups[0].Episodes[0].Url != "https://dead.example.net/v/1/index.m3u8" {
		t.Fatalf("播放组改写异常: %+v", groups)
	}
	if groups[0].Episodes[0].Url == want && !groups[0].Episodes[0].Frame {
		t.Errorf("播放组子资源也应标记 frame=true: %+v", groups)
	}

	// 数据库里仍是原始地址
	stored, err := st.Videos.GetVideo(list.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored.VodPlayList, "https://dead.example.net/v/1/index.m3u8") {
		t.Errorf("原始播放地址不应被改写: %s", stored.VodPlayList)
	}
}

// TestURLRewriterUsesFrame 区分「只换域名」与「拼了解析地址」：
// 前者仍是媒体流可直链播放，后者是解析页面必须用 iframe。
func TestURLRewriterUsesFrame(t *testing.T) {
	cases := []struct {
		name       string
		prefix     string
		domain     string
		wantFrame  bool
		wantEnable bool
	}{
		{"未配置", "", "", false, false},
		{"只换域名", "", "https://cdn.example.com", false, true},
		{"拼解析地址", "https://jx.example.com/?url=", "", true, true},
		{"两者都配", "https://jx.example.com/?url=", "https://cdn.example.com", true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := NewURLRewriter(c.prefix, c.domain)
			if r.Enabled() != c.wantEnable || r.UsesFrame() != c.wantFrame {
				t.Errorf("Enabled=%v UsesFrame=%v，期望 %v / %v",
					r.Enabled(), r.UsesFrame(), c.wantEnable, c.wantFrame)
			}
		})
	}
}

func decodeGroups(t *testing.T, raw string) []model.PlayGroup {
	t.Helper()
	var groups []model.PlayGroup
	if err := json.Unmarshal([]byte(raw), &groups); err != nil {
		t.Fatalf("播放组解析失败: %v", err)
	}
	return groups
}
