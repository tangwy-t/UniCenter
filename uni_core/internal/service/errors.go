package service

import (
	"errors"

	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"gorm.io/gorm"
)

// translateNotFound 将 gorm.ErrRecordNotFound 翻译为 404 NotFound,其余错误
// 原样返回。契约:repo 层返回原始 gorm 错误,service 层负责翻译;此前各 service
// 文件约 40 处重复 "if errors.Is(err, gorm.ErrRecordNotFound) → NotFound"
// 同形分支,统一收敛到此处。
// 注意:Unauthorized/BadRequest/返回空集等语义不同的分支不经过本 helper。
func translateNotFound(err error, notFoundMsg string) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperror.NotFound(notFoundMsg)
	}
	return err
}
