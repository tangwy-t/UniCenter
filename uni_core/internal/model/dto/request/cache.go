package request

// ListKeysRequest 缓存 key 列表查询参数。
type ListKeysRequest struct {
	Prefix string `form:"prefix"`
	Cursor uint64 `form:"cursor"`
	Count  int64  `form:"count" binding:"min=1,max=100"`
}

// GetKeyValueRequest 缓存 key 值查询参数。
type GetKeyValueRequest struct {
	Key string `form:"key" binding:"required"`
	// Offset list/zset 起始下标、string 起始字节
	Offset int64 `form:"offset" binding:"min=0"`
	// Limit list/zset/set/hash 每页条数(≤200)、string 字节窗(≤4MB)。
	// binding 只做全局上限(4MB);集合类 200 的上限依赖 type,由 store 层
	// ErrBadLimit 哨兵错误承接,service 层翻译为 400。
	Limit int64 `form:"limit" binding:"min=1,max=4194304"`
	// Cursor set/hash 的 SCAN 游标,0 表示从头扫
	Cursor uint64 `form:"cursor"`
}

// DeleteKeysRequest 批量删除缓存 key 请求参数。
// maxCount 加上限：无上限时传 1e9 可扫描整个 keyspace 批量删 key。
type DeleteKeysRequest struct {
	Prefix   string `form:"prefix" binding:"required"`
	MaxCount int64  `form:"maxCount" binding:"min=1,max=10000"`
}
