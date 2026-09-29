package dockerstate

import (
	"context"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

// 一次性票据的四条纪律：TTL 30s、单次使用、绑定三要素、随机性。
func TestTicketIssueConsumeAndExpiry(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := NewTicketStore(rdb)
	ctx := context.Background()

	tk, err := store.Issue(ctx, 42, 7, "sess-0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if tk.Ticket == "" || len(tk.Ticket) < 32 {
		t.Fatalf("票据必须是 32B 随机串的 URL 安全编码: %q", tk.Ticket)
	}
	if strings.ContainsAny(tk.Ticket, "+/=") {
		t.Fatalf("票据要直接进查询参数，必须是 URL 安全字符集: %q", tk.Ticket)
	}
	// TTL：恰为契约的 30 秒（短 TTL 是「凭证不落 URL 长驻」的落点）。
	if ttl := mr.TTL(StreamTicketKeyPrefix + tk.Ticket); ttl != StreamTicketTTL {
		t.Fatalf("票据 TTL 应为 %v，实际 %v", StreamTicketTTL, ttl)
	}

	// 单次使用：第一次命中且删除，第二次必须落空。
	got, err := store.Consume(ctx, tk.Ticket)
	if err != nil || got == nil {
		t.Fatalf("首次消费必须命中: %+v %v", got, err)
	}
	if got.UserID != 42 || got.DeviceID != 7 || got.SessionID != "sess-0123456789abcdef" {
		t.Fatalf("票据必须绑定 userId+sessionId+deviceId: %+v", got)
	}
	again, err := store.Consume(ctx, tk.Ticket)
	if err != nil || again != nil {
		t.Fatalf("票据必须单次使用（第二次落空）: %+v %v", again, err)
	}

	// 过期：TTL 到自然作废（不消费即不存在）。
	tk2, err := store.Issue(ctx, 42, 7, "sess-0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	mr.FastForward(StreamTicketTTL + 1)
	expired, err := store.Consume(ctx, tk2.Ticket)
	if err != nil || expired != nil {
		t.Fatalf("过期票据必须落空: %+v %v", expired, err)
	}

	// 空票据与未知票据都是「落空」而不是错误（调用方统一折成 401）。
	if got, err := store.Consume(ctx, ""); err != nil || got != nil {
		t.Fatalf("空票据应落空: %+v %v", got, err)
	}
	if got, err := store.Consume(ctx, "no-such-ticket"); err != nil || got != nil {
		t.Fatalf("未知票据应落空: %+v %v", got, err)
	}
}

// 两次签发的票据必须不同（crypto/rand；相同即意味着可预测 -> 可枚举）。
func TestTicketValuesAreRandom(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := NewTicketStore(rdb)
	ctx := context.Background()

	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		tk, err := store.Issue(ctx, 1, 2, "sess-0123456789abcdef")
		if err != nil {
			t.Fatal(err)
		}
		if seen[tk.Ticket] {
			t.Fatalf("票据重复: %q", tk.Ticket)
		}
		seen[tk.Ticket] = true
	}
}
