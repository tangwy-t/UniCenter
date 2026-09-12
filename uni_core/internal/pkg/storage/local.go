package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Local 是本地磁盘后端。key 为绝对文件路径（相对上传根目录由上层拼接）。
type Local struct{}

// NewLocal 返回一个无状态的本地磁盘后端。
func NewLocal() *Local { return &Local{} }

func (l *Local) Put(ctx context.Context, key string, src io.Reader, maxSize int64) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(key), 0o755); err != nil {
		return 0, fmt.Errorf("storage/local: mkdir: %w", err)
	}
	// O_EXCL：排他创建，杜绝同名覆盖与竞态。
	dst, err := os.OpenFile(key, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return 0, fmt.Errorf("storage/local: create: %w", err)
	}
	// 一旦文件创建成功，后续失败须删除半成品，避免孤儿文件残留。
	removeOnError := true
	defer func() {
		if removeOnError {
			_ = os.Remove(key)
		}
	}()

	n, err := io.Copy(dst, io.LimitReader(src, maxSize+1))
	if closeErr := dst.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return 0, fmt.Errorf("storage/local: write: %w", err)
	}
	if n > maxSize {
		return 0, ErrTooLarge
	}
	removeOnError = false
	return n, nil
}

func (l *Local) Open(ctx context.Context, key string) (io.ReadSeekCloser, error) {
	f, err := os.Open(key)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("storage/local: open: %w", err)
	}
	return f, nil
}

func (l *Local) Delete(ctx context.Context, key string) error {
	if err := os.Remove(key); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("storage/local: remove: %w", err)
	}
	return nil
}
