package server

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// sourceInput 是站点配置中允许前端提交的字段。
type sourceInput struct {
	Name             string `json:"name"`
	APIBase          string `json:"apiBase"`
	Format           string `json:"format"`
	ExtraParams      string `json:"extraParams"`
	Enabled          *bool  `json:"enabled"`
	IncrementalHours int    `json:"incrementalHours"`
	MaxPages         int    `json:"maxPages"`
}

func (in sourceInput) validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return errors.New("站点名称不能为空")
	}
	raw := strings.TrimSpace(in.APIBase)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("接口地址需为完整的 http(s) URL，例如 https://site.com/api.php/provide/vod/")
	}
	if f := strings.TrimSpace(in.Format); f != "" && f != "seacms_json" {
		return errors.New("暂不支持的采集格式：" + f)
	}
	if in.IncrementalHours < 0 || in.MaxPages < 0 {
		return errors.New("增量小时数与最大页数不能为负")
	}
	return nil
}

func (in sourceInput) apply(dst *model.Source) *model.Source {
	dst.Name = strings.TrimSpace(in.Name)
	dst.APIBase = strings.TrimSpace(in.APIBase)
	dst.Format = strings.TrimSpace(in.Format)
	if dst.Format == "" {
		dst.Format = "seacms_json"
	}
	dst.ExtraParams = strings.TrimSpace(in.ExtraParams)
	if in.Enabled != nil {
		dst.Enabled = *in.Enabled
	} else if dst.ID == 0 {
		dst.Enabled = true // 新建时默认启用
	}
	dst.IncrementalHours = in.IncrementalHours
	dst.MaxPages = in.MaxPages
	return dst
}

// sourceView 在站点配置上附加该站已入库的影片数量。
type sourceView struct {
	model.Source
	VideoCount int64 `json:"videoCount"`
}

func (s *Server) sourceViews(c *gin.Context) ([]sourceView, bool) {
	srcs, err := s.store.ListSources()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return nil, false
	}
	counts, err := s.store.CountVideosBySource()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return nil, false
	}
	out := make([]sourceView, 0, len(srcs))
	for _, src := range srcs {
		out = append(out, sourceView{Source: src, VideoCount: counts[src.ID]})
	}
	return out, true
}

func (s *Server) listSources(c *gin.Context) {
	out, ok := s.sourceViews(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) createSource(c *gin.Context) {
	var in sourceInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := in.validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	src := in.apply(&model.Source{})
	if err := s.store.CreateSource(src); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, src)
}

func (s *Server) updateSource(c *gin.Context) {
	id, err := parseUintParam(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	existing, err := s.store.GetSource(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "站点不存在"})
		return
	}
	var in sourceInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := in.validate(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updated, err := s.store.UpdateSource(id, in.apply(existing))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, updated)
}

func (s *Server) deleteSource(c *gin.Context) {
	id, err := parseUintParam(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := s.store.DeleteSource(id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// parseMode 从 query 或 JSON body 读取采集模式。
func parseMode(c *gin.Context) (string, bool) {
	mode := c.Query("mode")
	if mode == "" {
		var body struct {
			Mode string `json:"mode"`
		}
		_ = c.ShouldBindJSON(&body)
		mode = body.Mode
	}
	if mode == "" {
		mode = model.ModeIncremental
	}
	if mode != model.ModeFull && mode != model.ModeIncremental {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mode 仅支持 full / incremental"})
		return "", false
	}
	return mode, true
}

func (s *Server) crawlSource(c *gin.Context) {
	id, err := parseUintParam(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	mode, ok := parseMode(c)
	if !ok {
		return
	}
	if _, err := s.store.GetSource(id); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "站点不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	res, err := s.runner.RunOne(c.Request.Context(), id, mode)
	if err != nil {
		code := http.StatusBadGateway
		if strings.Contains(err.Error(), "正在采集中") {
			code = http.StatusConflict
		}
		c.JSON(code, gin.H{"error": err.Error(), "result": res})
		return
	}
	c.JSON(http.StatusOK, res)
}

func (s *Server) crawlAll(c *gin.Context) {
	mode, ok := parseMode(c)
	if !ok {
		return
	}
	results, err := s.runner.RunAll(c.Request.Context(), mode)
	body := gin.H{"mode": mode, "results": results}
	if err != nil {
		body["error"] = err.Error()
	}
	c.JSON(http.StatusOK, body)
}

func (s *Server) listVideos(c *gin.Context) {
	sourceID, _ := strconv.ParseUint(c.Query("source_id"), 10, 64)
	page, _ := strconv.Atoi(c.Query("page"))
	size, _ := strconv.Atoi(c.Query("size"))
	items, total, err := s.store.ListVideos(uint(sourceID), strings.TrimSpace(c.Query("q")), page, size)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if size < 1 || size > 200 {
		size = 20
	}
	if page < 1 {
		page = 1
	}
	c.JSON(http.StatusOK, gin.H{"items": items, "total": total, "page": page, "size": size})
}

func (s *Server) listJobs(c *gin.Context) {
	sourceID, _ := strconv.ParseUint(c.Query("source_id"), 10, 64)
	limit, _ := strconv.Atoi(c.Query("limit"))
	items, err := s.store.ListJobs(uint(sourceID), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, items)
}

func parseUintParam(raw string) (uint, error) {
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || n == 0 {
		return 0, errors.New("无效的 id")
	}
	return uint(n), nil
}
