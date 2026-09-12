package service

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/storage"
	"github.com/tangwy-t/webmanager-server/internal/pkg/thumb"
)

const (
	defaultThumbWidth  = 256
	maxThumbWidth      = 512
	thumbContentType   = "image/jpeg"
	thumbCacheLimit    = 256      // 缓存条目上限(按 id:width 键)
	thumbCacheMaxBytes = 64 << 20 // 缓存总字节上限
)

// thumbLRU 是缩略图的内存 LRU:请求按 id:width 键命中,
// 命中走内存(转码仅发生一次),容量到达上限时按最近最少使用淘汰。
type thumbLRU struct {
	mu      sync.Mutex
	entries map[string]*thumbLRUEntry
	order   *list.List // 队首 = 最近使用
	bytes   int64
}

type thumbLRUEntry struct {
	key  string
	data []byte
	elem *list.Element
}

func newThumbLRU() *thumbLRU {
	return &thumbLRU{entries: make(map[string]*thumbLRUEntry), order: list.New()}
}

func (c *thumbLRU) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(entry.elem)
	return entry.data, true
}

func (c *thumbLRU) set(key string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry, exists := c.entries[key]; exists {
		entry.data = data
		c.order.MoveToFront(entry.elem)
		return
	}
	entry := &thumbLRUEntry{key: key, data: data, elem: c.order.PushFront(key)}
	c.entries[key] = entry
	c.bytes += int64(len(data))

	// 淘汰:超条目数或超总字节时,从队尾(最久未用)移除。
	for c.order.Len() > thumbCacheLimit || c.bytes > thumbCacheMaxBytes {
		last := c.order.Back()
		if last == nil {
			break
		}
		oldKey := last.Value.(string)
		c.order.Remove(last)
		c.bytes -= int64(len(c.entries[oldKey].data))
		delete(c.entries, oldKey)
	}
}

// Thumb 生成(或从缓存取出)图片文件的 JPEG 缩略图。
// 非图片/不可解码(如 SVG 数据)返回 BadRequest,由前端回退为图标。
func (s *FileService) Thumb(ctx context.Context, id uint64, width int) ([]byte, string, error) {
	if width <= 0 {
		width = defaultThumbWidth
	}
	width = min(width, maxThumbWidth)

	cacheKey := fmt.Sprintf("%d:%d", id, width)
	if data, ok := s.thumbCache.get(cacheKey); ok {
		return data, thumbContentType, nil
	}

	file, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if file == nil {
		return nil, "", apperror.NotFound("文件不存在")
	}
	ext := ""
	if file.Ext != nil {
		ext = *file.Ext
	}
	if entity.CategoryOfExt(ext) != entity.FileCategoryImage {
		return nil, "", apperror.BadRequest("非图片文件不支持缩略图")
	}

	backend, err := s.backendFor(file.StorageType)
	if err != nil {
		return nil, "", err
	}
	reader, err := backend.Open(ctx, s.resolveKey(ctx, file))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, "", apperror.NotFound("文件已丢失")
		}
		return nil, "", err
	}
	defer reader.Close()

	data, err := thumb.Generate(reader, width)
	if err != nil {
		var invalid thumb.Invalid
		if errors.As(err, &invalid) {
			return nil, "", apperror.BadRequest("无法生成缩略图")
		}
		return nil, "", apperror.Internal("缩略图生成失败", err)
	}

	s.thumbCache.set(cacheKey, data)
	return data, thumbContentType, nil
}
