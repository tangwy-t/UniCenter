package service

import (
	"context"
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/jwt"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/session"
)

// ── fakes ────────────────────────────────────────────────────────────────

type fakeOnlineStore struct {
	uids       []uint64
	toks       map[uint64][]string
	metas      map[string]*session.SessionMeta
	revokedUID uint64
	revokeErr  error
}

func (f *fakeOnlineStore) ListOnlineUserIDs(context.Context, int64) ([]uint64, error) {
	return f.uids, nil
}
func (f *fakeOnlineStore) ListUserTokens(_ context.Context, uid uint64) ([]string, error) {
	return f.toks[uid], nil
}
func (f *fakeOnlineStore) BulkLoadSessionMeta(_ context.Context, tokens []string) (map[string]*session.SessionMeta, error) {
	out := map[string]*session.SessionMeta{}
	for _, t := range tokens {
		if m, ok := f.metas[t]; ok {
			out[t] = m
		}
	}
	return out, nil
}
func (f *fakeOnlineStore) RevokeAll(_ context.Context, uid uint64, _ string) error {
	f.revokedUID = uid
	return f.revokeErr
}

type fakeOnlineUsers struct {
	users []entity.SysUser
}

func (f *fakeOnlineUsers) FindByIDs(context.Context, []uint64) ([]entity.SysUser, error) {
	return f.users, nil
}

const testSecret = "test-secret-key-for-online-service-tests-32b"

type fakeOnlineCfg struct{}

func (fakeOnlineCfg) GetString(_ context.Context, _ string, _ string) string { return testSecret }

type fakeWsKick struct {
	kickedUID    uint64
	kickedReason string
	kickedToken  string
	calls        int
}

func (f *fakeWsKick) PublishKick(_ context.Context, userID uint64, reason, kickToken string) error {
	f.kickedUID = userID
	f.kickedReason = reason
	f.kickedToken = kickToken
	f.calls++
	return nil
}

func newOnlineSvc(store *fakeOnlineStore, users *fakeOnlineUsers) (*OnlineUserService, *fakeWsKick) {
	kick := &fakeWsKick{}
	return NewOnlineUserService(store, users, fakeOnlineCfg{}, kick, logger.NewNop()), kick
}

func deptPtr() *entity.SysDept { return &entity.SysDept{Name: "研发部"} }

// ── tests ────────────────────────────────────────────────────────────────

func TestListAggregatesSameDevice(t *testing.T) {
	store := &fakeOnlineStore{
		uids: []uint64{1},
		toks: map[uint64][]string{1: {"tokA", "tokB"}},
		metas: map[string]*session.SessionMeta{
			"tokA": {UserID: 1, IP: "1.1.1.1", UserAgent: "ua", Browser: "Chrome", OS: "Windows", LoginAt: 100, ExpireAt: 4102444800},
			"tokB": {UserID: 1, IP: "1.1.1.1", UserAgent: "ua", Browser: "Chrome", OS: "Windows", LoginAt: 100, ExpireAt: 4102444800},
		},
	}
	users := &fakeOnlineUsers{users: []entity.SysUser{{BaseEntity: entity.BaseEntity{ID: 1}, Username: "alice", Dept: deptPtr()}}}
	svc, _ := newOnlineSvc(store, users)

	page, err := svc.List(context.Background(), &request.OnlineUserQuery{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	list := page.List.([]response.OnlineSession)
	if page.Total != 1 {
		t.Fatalf("聚合后应 1 行, got total=%d", page.Total)
	}
	if list[0].TokenCount != 2 || list[0].Username != "alice" {
		t.Fatalf("row = %+v", list[0])
	}
}

func TestListSkipsInvisibleUser(t *testing.T) {
	store := &fakeOnlineStore{
		uids: []uint64{1, 2},
		toks: map[uint64][]string{
			1: {"a"},
			2: {"b"},
		},
		metas: map[string]*session.SessionMeta{
			"a": {UserID: 1, IP: "1.1.1.1", UserAgent: "ua", Browser: "Chrome", OS: "Windows", LoginAt: 100, ExpireAt: 4102444800},
		},
	}
	users := &fakeOnlineUsers{users: []entity.SysUser{{BaseEntity: entity.BaseEntity{ID: 1}, Username: "alice"}}}
	svc, _ := newOnlineSvc(store, users)
	page, _ := svc.List(context.Background(), &request.OnlineUserQuery{})
	if page.Total != 1 {
		t.Fatalf("不可见用户应被过滤, got total=%d", page.Total)
	}
}

func TestKickForbiddenWhenOutOfScope(t *testing.T) {
	store := &fakeOnlineStore{}
	users := &fakeOnlineUsers{users: nil} // 可见用户为空 → 越权
	svc, _ := newOnlineSvc(store, users)
	err := svc.Kick(context.Background(), &request.KickSessionReq{UserID: 1}, "")
	if err == nil {
		t.Fatal("越权踢下线应报错")
	}
}

func TestKickSelfRejected(t *testing.T) {
	store := &fakeOnlineStore{}
	users := &fakeOnlineUsers{users: []entity.SysUser{{BaseEntity: entity.BaseEntity{ID: 1}}}}
	svc, _ := newOnlineSvc(store, users)
	tok, err := jwt.GenerateAccessToken(1, nil, nil, testSecret, 7200)
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	if err := svc.Kick(context.Background(), &request.KickSessionReq{UserID: 1}, tok); err == nil {
		t.Fatal("自踢应被拒绝")
	}
}

func TestKickRevokesAll(t *testing.T) {
	store := &fakeOnlineStore{}
	users := &fakeOnlineUsers{users: []entity.SysUser{{BaseEntity: entity.BaseEntity{ID: 1}}}}
	svc, kick := newOnlineSvc(store, users)
	if err := svc.Kick(context.Background(), &request.KickSessionReq{UserID: 1}, ""); err != nil {
		t.Fatalf("kick: %v", err)
	}
	if store.revokedUID != 1 {
		t.Fatalf("应 RevokeAll 该用户, got uid=%d", store.revokedUID)
	}
	if kick.calls != 1 || kick.kickedUID != 1 || kick.kickedReason != "您已被管理员强制下线" || kick.kickedToken != "" {
		t.Fatalf("应下发 WS 踢人事件: calls=%d uid=%d reason=%q token=%q", kick.calls, kick.kickedUID, kick.kickedReason, kick.kickedToken)
	}
}

func TestKickNoWsPublisherDoesNotPanic(t *testing.T) {
	store := &fakeOnlineStore{}
	users := &fakeOnlineUsers{users: []entity.SysUser{{BaseEntity: entity.BaseEntity{ID: 1}}}}
	svc := NewOnlineUserService(store, users, fakeOnlineCfg{}, nil, logger.NewNop())
	if err := svc.Kick(context.Background(), &request.KickSessionReq{UserID: 1}, ""); err != nil {
		t.Fatalf("nil ws publisher 下 kick 不应失败: %v", err)
	}
}
