package repository

import (
	"errors"

	"gorm.io/gorm"
)

// ErrNotFound 是 repository 层对外的「记录不存在」语义。
// GORM 的 ErrRecordNotFound 属于实现细节，不应该泄漏到 service / controller 层。
var ErrNotFound = errors.New("记录不存在")

// notFound 把 GORM 的「记录不存在」翻译成本层错误语义，供各实体仓储共用。
func notFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}
