package snowflake

import (
	"sync"
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
)

// TestNewValidWorker workers 0 与 1023 均在合法范围内。
func TestNewValidWorker(t *testing.T) {
	for _, id := range []int64{0, 1023} {
		node, err := New(id, logger.NewNop())
		if err != nil || node == nil {
			t.Fatalf("New(%d) = %v/%v, want ok", id, node, err)
		}
	}
}

// TestNewInvalidWorker 越界 workerID 必须报错(bwmarrin 约定 0-1023)。
func TestNewInvalidWorker(t *testing.T) {
	for _, id := range []int64{-1, 1024} {
		if _, err := New(id, logger.NewNop()); err == nil {
			t.Fatalf("New(%d) 应报错", id)
		}
	}
}

// TestIDsUniqueAndMonotonic 串行 10k 个 ID 唯一且严格递增。
func TestIDsUniqueAndMonotonic(t *testing.T) {
	node, err := New(1, logger.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[uint64]struct{}, 10000)
	var prev uint64
	for i := 0; i < 10000; i++ {
		id := node.Generate().Int64()
		u := uint64(id)
		if _, dup := seen[u]; dup {
			t.Fatalf("第 %d 个 ID 重复: %d", i, u)
		}
		seen[u] = struct{}{}
		if i > 0 && u <= prev {
			t.Fatalf("ID 未严格递增: %d <= %d", u, prev)
		}
		prev = u
	}
}

// TestIDsUniqueConcurrent 并发生成仍全局唯一。
func TestIDsUniqueConcurrent(t *testing.T) {
	node, err := New(2, logger.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	const workers, perWorker = 8, 2500
	var wg sync.WaitGroup
	ids := make(chan uint64, workers*perWorker)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				ids <- uint64(node.Generate().Int64())
			}
		}()
	}
	wg.Wait()
	close(ids)

	seen := make(map[uint64]struct{}, workers*perWorker)
	for id := range ids {
		if _, dup := seen[id]; dup {
			t.Fatalf("并发重复 ID: %d", id)
		}
		seen[id] = struct{}{}
	}
}
