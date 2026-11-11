package controller

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/SilenCN/AssetSpider/internal/service"
	"github.com/SilenCN/AssetSpider/internal/utils"
	"github.com/gin-gonic/gin"
)

type SourceController struct {
	svc *service.SourceService
}

func NewSourceController(svc *service.SourceService) *SourceController {
	return &SourceController{svc: svc}
}

func (s *SourceController) List(c *gin.Context) {
	out, err := s.svc.List(principalOf(c))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (s *SourceController) Create(c *gin.Context) {
	var in service.SourceInput
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, err.Error())
		return
	}
	// 新增站点默认不自动采集；前端「新增站点」会带 autostart=1 让站点落库后
	// 立即跑一次全量，方便用户马上看到数据。导入 / API 直连不传则保持手动。
	created, err := s.svc.Create(in, c.Query("autostart") == "1")
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusCreated, created)
}

func (s *SourceController) Update(c *gin.Context) {
	id, err := utils.ParseUintParam(c.Param("id"))
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	var in service.SourceInput
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, err.Error())
		return
	}
	updated, err := s.svc.Update(id, in)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, updated)
}

func (s *SourceController) Delete(c *gin.Context) {
	id, err := utils.ParseUintParam(c.Param("id"))
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	if err := s.svc.Delete(id); err != nil {
		writeError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Clear 清空站点数据（影片、分类、标签、采集记录），站点配置保留。
func (s *SourceController) Clear(c *gin.Context) {
	id, err := utils.ParseUintParam(c.Param("id"))
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	res, err := s.svc.Clear(id)
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// Export 把当前主体可见的站点配置以 JSON 附件形式导出。
func (s *SourceController) Export(c *gin.Context) {
	bundle, err := s.svc.Export(principalOf(c))
	if err != nil {
		writeError(c, err)
		return
	}
	filename := "assetspider-sources-" + time.Now().Format("20060102-150405") + ".json"
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.JSON(http.StatusOK, bundle)
}

const maxImportBytes = 2 << 20 // 2 MiB，足够容纳上千条站点配置

// Import 接收 JSON 文件原文，支持导出文件（{version, sources:[...]}）、
// 裸数组以及单条站点对象三种形态，逐条交给 service 做校验与 upsert。
func (s *SourceController) Import(c *gin.Context) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxImportBytes+1))
	if err != nil {
		badRequest(c, "读取导入内容失败："+err.Error())
		return
	}
	if len(body) > maxImportBytes {
		badRequest(c, "导入文件过大（上限 2 MB）")
		return
	}
	items, err := decodeSourceBundle(body)
	if err != nil {
		badRequest(c, err.Error())
		return
	}
	result, err := s.svc.Import(items, principalOf(c))
	if err != nil {
		writeError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func decodeSourceBundle(body []byte) ([]service.SourceInput, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, fmt.Errorf("导入内容为空，请选择 JSON 文件")
	}
	if body[0] == '{' {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(body, &probe); err != nil {
			return nil, fmt.Errorf("JSON 解析失败：%v", err)
		}
		if raw, ok := probe["sources"]; ok {
			var items []service.SourceInput
			if err := json.Unmarshal(raw, &items); err != nil {
				return nil, fmt.Errorf("sources 字段不是合法的站点数组：%v", err)
			}
			return items, nil
		}
		// 也允许直接贴一条站点对象。
		var one service.SourceInput
		if err := json.Unmarshal(body, &one); err != nil {
			return nil, fmt.Errorf("JSON 解析失败：%v", err)
		}
		return []service.SourceInput{one}, nil
	}
	var items []service.SourceInput
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("JSON 解析失败（应为站点数组、单条对象或含 sources 的导出文件）：%v", err)
	}
	return items, nil
}
