package service

import (
	"strings"

	"github.com/SilenCN/AssetSpider/internal/model"
	"github.com/SilenCN/AssetSpider/internal/repository"
)

// AssetInput 是新增资产时允许提交的字段。
type AssetInput struct {
	Host     string `json:"host"`
	IP       string `json:"ip"`
	Category string `json:"category"`
}

// AssetService 处理通用资产台账的读写。
type AssetService struct {
	assets repository.AssetRepository
}

func NewAssetService(assets repository.AssetRepository) *AssetService {
	return &AssetService{assets: assets}
}

func (s *AssetService) List() ([]model.Asset, error) {
	return s.assets.List()
}

func (s *AssetService) Create(in AssetInput) (*model.Asset, error) {
	host := strings.TrimSpace(in.Host)
	if host == "" {
		return nil, invalid("host 不能为空")
	}
	a := &model.Asset{
		Host:     host,
		IP:       strings.TrimSpace(in.IP),
		Category: strings.TrimSpace(in.Category),
	}
	if err := s.assets.Create(a); err != nil {
		return nil, err
	}
	return a, nil
}

func (s *AssetService) Delete(id uint) error {
	return wrapRepo(s.assets.Delete(id))
}
