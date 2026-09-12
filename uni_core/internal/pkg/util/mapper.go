// Package util provides generic helper functions shared across service packages.
package util

import (
	"github.com/jinzhu/copier"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"go.uber.org/zap"
)

// MapEntity copies fields from src to a new value of type T using copier.
// On copy failure it logs a warning and returns the zero value of T:
// copier failure at runtime is effectively unreachable for struct-to-struct
// copies (it fails at development time on type mismatches), and list call
// sites cannot fail mid-loop without abandoning a page of results.
func MapEntity[T any](src any, log logger.LoggerInterface) T {
	var dst T
	if err := copier.Copy(&dst, src); err != nil {
		log.Warn("copier.Copy failed", zap.Error(err))
	}
	return dst
}

// CopyEntity copies fields from src to dst using copier with pointer semantics.
// The caller retains ownership of dst and can modify it further after the copy.
func CopyEntity[T any](dst *T, src any, log logger.LoggerInterface) {
	if err := copier.Copy(dst, src); err != nil {
		log.Warn("copier.Copy failed", zap.Error(err))
	}
}
