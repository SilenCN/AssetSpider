// Package service 是四层结构中的第三层，承载全部业务规则与流程编排。
//
// 它只依赖下一层按实体拆分的仓储接口（AssetRepository / SourceRepository /
// VideoRepository / JobRepository），以及横切的 model / config 包；
// 不感知 HTTP（gin）、不感知路由，也不直接触碰 GORM 或 SQL。
package service

import (
	"errors"
	"fmt"

	"github.com/SilenCN/AssetSpider/internal/repository"
)

// 业务错误语义。controller 只认这几个哨兵错误，再翻译成 HTTP 状态码，
// 从而无需知道 repository / gorm 的具体错误类型。
var (
	ErrInvalid  = errors.New("参数无效")
	ErrNotFound = errors.New("资源不存在")
	ErrConflict = errors.New("资源冲突")
)

func invalid(format string, args ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func conflict(format string, args ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrConflict, fmt.Sprintf(format, args...))
}

func notFound(format string, args ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrNotFound, fmt.Sprintf(format, args...))
}

// wrapRepo 把 repository 层的错误语义翻译成 service 层语义。
// 其余错误原样上抛，由 controller 统一按 500 处理。
func wrapRepo(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, repository.ErrNotFound) {
		return fmt.Errorf("%w: %s", ErrNotFound, err.Error())
	}
	return err
}
