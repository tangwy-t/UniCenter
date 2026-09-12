// Package storage 抽象文件内容的物理持久化后端（本地盘 / S3 兼容对象存储）。
//
// 上层（service 层）依据文件元数据的 StorageType 选择对应的 Backend：
//   - local: key 为上传根目录下的绝对路径（由上层拼接 base+rel）。
//   - s3:    key 为 bucket 内的对象键。
//
// 引入此抽象后，文件模块的上传/读取/删除不再直接触碰 os 包或具体 SDK，
// 本地盘与对象存储共享同一套调用路径。
package storage

import (
	"context"
	"errors"
	"io"
)

var (
	// ErrNotFound 表示对象不存在（读取/删除时按缺失处理）。
	ErrNotFound = errors.New("storage: object not found")
	// ErrTooLarge 表示待写入内容超过大小上限。
	ErrTooLarge = errors.New("storage: object exceeds size limit")
)

// Backend 抽象对象的写入/读取/删除。
type Backend interface {
	// Put 以流式方式把 src 写入 key，并强制大小上限：实际写入超过
	// maxSize 时返回 ErrTooLarge，且不残留残缺对象。返回写入字节数。
	Put(ctx context.Context, key string, src io.Reader, maxSize int64) (int64, error)
	// Open 返回可随机读取（io.Seeker）的读句柄，支持 Range 分段请求；
	// 对象不存在时返回 ErrNotFound。
	Open(ctx context.Context, key string) (io.ReadSeekCloser, error)
	// Delete 删除对象；对象不存在视为成功（幂等）。
	Delete(ctx context.Context, key string) error
}
