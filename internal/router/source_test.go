package router_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SilenCN/AssetSpider/internal/app"
	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
	"github.com/gin-gonic/gin"
)

func TestSourceAndCrawlAPI(t *testing.T) {
	r, _ := newTestRouter(t)
	site := newMockVideoSite(t)
	apiBase := site.URL + "/api.php/provide/vod/"
	payload := map[string]interface{}{
		"name": "模拟资源站", "apiBase": apiBase, "format": "seacms_json",
		"extraParams": "t=1", "enabled": true, "incrementalHours": 24, "maxPages": 10,
	}

	// 1. 创建站点
	w := doJSON(r, http.MethodPost, "/api/sources/v1", payload)
	if w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}
	var src model.Source
	if err := json.Unmarshal(w.Body.Bytes(), &src); err != nil {
		t.Fatal(err)
	}
	if src.ID == 0 || !src.Enabled {
		t.Fatalf("创建结果异常: %+v", src)
	}

	// 2. 列表附带影片数
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/sources/v1", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"videoCount":0`) {
		t.Fatalf("站点列表异常: %d %s", w.Code, w.Body.String())
	}

	// 3. 全量采集并入库
	w = doJSON(r, http.MethodPost, "/api/sources/v1/"+strconv.Itoa(int(src.ID))+"/crawls?mode=full", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("全量采集失败: %d %s", w.Code, w.Body.String())
	}
	var first struct {
		New       int `json:"new"`
		Fetched   int `json:"fetched"`
		Unchanged int `json:"unchanged"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if first.Fetched != 2 || first.New != 2 {
		t.Fatalf("首次全量应新增 2 条: %+v", first)
	}

	// 4. 数据入库可查询
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/videos/v1?q=影片A", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("视频查询失败: %d %s", w.Code, w.Body.String())
	}
	var videos struct {
		Items []model.Video `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &videos); err != nil {
		t.Fatal(err)
	}
	if len(videos.Items) != 1 || videos.Items[0].VodID != "101" {
		t.Fatalf("入库数据异常: %+v", videos)
	}

	// 5. 再次全量 → 全部去重
	w = doJSON(r, http.MethodPost, "/api/sources/v1/"+strconv.Itoa(int(src.ID))+"/crawls?mode=full", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"new":0`) ||
		!strings.Contains(w.Body.String(), `"unchanged":2`) {
		t.Fatalf("重复采集未去重: %d %s", w.Code, w.Body.String())
	}

	// 6. 批量增量采集
	w = doJSON(r, http.MethodPost, "/api/crawls/v1?mode=incremental", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"mode":"incremental"`) {
		t.Fatalf("批量采集失败: %d %s", w.Code, w.Body.String())
	}

	// 7. 采集记录
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/jobs/v1", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("采集记录查询失败: %d", w.Code)
	}
	var jobs []model.CrawlJob
	if err := json.Unmarshal(w.Body.Bytes(), &jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 3 || jobs[0].Status != model.JobSuccess {
		t.Fatalf("采集记录异常: %d %+v", len(jobs), jobs)
	}

	// 8. 参数与资源校验
	if got := doJSON(r, http.MethodPost, "/api/sources/v1/"+strconv.Itoa(int(src.ID))+"/crawls?mode=bad", nil); got.Code != http.StatusBadRequest {
		t.Errorf("非法模式应返回 400，实际 %d", got.Code)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodPost, "/api/sources/v1/9999/crawls?mode=full", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("不存在的站点应返回 404，实际 %d", w.Code)
	}

	// 9. 更新与级联删除
	payload["name"] = "改名后的资源站"
	if w = doJSON(r, http.MethodPut, "/api/sources/v1/"+strconv.Itoa(int(src.ID)), payload); w.Code != http.StatusOK {
		t.Fatalf("更新站点失败: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodDelete, "/api/sources/v1/"+strconv.Itoa(int(src.ID)), nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("删除站点失败: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/videos/v1", nil))
	if got := listItemCount(t, w.Body.Bytes()); got != 0 {
		t.Fatalf("删除站点后影片未级联清理: %s", w.Body.String())
	}
}

func TestSourcePlayRewriteAPI(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		rec := map[string]interface{}{
			"vod_id": 301, "vod_name": "改写地址影片", "type_id": 6, "type_name": "动作片",
			"vod_time": "2026-11-08 10:00:00", "vod_play_from": "m3u8",
			"vod_play_url": "第1集$https://dead.example.net/v/1/index.m3u8",
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "ok", "page": 1, "pagecount": 1,
			"limit": "20", "total": 1, "list": []interface{}{rec},
		})
	}))
	t.Cleanup(mock.Close)

	r, _ := newTestRouter(t)

	// 1. 带解析地址与播放域名创建站点：域名会被规范化
	w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "改写站点", "apiBase": mock.URL + "/api.php/provide/vod/", "enabled": true,
		"parseApi":   "https://jx.example.com/?url=",
		"playDomain": " cdn.example.com/ ",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}
	var src model.Source
	if err := json.Unmarshal(w.Body.Bytes(), &src); err != nil {
		t.Fatal(err)
	}
	if src.ParseAPI != "https://jx.example.com/?url=" || src.PlayDomain != "https://cdn.example.com" {
		t.Fatalf("播放配置未按预期规范化: parseApi=%q playDomain=%q", src.ParseAPI, src.PlayDomain)
	}

	// 2. 采集后按 id 读取详情：播放地址应已被改写（列表只返回卡片投影）
	if w := doJSON(r, http.MethodPost,
		"/api/sources/v1/"+strconv.Itoa(int(src.ID))+"/crawls?mode=full", nil); w.Code != http.StatusOK {
		t.Fatalf("采集失败: %d %s", w.Code, w.Body.String())
	}
	want := "https://jx.example.com/?url=https://cdn.example.com/v/1/index.m3u8"
	videoID := func() uint {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authReq(http.MethodGet, "/api/videos/v1?q=改写地址影片", nil))
		var out struct {
			Items []model.VideoCard `json:"items"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Items) != 1 {
			t.Fatalf("查询影片失败: %d %s", w.Code, w.Body.String())
		}
		return out.Items[0].ID
	}
	detailOf := func(id uint) model.Video {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authReq(http.MethodGet, "/api/videos/v1/"+strconv.Itoa(int(id)), nil))
		var v model.Video
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &v) != nil {
			t.Fatalf("详情接口异常: %d %s", w.Code, w.Body.String())
		}
		return v
	}
	vid := videoID()
	v := detailOf(vid)
	var groups []model.PlayGroup
	if err := json.Unmarshal([]byte(v.VodPlayList), &groups); err != nil || len(groups) != 1 {
		t.Fatalf("播放组数据异常: %v %s", err, v.VodPlayList)
	}
	if got := groups[0].Episodes[0].Url; got != want {
		t.Fatalf("详情播放地址改写异常: %q，期望 %q", got, want)
	}
	if !groups[0].Episodes[0].Frame {
		t.Error("详情播放组应带 frame=true，供前端用 iframe 承载解析页面")
	}

	// 3. 播放组子资源同样改写
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet,
		"/api/videos/v1/"+strconv.Itoa(int(vid))+"/play-groups", nil))
	var got []model.PlayGroup
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got) != 1 {
		t.Fatalf("播放组接口异常: %d %s", w.Code, w.Body.String())
	}
	if got[0].Episodes[0].Url != want {
		t.Fatalf("播放组地址改写异常: %q", got[0].Episodes[0].Url)
	}
	if !got[0].Episodes[0].Frame {
		t.Error("播放组接口应带 frame=true")
	}

	// 4. 清空配置后恢复原始地址（数据库里始终是原始值）
	if w := doJSON(r, http.MethodPut, "/api/sources/v1/"+strconv.Itoa(int(src.ID)), map[string]interface{}{
		"name": src.Name, "apiBase": src.APIBase, "enabled": true, "incrementalHours": 24,
		"parseApi": "", "playDomain": "",
	}); w.Code != http.StatusOK {
		t.Fatalf("更新站点失败: %d %s", w.Code, w.Body.String())
	}
	v = detailOf(vid)
	groups = nil
	if err := json.Unmarshal([]byte(v.VodPlayList), &groups); err != nil || len(groups) != 1 {
		t.Fatalf("播放组数据异常: %v", err)
	}
	if got := groups[0].Episodes[0].Url; got != "https://dead.example.net/v/1/index.m3u8" {
		t.Fatalf("清空配置后应回到原始地址，实际 %q", got)
	}

	// 5. 非法解析地址被拒绝
	if w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "非法解析站", "apiBase": mock.URL + "/api.php/provide/vod/",
		"parseApi": "jx.example.com/?url=",
	}); w.Code != http.StatusBadRequest {
		t.Fatalf("非法解析地址应返回 400，实际 %d %s", w.Code, w.Body.String())
	}
}

// TestRecoverInterruptedOnRestart 验证服务重启后自动恢复被中断的采集任务：
// 复用原记录、从续采页继续、统计累加、不新开任务；用户手动暂停的任务不动。
func TestRecoverInterruptedOnRestart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "restart.db")
	webDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("<div id=\"app\"></div>"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 模拟站点：共 3 页，记录每次请求的页码。
	var mu sync.Mutex
	var pages []int
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Get("ac") == "typelist" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 1, "msg": "ok", "page": 1, "pagecount": 1, "limit": "1", "total": 0,
				"list": []interface{}{},
			})
			return
		}
		page, _ := strconv.Atoi(req.URL.Query().Get("pg"))
		if page < 1 {
			page = 1
		}
		mu.Lock()
		pages = append(pages, page)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "ok", "page": page, "pagecount": 3, "limit": "1", "total": 3,
			"list": []map[string]interface{}{{
				"vod_id": 2000 + page, "vod_name": fmt.Sprintf("重启恢复%d", page),
				"type_name": "动作片", "vod_time": fmt.Sprintf("2026-11-07 10:%02d:00", page),
			}},
		})
	}))
	defer site.Close()

	// 预置「上次进程」遗留的站点与一条进度已保存到第 2 页的 running 记录。
	st, err := repository.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	src := &model.Source{
		Name: "重启恢复站点", APIBase: site.URL + "/api.php/provide/vod/",
		Format: model.FormatMacCMSJSON, Enabled: true, IncrementalHours: 24,
	}
	if err := st.Sources.CreateSource(src); err != nil {
		t.Fatal(err)
	}
	job := &model.CrawlJob{
		SourceID: src.ID, SourceName: src.Name, Mode: model.ModeFull,
		Status: model.JobRunning, StartedAt: time.Now(),
		ResumePage: 2, Pages: 1, Fetched: 1, Attempt: 1,
	}
	if err := st.Jobs.CreateJob(job); err != nil {
		t.Fatal(err)
	}
	interruptedID := job.ID
	// 另放一条用户手动暂停的记录，确认不会被自动恢复。
	paused := &model.CrawlJob{
		SourceID: src.ID, SourceName: src.Name, Mode: model.ModeFull,
		Status: model.JobPaused, StartedAt: time.Now(), ResumePage: 3,
	}
	if err := st.Jobs.CreateJob(paused); err != nil {
		t.Fatal(err)
	}
	pausedID := paused.ID
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// 模拟重启：同一个数据库，重新装配应用。
	cfg := config.Default()
	cfg.Database.Path = dbPath
	cfg.Web.Dir = webDir
	cfg.Collector.RetryTimes = 0
	application, err := app.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()

	loginReq, _ := http.NewRequest(http.MethodPost, "/api/auth/v1/sessions",
		jsonBody(map[string]string{"username": "admin", "password": "admin123456"}))
	loginReq.Header.Set("Content-Type", "application/json")
	lw := httptest.NewRecorder()
	application.Engine.ServeHTTP(lw, loginReq)
	if lw.Code != http.StatusOK {
		t.Fatalf("登录失败: %d %s", lw.Code, lw.Body.String())
	}
	var lr struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(lw.Body.Bytes(), &lr); err != nil || lr.Token == "" {
		t.Fatal("登录响应缺少 token")
	}
	testToken = lr.Token
	defer func() { testToken = "" }()

	r := application.Engine
	got := waitJobStatus(t, r, interruptedID, model.JobSuccess)

	if got.ID != interruptedID {
		t.Fatalf("应复用原记录 %d，实际 %d", interruptedID, got.ID)
	}
	if strings.Contains(got.Message, "服务重启") {
		t.Fatalf("恢复后中断提示应清空，实际 %q", got.Message)
	}
	if got.RetryOf != 0 {
		t.Fatalf("恢复不应标记来源任务，实际 %d", got.RetryOf)
	}
	// 从第 2 页续采 2 页，统计在原记录上累加：1 + 2 = 3。
	if got.Pages != 3 || got.Fetched != 3 {
		t.Fatalf("统计应在原记录上累加（3 页 / 3 条），实际 %d 页 / %d 条", got.Pages, got.Fetched)
	}
	if jobs := listJobs(t, r); len(jobs) != 2 {
		t.Fatalf("重启恢复不应新开任务，期望 2 条记录，实际 %d 条", len(jobs))
	}
	if p := jobByID(t, r, pausedID); p.Status != model.JobPaused {
		t.Fatalf("用户手动暂停的任务不应被自动恢复，实际 %s", p.Status)
	}
	mu.Lock()
	gotPages := append([]int(nil), pages...)
	mu.Unlock()
	if len(gotPages) != 2 || gotPages[0] != 2 || gotPages[1] != 3 {
		t.Fatalf("应从第 2 页续采且只请求 2、3 页，实际 %v", gotPages)
	}
}
