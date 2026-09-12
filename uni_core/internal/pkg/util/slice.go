package util

import "sort"

// MissingIDs 返回 requested 中在 existing 里不存在的元素。
// 两个入参需已去重；结果按升序排序以保证错误消息输出稳定。
func MissingIDs(requested, existing []uint64) []uint64 {
	if len(requested) == 0 {
		return nil
	}
	have := make(map[uint64]struct{}, len(existing))
	for _, id := range existing {
		have[id] = struct{}{}
	}
	var missing []uint64
	for _, id := range requested {
		if _, ok := have[id]; !ok {
			missing = append(missing, id)
		}
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	return missing
}

// DedupIDs 去除重复元素，保序（首次出现顺序）。
func DedupIDs(ids []uint64) []uint64 {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[uint64]struct{}, len(ids))
	out := make([]uint64, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
