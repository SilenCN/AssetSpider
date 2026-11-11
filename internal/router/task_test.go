package router_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/model"
)

func TestTaskPauseResumeAndCapacity(t *testing.T) {
	r, _ := newTestRouter(t)

	// 慢速分页站点：每页 1 条、共 40 页，每页 sleep 50ms，
	// 保证「暂停」一定发生在采集中途，而不是采集结束后。
	var mu sync.Mutex
	pageHits := 0
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		page, _ := strconv.Atoi(req.URL.Query().Get("pg"))
		if page < 1 {
			page = 1
		}
		mu.Lock()
		pageHits++
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "ok", "page": page, "pagecount": 40, "limit": "1", "total": 40,
			"list": []map[string]interface{}{{
				"vod_id": 1000 + page, "vod_name": fmt.Sprintf("暂停测试%d", page),
				"type_name": "动作片", "vod_area": "大陆", "vod_score": "8.0",
				"vod_time": fmt.Sprintf("2026-11-07 10:%02d:00", page%60),
			}},
		})
	}))
	defer site.Close()

	if w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "慢速站点", "apiBase": site.URL + "/api.php/provide/vod/",
		"format": "maccms_json", "enabled": true, "incrementalHours": 24,
	}); w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}

	// ---- 并发上限 ----
	w := doJSON(r, http.MethodPut, "/api/tasks/v1/capacity", map[string]int{"maxConcurrent": 2})
	var cap struct {
		MaxConcurrent int `json:"maxConcurrent"`
		Running       int `json:"running"`
		Queued        int `json:"queued"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &cap) != nil || cap.MaxConcurrent != 2 {
		t.Fatalf("调整并发上限失败: %d %s", w.Code, w.Body.String())
	}
	if w := doJSON(r, http.MethodPut, "/api/tasks/v1/capacity", map[string]int{"maxConcurrent": 0}); w.Code != http.StatusBadRequest {
		t.Fatalf("非法并发上限应返回 400，实际 %d", w.Code)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/tasks/v1/capacity", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"maxConcurrent":2`) {
		t.Fatalf("读取并发上限异常: %d %s", w.Code, w.Body.String())
	}

	// ---- 异步入队 ----
	w = doJSON(r, http.MethodPost, "/api/tasks/v1", map[string]interface{}{"sourceId": 1, "mode": "full"})
	var created struct {
		JobID  uint   `json:"jobId"`
		Status string `json:"status"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.JobID == 0 {
		t.Fatalf("创建任务异常: %d %s", w.Code, w.Body.String())
	}

	// 同站点的第二个任务不再冲突，而是排队等待（这里不发起第二条，
	// 否则它会在这条用例后续「暂停 / 续采」的断言里抢占站点）。
	// 排队行为见 service 层的 TestSameSourceTasksQueueInsteadOfReject。
	// 不存在的任务
	if w := doJSON(r, http.MethodPut, "/api/tasks/v1/999999",
		map[string]string{"status": "paused"}); w.Code != http.StatusNotFound {
		t.Fatalf("暂停未知任务应返回 404，实际 %d", w.Code)
	}

	type taskItem struct {
		JobID  uint   `json:"jobId"`
		Status string `json:"status"`
		Pages  int    `json:"pages"`
	}
	type taskList struct {
		Items []taskItem `json:"items"`
	}
	listTasks := func() taskList {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, authReq(http.MethodGet, "/api/tasks/v1", nil))
		var out taskList
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("任务列表异常: %d %s", w.Code, w.Body.String())
		}
		return out
	}
	find := func(list taskList, jobID uint) (taskItem, bool) {
		for _, it := range list.Items {
			if it.JobID == jobID {
				return it, true
			}
		}
		return taskItem{}, false
	}
	waitFor := func(desc string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(6 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("等待超时：%s", desc)
			}
			time.Sleep(30 * time.Millisecond)
		}
	}

	// 等到真正开跑且已完成至少 1 页
	waitFor("任务进入运行态且有进度", func() bool {
		it, ok := find(listTasks(), created.JobID)
		return ok && it.Status == model.JobRunning && it.Pages >= 1
	})

	// ---- 暂停 ----
	w = doJSON(r, http.MethodPut, "/api/tasks/v1/"+strconv.Itoa(int(created.JobID)),
		map[string]string{"status": "paused"})
	if w.Code != http.StatusOK {
		t.Fatalf("暂停任务失败: %d %s", w.Code, w.Body.String())
	}
	// 任务应在暂停后退出运行态（从任务表中移除）
	waitFor("暂停后任务结束", func() bool {
		_, ok := find(listTasks(), created.JobID)
		return !ok
	})

	mu.Lock()
	hitsAtPause := pageHits
	mu.Unlock()
	if hitsAtPause >= 40 {
		t.Fatalf("暂停过晚，站点已被完整采集（%d 页）", hitsAtPause)
	}

	firstPaused := lastJob(t, r)
	if firstPaused.Status != model.JobPaused || firstPaused.ResumePage <= 1 {
		t.Fatalf("暂停后应记录续采页码: %+v", firstPaused)
	}
	if firstPaused.Pages < 1 {
		t.Fatalf("暂停时应保留已完成的页数: %+v", firstPaused)
	}

	// ---- 继续：从暂停页码续采 ----
	w = doJSON(r, http.MethodPut, "/api/tasks/v1/"+strconv.Itoa(int(firstPaused.ID)),
		map[string]string{"status": "running"})
	var resumed struct {
		JobID uint `json:"jobId"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &resumed) != nil || resumed.JobID == 0 {
		t.Fatalf("继续任务失败: %d %s", w.Code, w.Body.String())
	}
	if resumed.JobID != firstPaused.ID {
		t.Fatalf("继续任务应复用原记录 %d，实际新开了 %d", firstPaused.ID, resumed.JobID)
	}
	// 复用原记录后续采视图一开始就带着上一轮的累计统计，不能只看 Pages>=1，
	// 否则会在还没真正采到新页面时就暂停（resume_page 不会前进）。
	var resumedView struct {
		Pages   int `json:"pages"`
		Fetched int `json:"fetched"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resumedView)
	if resumedView.Pages < firstPaused.Pages || resumedView.Fetched < firstPaused.Fetched {
		t.Fatalf("继续采集中应沿用原记录的累计统计：记录 %d 页/%d 条，视图 %d 页/%d 条",
			firstPaused.Pages, firstPaused.Fetched, resumedView.Pages, resumedView.Fetched)
	}
	waitFor("续采任务进入运行态并采到新页面", func() bool {
		it, ok := find(listTasks(), resumed.JobID)
		return ok && it.Status == model.JobRunning && it.Pages > firstPaused.Pages
	})
	if w := doJSON(r, http.MethodPut, "/api/tasks/v1/"+strconv.Itoa(int(resumed.JobID)),
		map[string]string{"status": "paused"}); w.Code != http.StatusOK {
		t.Fatalf("再次暂停失败: %d %s", w.Code, w.Body.String())
	}
	waitFor("续采任务结束", func() bool {
		_, ok := find(listTasks(), resumed.JobID)
		return !ok
	})

	secondPaused := lastJob(t, r)
	if secondPaused.ID != firstPaused.ID {
		t.Fatalf("继续采集不应新开记录：期望 #%d，实际 #%d", firstPaused.ID, secondPaused.ID)
	}
	if secondPaused.Status != model.JobPaused {
		t.Fatalf("续采后暂停状态异常: %+v", secondPaused)
	}
	if secondPaused.ResumePage <= firstPaused.ResumePage {
		t.Fatalf("续采应从暂停页码继续：首次 %d → 续采 %d",
			firstPaused.ResumePage, secondPaused.ResumePage)
	}
}

// TestTaskAutoRetryOnFailure 验证失败自动重试：站点第一次请求失败，
// 后台按退避自动重试一次后成功，采集记录标记为「第 2 次尝试成功」。
func TestTaskAutoRetryOnFailure(t *testing.T) {
	r, _ := newTestRouterWith(t, func(cfg *config.Config) {
		cfg.Collector.RetryTimes = 1
		cfg.Collector.RetryBackoffSeconds = 1
	})

	var mu sync.Mutex
	hits := 0
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// 采集还会额外请求 ac=typelist 补全父级分类名称；它失败不影响采集，
		// 这里直接返回空列表（不计入失败注入逻辑）。
		if req.URL.Query().Get("ac") == "typelist" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 1, "msg": "ok", "page": 1, "pagecount": 1, "limit": "1", "total": 0,
				"list": []interface{}{},
			})
			return
		}
		mu.Lock()
		hits++
		n := hits
		mu.Unlock()
		if n == 1 {
			http.Error(w, "upstream boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "ok", "page": 1, "pagecount": 1, "limit": "1", "total": 1,
			"list": []map[string]interface{}{{
				"vod_id": 3001, "vod_name": "自动重试影片", "type_name": "动作片",
				"vod_area": "大陆", "vod_time": "2026-11-07 10:00:00",
			}},
		})
	}))
	defer site.Close()

	if w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "重试站点", "apiBase": site.URL + "/api.php/provide/vod/",
		"format": "maccms_json", "enabled": true, "incrementalHours": 24,
	}); w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}

	w := doJSON(r, http.MethodPost, "/api/tasks/v1", map[string]interface{}{"sourceId": 1, "mode": "full"})
	var created struct {
		JobID uint `json:"jobId"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &created) != nil || created.JobID == 0 {
		t.Fatalf("创建任务异常: %d %s", w.Code, w.Body.String())
	}

	// 等待重试期间，任务列表应能看到倒计时与失败原因
	sawRetryWait := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := taskViewOf(r, created.JobID); ok && v.RetryInSeconds > 0 {
			sawRetryWait = true
			if v.Attempt != 1 || v.MaxAttempts != 2 {
				t.Fatalf("重试期间尝试次数异常: %+v", v)
			}
			if !strings.Contains(v.Message, "自动重试") {
				t.Fatalf("重试期间应说明自动重试: %+v", v)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawRetryWait {
		t.Fatalf("未观察到「等待自动重试」的任务状态")
	}

	job := waitJobStatus(t, r, created.JobID, model.JobSuccess)
	if job.Attempt != 2 {
		t.Fatalf("自动重试后应记录 2 次尝试，实际 %d（%s）", job.Attempt, job.Message)
	}
	if job.New != 1 || job.Fetched != 1 {
		t.Fatalf("重试成功后应正常入库: %+v", job)
	}
}

// TestTaskManualRetryAndRerun 验证手动重试（从失败页续采）与重新运行（从第 1 页）。
func TestTaskManualRetryAndRerun(t *testing.T) {
	r, _ := newTestRouterWith(t, func(cfg *config.Config) {
		cfg.Collector.RetryTimes = 0 // 关闭自动重试，专心验证手动动作
	})

	var mu sync.Mutex
	pages := []int{}
	failFrom := 2 // 第 2 页起返回 500
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
		fail := failFrom > 0 && page >= failFrom
		mu.Unlock()
		if fail {
			http.Error(w, "upstream boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "ok", "page": page, "pagecount": 3, "limit": "1", "total": 3,
			"list": []map[string]interface{}{{
				"vod_id": 4000 + page, "vod_name": fmt.Sprintf("重试影片%d", page),
				"type_name": "动作片", "vod_area": "大陆",
				"vod_time": fmt.Sprintf("2026-11-07 10:%02d:00", page),
			}},
		})
	}))
	defer site.Close()

	if w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "手动重试站点", "apiBase": site.URL + "/api.php/provide/vod/",
		"format": "maccms_json", "enabled": true, "incrementalHours": 24,
	}); w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}

	// ---- 首次采集：第 2 页失败 ----
	w := doJSON(r, http.MethodPost, "/api/tasks/v1", map[string]interface{}{"sourceId": 1, "mode": "full"})
	var first struct {
		JobID uint `json:"jobId"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &first) != nil {
		t.Fatalf("创建任务异常: %d %s", w.Code, w.Body.String())
	}
	failed := waitJobStatus(t, r, first.JobID, model.JobFailed)
	if failed.ResumePage != 2 {
		t.Fatalf("失败任务的续采页码应为失败页 2，实际 %d", failed.ResumePage)
	}
	if failed.Pages != 1 {
		t.Fatalf("失败前应只完成 1 页，实际 %d", failed.Pages)
	}
	if failed.Attempt != 1 {
		t.Fatalf("关闭自动重试后尝试次数应为 1，实际 %d", failed.Attempt)
	}

	// ---- 手动重试：站点恢复，从失败页续采 ----
	mu.Lock()
	failFrom = 0
	pages = nil
	mu.Unlock()

	w = doJSON(r, http.MethodPost, "/api/tasks/v1/"+strconv.Itoa(int(first.JobID))+"/retry", nil)
	var retried struct {
		JobID   uint `json:"jobId"`
		Attempt int  `json:"attempt"`
		Pages   int  `json:"pages"`
		Fetched int  `json:"fetched"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &retried) != nil || retried.JobID == 0 {
		t.Fatalf("重试任务失败: %d %s", w.Code, w.Body.String())
	}
	if retried.JobID != first.JobID {
		t.Fatalf("重试应复用原任务记录 %d，实际新开了 %d", first.JobID, retried.JobID)
	}
	if retried.Attempt != 2 {
		t.Fatalf("重试后应接着原任务的尝试次数（2），实际 %d", retried.Attempt)
	}
	// 重试入队时就要带上原任务已累计的统计，页面不会闪回 0。
	if retried.Pages != 1 || retried.Fetched != 1 {
		t.Fatalf("重试视图应带原任务累计统计（1 页/1 条），实际 %d 页/%d 条", retried.Pages, retried.Fetched)
	}
	retryJob := waitJobStatus(t, r, first.JobID, model.JobSuccess)
	if retryJob.RetryOf != 0 {
		t.Fatalf("重试复用原记录，不应再标记来源任务，实际 %d", retryJob.RetryOf)
	}
	// 统计与页数在原任务基础上累加：失败前已采 1 页 1 条，重试再采 2 页 2 条。
	if retryJob.Pages != 3 {
		t.Fatalf("重试后页数应累计为 3，实际 %d", retryJob.Pages)
	}
	if retryJob.Fetched != 3 || retryJob.New != 3 {
		t.Fatalf("重试后拉取/新增应累计为 3/3，实际 %d/%d", retryJob.Fetched, retryJob.New)
	}
	if retryJob.Attempt != 2 {
		t.Fatalf("重试后尝试次数应累计为 2，实际 %d", retryJob.Attempt)
	}
	// 最终只有一条任务记录
	if jobs := listJobs(t, r); len(jobs) != 1 {
		t.Fatalf("手动重试不应新开任务，期望 1 条记录，实际 %d 条", len(jobs))
	}
	mu.Lock()
	firstPage := -1
	if len(pages) > 0 {
		firstPage = pages[0]
	}
	mu.Unlock()
	if firstPage != 2 {
		t.Fatalf("重试应从失败页 2 继续，实际首请求页 %d", firstPage)
	}

	// 成功的任务不允许再「重试」
	if w := doJSON(r, http.MethodPost,
		"/api/tasks/v1/"+strconv.Itoa(int(retried.JobID))+"/retry", nil); w.Code != http.StatusConflict {
		t.Fatalf("对成功任务重试应返回 409，实际 %d %s", w.Code, w.Body.String())
	}

	// ---- 重新运行：从第 1 页重新采集 ----
	mu.Lock()
	pages = nil
	mu.Unlock()
	w = doJSON(r, http.MethodPost, "/api/tasks/v1/"+strconv.Itoa(int(retried.JobID))+"/rerun", nil)
	var rerun struct {
		JobID uint `json:"jobId"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &rerun) != nil || rerun.JobID == 0 {
		t.Fatalf("重新运行失败: %d %s", w.Code, w.Body.String())
	}
	if rerun.JobID != retried.JobID {
		t.Fatalf("重新运行应复用原记录 %d，实际新开了 %d", retried.JobID, rerun.JobID)
	}
	rerunJob := waitJobStatus(t, r, rerun.JobID, model.JobSuccess)
	mu.Lock()
	rerunFirstPage := -1
	if len(pages) > 0 {
		rerunFirstPage = pages[0]
	}
	mu.Unlock()
	if rerunFirstPage != 1 {
		t.Fatalf("重新运行应从第 1 页开始，实际首请求页 %d", rerunFirstPage)
	}
	if rerunJob.RetryOf != 0 {
		t.Fatalf("复用原记录时不应标记来源任务，实际 %d", rerunJob.RetryOf)
	}
	// 统计在重新运行前被清零，最后只剩这一轮的 3 页 3 条。
	if rerunJob.Pages != 3 || rerunJob.Fetched != 3 {
		t.Fatalf("重新运行应完整采集 3 页 3 条，实际 %d 页 / %d 条", rerunJob.Pages, rerunJob.Fetched)
	}
	if rerunJob.Attempt != 1 {
		t.Fatalf("重新运行应把尝试次数重新计数（1），实际 %d", rerunJob.Attempt)
	}
	if jobs := listJobs(t, r); len(jobs) != 1 {
		t.Fatalf("重新运行不应新开任务，期望 1 条记录，实际 %d 条", len(jobs))
	}

	// 未知任务的各动作都返回 404
	for _, action := range []string{"retry", "rerun"} {
		if w := doJSON(r, http.MethodPost, "/api/tasks/v1/999999/"+action, nil); w.Code != http.StatusNotFound {
			t.Fatalf("%s 未知任务应返回 404，实际 %d", action, w.Code)
		}
	}
}

// TestTaskLiveProgress 验证任务列表的实时进度：页数、总页数与百分比。
func TestTaskLiveProgress(t *testing.T) {
	r, _ := newTestRouterWith(t, func(cfg *config.Config) { cfg.Collector.RetryTimes = 0 })

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		page, _ := strconv.Atoi(req.URL.Query().Get("pg"))
		if page < 1 {
			page = 1
		}
		time.Sleep(40 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "ok", "page": page, "pagecount": 10, "limit": "1", "total": 10,
			"list": []map[string]interface{}{{
				"vod_id": 5000 + page, "vod_name": fmt.Sprintf("进度影片%d", page),
				"type_name": "动作片", "vod_area": "大陆",
				"vod_time": fmt.Sprintf("2026-11-07 10:%02d:00", page),
			}},
		})
	}))
	defer site.Close()

	if w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "进度站点", "apiBase": site.URL + "/api.php/provide/vod/",
		"format": "maccms_json", "enabled": true, "incrementalHours": 24,
	}); w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}
	w := doJSON(r, http.MethodPost, "/api/tasks/v1", map[string]interface{}{"sourceId": 1, "mode": "full"})
	var created struct {
		JobID uint `json:"jobId"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatalf("创建任务异常: %d %s", w.Code, w.Body.String())
	}

	// 中途应能看到 0 < 百分比 < 100 且带总页数
	sawProgress := false
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) && !sawProgress {
		if v, ok := taskViewOf(r, created.JobID); ok {
			if v.Status == model.JobRunning && v.PageCount == 10 && v.Page >= 2 && v.Page < 10 {
				if v.Percent <= 0 || v.Percent >= 100 {
					t.Fatalf("运行中百分比应在 0..100 之间: %+v", v)
				}
				if v.ItemsPerSecond <= 0 {
					t.Fatalf("运行中应能算出采集速率: %+v", v)
				}
				// 拉取 / 新增 / 更新 / 未变也要随任务实时累计，且四者自洽。
				if v.Fetched < v.Pages {
					t.Fatalf("运行中拉取条数应随分页推进: %+v", v)
				}
				if v.Fetched != v.New+v.Updated+v.Unchanged {
					t.Fatalf("运行中拉取应等于新增+更新+未变: %+v", v)
				}
				// 采集记录的 live 也必须是同一份实时统计
				if lv := liveOf(t, r, created.JobID); lv == nil || lv.Fetched < v.Fetched {
					t.Fatalf("采集记录实时统计落后: live=%+v task=%+v", lv, v)
				}
				sawProgress = true
			}
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !sawProgress {
		t.Fatalf("未观察到运行中的分页进度")
	}

	job := waitJobStatus(t, r, created.JobID, model.JobSuccess)
	if job.Pages != 10 {
		t.Fatalf("应完整采集 10 页，实际 %d", job.Pages)
	}
	// 任务结束后不应再留在任务列表里
	if _, ok := taskViewOf(r, created.JobID); ok {
		t.Fatalf("已结束的任务不应出现在运行任务列表中")
	}
	// 采集记录里的 live 字段应为空
	w = httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/jobs/v1?limit=1", nil))
	if strings.Contains(w.Body.String(), `"live"`) {
		t.Fatalf("已结束任务的采集记录不应带 live 进度: %s", w.Body.String())
	}
}

// TestTaskPauseDuringRetryWait 验证「等待自动重试」期间也能被手动暂停：
// 暂停应立刻打断退避等待，任务记为已暂停而不是等到重试结束。
func TestTaskPauseDuringRetryWait(t *testing.T) {
	r, _ := newTestRouterWith(t, func(cfg *config.Config) {
		cfg.Collector.RetryTimes = 3
		cfg.Collector.RetryBackoffSeconds = 5 // 退避足够长，便于在中途暂停
	})

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "always boom", http.StatusInternalServerError)
	}))
	defer site.Close()

	if w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "长退避站点", "apiBase": site.URL + "/api.php/provide/vod/",
		"format": "maccms_json", "enabled": true, "incrementalHours": 24,
	}); w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}
	w := doJSON(r, http.MethodPost, "/api/tasks/v1", map[string]interface{}{"sourceId": 1, "mode": "full"})
	var created struct {
		JobID uint `json:"jobId"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatalf("创建任务异常: %d %s", w.Code, w.Body.String())
	}

	deadline := time.Now().Add(4 * time.Second)
	for {
		if v, ok := taskViewOf(r, created.JobID); ok && v.RetryInSeconds > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("未进入等待自动重试状态")
		}
		time.Sleep(20 * time.Millisecond)
	}

	start := time.Now()
	if w := doJSON(r, http.MethodPut, "/api/tasks/v1/"+strconv.Itoa(int(created.JobID)),
		map[string]string{"status": "paused"}); w.Code != http.StatusOK {
		t.Fatalf("暂停失败: %d %s", w.Code, w.Body.String())
	}
	job := waitJobStatus(t, r, created.JobID, model.JobPaused)
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("暂停应打断退避等待，实际耗时 %v", took)
	}
	if !strings.Contains(job.Message, "已暂停") {
		t.Fatalf("暂停说明异常: %s", job.Message)
	}
}

// TestTaskProgressPersistedWhileRunning 验证运行中的任务会定期把进度写库：
// 采集记录里能看到已完成的页数、统计与续采页，即使进程被杀也不会丢进度。
func TestTaskProgressPersistedWhileRunning(t *testing.T) {
	r, _ := newTestRouterWith(t, func(cfg *config.Config) {
		cfg.Collector.RetryTimes = 0
		cfg.Collector.ProgressSaveSeconds = 1 // 测试里把落库间隔压到 1 秒
	})

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		page, _ := strconv.Atoi(req.URL.Query().Get("pg"))
		if page < 1 {
			page = 1
		}
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "ok", "page": page, "pagecount": 10, "limit": "1", "total": 10,
			"list": []map[string]interface{}{{
				"vod_id": 7000 + page, "vod_name": fmt.Sprintf("落库影片%d", page),
				"type_name": "动作片", "vod_area": "大陆",
				"vod_time": fmt.Sprintf("2026-11-08 10:%02d:00", page),
			}},
		})
	}))
	defer site.Close()

	if w := doJSON(r, http.MethodPost, "/api/sources/v1", map[string]interface{}{
		"name": "落库站点", "apiBase": site.URL + "/api.php/provide/vod/",
		"format": "maccms_json", "enabled": true, "incrementalHours": 24,
	}); w.Code != http.StatusCreated {
		t.Fatalf("创建站点失败: %d %s", w.Code, w.Body.String())
	}
	w := doJSON(r, http.MethodPost, "/api/tasks/v1", map[string]interface{}{"sourceId": 1, "mode": "full"})
	var created struct {
		JobID uint `json:"jobId"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatalf("创建任务异常: %d %s", w.Code, w.Body.String())
	}

	// 运行中等待第一次落库：采集记录（落库值，非 live）里应出现已完成页数。
	var persisted model.CrawlJob
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		job := jobByID(t, r, created.JobID)
		if job.Status == model.JobRunning && job.Pages >= 2 {
			persisted = job
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if persisted.Pages == 0 {
		t.Fatalf("运行中未把进度写入采集记录（pages 仍为 0）")
	}
	if persisted.Fetched == 0 || persisted.Total != 10 {
		t.Fatalf("落库的统计不完整: %+v", persisted)
	}
	// 续采页应指向「已完成页 + 1」，重启后重试即可从这里继续。
	if persisted.ResumePage != persisted.Pages+1 {
		t.Fatalf("落库的续采页应为 %d，实际 %d", persisted.Pages+1, persisted.ResumePage)
	}

	job := waitJobStatus(t, r, created.JobID, model.JobSuccess)
	if job.Pages != 10 || job.Fetched != 10 {
		t.Fatalf("任务结束统计异常: %+v", job)
	}
	if job.ResumePage != 1 {
		t.Fatalf("任务正常结束后续采页应回到 1，实际 %d", job.ResumePage)
	}
}

// TestSourcePlayRewriteAPI 验证站点的「视频解析地址 / 视频播放域名」配置：
// 入库保存原始地址，读取影片列表与播放组时按配置改写（先换域名，再拼解析地址）。
