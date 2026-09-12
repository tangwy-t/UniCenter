package migration

import (
	"fmt"
	"sort"
	"sync"
)

var (
	registry   []Migration
	registryMu sync.Mutex
)

// Register 将迁移注册到全局注册表。应在 init() 中调用。
// 重复 Version panic:迁移版本是幂等应用的唯一标识,静默重复会让
// 后注册的迁移永不执行(版本已记录),这类编码错误必须启动即失败。
func Register(m Migration) {
	registryMu.Lock()
	defer registryMu.Unlock()
	for _, existing := range registry {
		if existing.Version == m.Version {
			panic(fmt.Sprintf("migration: duplicate version %d registered (%q vs %q)",
				m.Version, existing.Description, m.Description))
		}
	}
	registry = append(registry, m)
}

// All 返回所有已注册的迁移，按 Version 升序排列。
func All() []Migration {
	registryMu.Lock()
	defer registryMu.Unlock()
	sorted := make([]Migration, len(registry))
	copy(sorted, registry)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Version < sorted[j].Version })
	return sorted
}
