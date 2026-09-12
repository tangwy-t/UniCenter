package response

// IDResp 是 Create 类接口的最小成功负载:返回新建资源的 ID。
// ID 序列化为字符串,与实体层的 json:"id,string" 约定一致
// (前端拿到的 ID 统一是字符串,避免 JS number 精度丢失)。
type IDResp struct {
	ID uint64 `json:"id,string"`
}
