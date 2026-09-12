package session

import (
	"context"
	"testing"
	"time"
)

func TestSIDDeterministic(t *testing.T) {
	if SID("tok") != SID("tok") {
		t.Fatal("SID 不具确定性")
	}
	if SID("tok") == SID("tok2") {
		t.Fatal("不同 token 得到相同 sid")
	}
	if len(SID("tok")) != 64 {
		t.Fatalf("SID 长度 = %d, want 64", len(SID("tok")))
	}
}

func TestSessionMetaRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := NewSession(newFakeCache())
	m := &SessionMeta{UserID: 7, IP: "1.2.3.4", Browser: "Chrome", OS: "Windows", LoginAt: 1, ExpireAt: 2}
	if err := s.StoreSessionMeta(ctx, "tok", m, time.Minute); err != nil {
		t.Fatalf("StoreSessionMeta: %v", err)
	}
	got, err := s.LoadSessionMeta(ctx, "tok")
	if err != nil || got == nil || got.IP != "1.2.3.4" {
		t.Fatalf("LoadSessionMeta = %v/%v", got, err)
	}
	if got2, err := s.LoadSessionMeta(ctx, "missing"); err != nil || got2 != nil {
		t.Fatalf("miss want nil/nil, got %v/%v", got2, err)
	}
}

func TestBulkLoadSessionMeta(t *testing.T) {
	ctx := context.Background()
	s := NewSession(newFakeCache())
	_ = s.StoreSessionMeta(ctx, "a", &SessionMeta{IP: "a"}, time.Minute)
	_ = s.StoreSessionMeta(ctx, "c", &SessionMeta{IP: "c"}, time.Minute)
	got, err := s.BulkLoadSessionMeta(ctx, []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("BulkLoadSessionMeta: %v", err)
	}
	if len(got) != 2 || got["a"].IP != "a" || got["c"].IP != "c" {
		t.Fatalf("got = %+v", got)
	}
}

func TestListOnlineUserIDsAndTokens(t *testing.T) {
	ctx := context.Background()
	s := NewSession(newFakeCache())
	_ = s.StoreAccess(ctx, "tok7", 7, time.Hour)
	_ = s.StoreAccess(ctx, "tok9", 9, time.Hour)
	uids, err := s.ListOnlineUserIDs(ctx, 100)
	if err != nil || len(uids) != 2 {
		t.Fatalf("uids = %v/%v", uids, err)
	}
	toks, _ := s.ListUserTokens(ctx, 7)
	if len(toks) != 1 || toks[0] != "tok7" {
		t.Fatalf("tokens = %v", toks)
	}
}
