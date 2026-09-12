package util

// Ptr 返回指向 v 的指针，是结构体字面量/字段赋值需要可空指针时的
// 类型安全替身（替代散落的 &v 表达式）。
//
// 与 Coalesce 互为反向操作（值→指针 / 指针→值），集中在本文件。
func Ptr[T any](v T) *T {
	return &v
}

// Coalesce 解引用可能为 nil 的指针：nil 时返回 T 的零值，否则返回 *p。
// 常用于把可空指针扁平化为值语义字段（如监控指标缺值时回退 0）。
func Coalesce[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
