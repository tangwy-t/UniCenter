package response

// CacheKeyInfo 缓存 key 列表项：key 名称 + Redis 值类型。
type CacheKeyInfo struct {
	Key  string `json:"key"`
	Type string `json:"type"`
}

// ListKeysResponse 缓存 key 列表响应。
type ListKeysResponse struct {
	Keys   []CacheKeyInfo `json:"keys"`
	Cursor uint64         `json:"cursor,string"`
}

// DeleteKeysResponse 批量删除缓存 key 响应。
type DeleteKeysResponse struct {
	Deleted int64 `json:"deleted"`
}

// CacheValuePage 缓存 key 值分页响应:统一携带分页元信息,value 为当前页内容。
type CacheValuePage struct {
	Key        string      `json:"key"`
	Type       string      `json:"type"`
	TTL        int64       `json:"ttl"`
	Total      int64       `json:"total"`
	Start      int64       `json:"start"`
	HasMore    bool        `json:"has_more"`
	NextCursor uint64      `json:"next_cursor,string"`
	Truncated  bool        `json:"truncated"`
	Value      interface{} `json:"value"`
}
