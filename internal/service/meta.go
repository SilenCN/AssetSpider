package service

import "github.com/SilenCN/AssetSpider/internal/model"

// FieldMetaResult 是前端详情展示所需的全字段元数据。
type FieldMetaResult struct {
	Groups interface{}          `json:"groups"`
	Fields []model.VodFieldMeta `json:"fields"`
	Total  int                  `json:"total"`
}

// MetaService 提供静态的字段元数据，不访问数据库。
type MetaService struct{}

func NewMetaService() *MetaService { return &MetaService{} }

func (s *MetaService) Fields() FieldMetaResult {
	return FieldMetaResult{
		Groups: model.VodGroupLabels,
		Fields: model.VodFieldMetas(),
		Total:  len(model.VodFields),
	}
}
