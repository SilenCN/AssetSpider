package router_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SilenCN/AssetSpider/internal/app"
	"github.com/SilenCN/AssetSpider/internal/config"
	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/service"
	"github.com/gin-gonic/gin"
)

// testToken 是上一个测试路由里首个登录者（自动注册为管理员）的令牌。
// 测试串行执行，每个用例都会用独立数据库重建令牌。
var testToken string

// authReq 构造自动携带管理员令牌的请求，让既有接口测试在启用登录校验后仍然有效。
func authReq(method, path string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, path, body)
	if testToken != "" {
		req.Header.Set("Authorization", "Bearer "+testToken)
	}
	return req
}

func newTestRouter(t *testing.T) (*gin.Engine, string) {
	t.Helper()
	return newTestRouterWith(t, nil)
}

// newTestRouterWith 允许用例调整采集器配置（如自动重试次数与退避时长）。
func newTestRouterWith(t *testing.T, tune func(*config.Config)) (*gin.Engine, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "api.db")

	webDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"),
		[]byte("<div id=\"app\">AssetSpider</div>"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Database.Path = dbPath
	cfg.Web.Dir = webDir
	if tune != nil {
		tune(cfg)
	}
	application, err := app.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { application.Close() })

	// 每个测试库都是全新的：先通过登录接口注册首个管理员，
	// 之后本文件里的所有请求由 authReq 自动携带该令牌。
	loginReq, err := http.NewRequest(http.MethodPost, "/api/auth/v1/sessions",
		jsonBody(map[string]string{"username": "admin", "password": "admin123456"}))
	if err != nil {
		t.Fatal(err)
	}
	loginReq.Header.Set("Content-Type", "application/json")
	lw := httptest.NewRecorder()
	application.Engine.ServeHTTP(lw, loginReq)
	if lw.Code != http.StatusOK {
		t.Fatalf("初始化管理员失败: %d %s", lw.Code, lw.Body.String())
	}
	var lr struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(lw.Body.Bytes(), &lr); err != nil || lr.Token == "" {
		t.Fatalf("登录响应缺少 token: %s", lw.Body.String())
	}
	testToken = lr.Token
	return application.Engine, dbPath
}

func jsonBody(v interface{}) *strings.Reader {
	b, _ := json.Marshal(v)
	return strings.NewReader(string(b))
}

func doJSON(r *gin.Engine, method, path string, body interface{}) *httptest.ResponseRecorder {
	req := authReq(method, path, jsonBody(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func newMockVideoSite(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 1, "msg": "数据列表", "page": 1, "pagecount": 1,
			"limit": "20", "total": 2,
			"list": []map[string]interface{}{
				{"vod_id": 101, "vod_name": "测试影片A", "type_name": "动作片",
					"vod_area": "大陆", "vod_score": "8.5", "vod_year": "2024",
					"vod_time": "2026-11-07 10:00:00", "vod_play_url": "https://x/a.m3u8"},
				{"vod_id": "102", "vod_name": "测试影片B", "type_name": "喜剧片",
					"vod_area": "美国", "vod_score": "9.1", "vod_year": "2025",
					"vod_time": "2026-11-07 11:00:00"},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// lastJob 读取最近一条采集记录。
func lastJob(t *testing.T, r *gin.Engine) model.CrawlJob {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/jobs/v1?limit=1", nil))
	var jobs []model.CrawlJob
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &jobs) != nil || len(jobs) != 1 {
		t.Fatalf("采集记录异常: %d %s", w.Code, w.Body.String())
	}
	return jobs[0]
}

// liveOf 读取采集记录接口里某条记录的实时进度（任务不在运行时为 nil）。
func liveOf(t *testing.T, r *gin.Engine, id uint) *service.TaskView {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/jobs/v1?limit=50", nil))
	var jobs []service.JobView
	if err := json.Unmarshal(w.Body.Bytes(), &jobs); err != nil {
		t.Fatalf("采集记录解析失败: %v", err)
	}
	for _, j := range jobs {
		if j.ID == id {
			return j.Live
		}
	}
	return nil
}

// listJobs 读取全部采集记录（测试里用来断言任务条数）。
func listJobs(t *testing.T, r *gin.Engine) []model.CrawlJob {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/jobs/v1?limit=50", nil))
	var jobs []model.CrawlJob
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &jobs) != nil {
		t.Fatalf("采集记录异常: %d %s", w.Code, w.Body.String())
	}
	return jobs
}

// jobByID 读取指定的采集记录（任务结束前后都可读，运行中会带上 live 字段）。
func jobByID(t *testing.T, r *gin.Engine, id uint) model.CrawlJob {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/jobs/v1?limit=50", nil))
	var jobs []model.CrawlJob
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &jobs) != nil {
		t.Fatalf("采集记录异常: %d %s", w.Code, w.Body.String())
	}
	for _, j := range jobs {
		if j.ID == id {
			return j
		}
	}
	t.Fatalf("采集记录中找不到任务 %d", id)
	return model.CrawlJob{}
}

// waitJobStatus 轮询采集记录，直到任务进入指定状态。
func waitJobStatus(t *testing.T, r *gin.Engine, id uint, want string) model.CrawlJob {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		job := jobByID(t, r, id)
		if job.Status == want {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("任务 %d 未在超时内进入 %s，当前 %s（%s）", id, want, job.Status, job.Message)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func taskViews(r *gin.Engine) []service.TaskView {
	w := httptest.NewRecorder()
	r.ServeHTTP(w, authReq(http.MethodGet, "/api/tasks/v1", nil))
	var out struct {
		Items []service.TaskView `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return out.Items
}

// TaskViewOf 从任务列表中取出指定任务。
func taskViewOf(r *gin.Engine, jobID uint) (service.TaskView, bool) {
	for _, it := range taskViews(r) {
		if it.JobID == jobID {
			return it, true
		}
	}
	return service.TaskView{}, false
}
