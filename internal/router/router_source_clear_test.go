package router_test

import (
	"net/http"
	"strconv"
	"testing"
)

// TestSourceClearDataAPI 验证「清空站点数据」接口：删除影片 / 分类 / 标签 /
// 采集记录，站点配置保留、采集状态复位；非管理员不可调用，未知站点 404。
func TestSourceClearDataAPI(t *testing.T) {
	r, _ := newTestRouter(t)
	mock := newMockVideoSite(t)
	id := authCreateSource(t, r, testToken, "待清空站", mock.URL+"/")
	path := "/api/sources/v1/" + strconv.Itoa(int(id))

	// 先同步采集一次，让该站点产生影片、分类与采集记录。
	if w := authCall(r, http.MethodPost, path+"/crawls", testToken,
		map[string]string{"mode": "full"}); w.Code != http.StatusOK {
		t.Fatalf("采集失败: %d %s", w.Code, w.Body.String())
	}
	var before struct {
		Total int64 `json:"total"`
	}
	decode(t, authCall(r, http.MethodGet, "/api/videos/v1?source_id="+strconv.Itoa(int(id)), testToken, nil), &before)
	if before.Total != 2 {
		t.Fatalf("采集后应有 2 部影片，实际 %d", before.Total)
	}

	// 普通用户无权清空。
	authCreateUser(t, r, testToken, "viewer", "viewer123456", []uint{id})
	userToken, _ := login(t, r, "viewer", "viewer123456")
	if w := authCall(r, http.MethodDelete, path+"/data", userToken, nil); w.Code != http.StatusForbidden {
		t.Fatalf("普通用户清空站点数据应返回 403，实际 %d %s", w.Code, w.Body.String())
	}

	// 管理员清空：返回各表删除条数。
	w := authCall(r, http.MethodDelete, path+"/data", testToken, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("清空失败: %d %s", w.Code, w.Body.String())
	}
	var res struct {
		Videos     int64 `json:"videos"`
		Categories int64 `json:"categories"`
		Jobs       int64 `json:"jobs"`
	}
	decode(t, w, &res)
	if res.Videos != 2 || res.Categories == 0 || res.Jobs != 1 {
		t.Fatalf("清空回报异常: %+v", res)
	}

	// 数据清空。
	var after struct {
		Total int64 `json:"total"`
	}
	decode(t, authCall(r, http.MethodGet, "/api/videos/v1?source_id="+strconv.Itoa(int(id)), testToken, nil), &after)
	if after.Total != 0 {
		t.Fatalf("清空后影片应为 0，实际 %d", after.Total)
	}
	var jobs []jsonRaw
	decode(t, authCall(r, http.MethodGet, "/api/jobs/v1?source_id="+strconv.Itoa(int(id)), testToken, nil), &jobs)
	if len(jobs) != 0 {
		t.Fatalf("清空后采集记录应为 0，实际 %d", len(jobs))
	}

	// 站点配置保留，采集状态与影片数复位。
	var srcs []struct {
		ID         uint   `json:"id"`
		Name       string `json:"name"`
		APIBase    string `json:"apiBase"`
		Enabled    bool   `json:"enabled"`
		VideoCount int64  `json:"videoCount"`
		LastStatus string `json:"lastStatus"`
		LastCursor string `json:"lastCursor"`
	}
	decode(t, authCall(r, http.MethodGet, "/api/sources/v1", testToken, nil), &srcs)
	if len(srcs) != 1 {
		t.Fatalf("站点配置应保留，实际 %d 个", len(srcs))
	}
	got := srcs[0]
	if got.ID != id || got.Name != "待清空站" || got.APIBase != mock.URL+"/" || !got.Enabled {
		t.Fatalf("站点配置不应被清空改动: %+v", got)
	}
	if got.VideoCount != 0 || got.LastStatus != "" || got.LastCursor != "" {
		t.Fatalf("采集状态与影片数应复位: %+v", got)
	}

	// 未知站点 404。
	if w := authCall(r, http.MethodDelete, "/api/sources/v1/999999/data", testToken, nil); w.Code != http.StatusNotFound {
		t.Fatalf("清空未知站点应返回 404，实际 %d %s", w.Code, w.Body.String())
	}
}

// jsonRaw 用于只关心数组长度的响应解码。
type jsonRaw = map[string]interface{}
