package wireup

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockernotify"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/ws"
	"github.com/tangwy-t/UniCenter/uni_core/internal/service"
)

// ── 通知联动器的发布面适配器测试 ──────────────────────────────────────────
//
// dockerNoticeSink 是联动器与 system-notice 服务层之间唯一的翻译点：受众（全员）、
// 优先级（重要）、两步发布（Create → Publish）都在这 20 行里定死。它调的是
// **真实 NoticeService**（mock 其 repo 与 eventBus），因此钉住的正是服务层口径：
// 「全员」要走 PublishType=all 的免收件人路径、发布后要向全员广播。

// fakeNoticeRepo 只实现 Create/FindByID/PublishTx 三条真实路径（NoticeService 的
// 发布链用到的），其余方法零值返回 —— 不是测试对象的部分不该有行为。
type fakeNoticeRepo struct {
	mu      sync.Mutex
	nextID  uint64
	created []*entity.SysNotice
	pubbed  []*entity.SysNotice
	err     error
}

func (r *fakeNoticeRepo) Create(_ context.Context, n *entity.SysNotice) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.nextID++
	n.ID = r.nextID
	r.created = append(r.created, n)
	return nil
}

func (r *fakeNoticeRepo) FindByID(_ context.Context, id uint64) (*entity.SysNotice, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	for _, n := range r.created {
		if n.ID == id {
			return n, nil
		}
	}
	return nil, errors.New("not found")
}

func (r *fakeNoticeRepo) PublishTx(_ context.Context, n *entity.SysNotice, _ []entity.SysNoticeUser) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.pubbed = append(r.pubbed, n)
	return nil
}

// 其余方法零值返回（本测试不走到）。
func (r *fakeNoticeRepo) FindPage(context.Context, *request.NoticeQuery) ([]entity.SysNotice, int64, error) {
	return nil, 0, nil
}
func (r *fakeNoticeRepo) Update(context.Context, *entity.SysNotice) error { return nil }
func (r *fakeNoticeRepo) Delete(context.Context, uint64) error            { return nil }
func (r *fakeNoticeRepo) FindMyNotices(context.Context, uint64) ([]entity.SysNotice, error) {
	return nil, nil
}
func (r *fakeNoticeRepo) FindMyNoticesWithRead(context.Context, uint64) ([]entity.NoticeWithRead, error) {
	return nil, nil
}
func (r *fakeNoticeRepo) UpsertNoticeUser(context.Context, *entity.SysNoticeUser) error {
	return nil
}
func (r *fakeNoticeRepo) MarkAllRead(context.Context, uint64) error { return nil }
func (r *fakeNoticeRepo) FindReadStatusMap(context.Context, uint64, []uint64) (map[uint64]int8, error) {
	return nil, nil
}
func (r *fakeNoticeRepo) FindUserIDsByRoleIDs(context.Context, []uint64) ([]uint64, error) {
	return nil, nil
}
func (r *fakeNoticeRepo) FindUserIDsByDeptIDs(context.Context, []uint64) ([]uint64, error) {
	return nil, nil
}
func (r *fakeNoticeRepo) FindUserNamesByIDs(context.Context, []uint64) (map[uint64]string, error) {
	return nil, nil
}
func (r *fakeNoticeRepo) FindUsersByIDs(context.Context, []uint64) ([]entity.SysUser, error) {
	return nil, nil
}
func (r *fakeNoticeRepo) FindReadUsers(context.Context, uint64, *request.NoticeReadUsersQuery) ([]response.NoticeReadUserResp, int64, error) {
	return nil, 0, nil
}

// fakeEventBus 记录通知广播（NoticeService 发布后的全员推送断言用）。
type fakeEventBus struct {
	mu   sync.Mutex
	evts []*ws.PushEvent
}

func (b *fakeEventBus) PublishNotice(_ context.Context, evt *ws.PushEvent) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.evts = append(b.evts, evt)
	return nil
}

// 适配器把一条 Alert 变成「全员可见、已发布、重要」的通知：走 Create+Publish
// 两步既有口径，并触发全员广播 —— 这就是「被通知」的最后一段。
func TestDockerNoticeSinkPublishesToAll(t *testing.T) {
	repo := &fakeNoticeRepo{}
	bus := &fakeEventBus{}
	svc := service.NewNoticeService(repo, bus, logger.NewNop())
	sink := newDockerNoticeSink(svc, logger.NewNop())

	err := sink.PublishAlert(context.Background(), dockernotify.Alert{
		Title:   "容器异常退出：web（alpha）",
		Content: "主机：alpha（#7）\n退出码：137",
	})
	if err != nil {
		t.Fatalf("PublishAlert: %v", err)
	}

	repo.mu.Lock()
	created := repo.created
	pubbed := repo.pubbed
	repo.mu.Unlock()
	if len(created) != 1 || len(pubbed) != 1 {
		t.Fatalf("应各走一次 Create+Publish，实际 create=%d publish=%d",
			len(created), len(pubbed))
	}
	n := created[0]
	if n.Title != "容器异常退出：web（alpha）" || *n.Content != "主机：alpha（#7）\n退出码：137" {
		t.Fatalf("标题/正文不符: %+v", n)
	}
	if *n.Priority != entity.NoticePriorityImportant || *n.NoticeType != entity.NoticeTypeNotice {
		t.Fatalf("优先级/类型应为重要/通知: %+v", n)
	}
	if n.TargetType == nil || *n.TargetType != entity.NoticeTargetTypeAll ||
		n.PublishType == nil || *n.PublishType != entity.NoticePublishTypeAll {
		t.Fatalf("受众必须是全员发布: %+v", n)
	}
	if pubbed[0].Status == nil || *pubbed[0].Status != entity.NoticeStatusPublished {
		t.Fatalf("发布后状态应为已发布: %+v", pubbed[0])
	}

	bus.mu.Lock()
	evts := bus.evts
	bus.mu.Unlock()
	if len(evts) != 1 || !evts[0].IsAll || evts[0].Type != ws.MsgTypeNewNotice {
		t.Fatalf("发布后必须向全员广播新通知: %+v", evts)
	}
	var payload struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(evts[0].NoticeData, &payload); err != nil || payload.Title != "容器异常退出：web（alpha）" {
		t.Fatalf("广播载荷必须带标题: %v %s", err, evts[0].NoticeData)
	}
}

// Create 失败 = 发布链终止且错误上抛（联动器只记日志，草稿都不会留下）。
func TestDockerNoticeSinkCreateFailurePropagates(t *testing.T) {
	repo := &fakeNoticeRepo{err: errors.New("db down")}
	bus := &fakeEventBus{}
	svc := service.NewNoticeService(repo, bus, logger.NewNop())
	sink := newDockerNoticeSink(svc, logger.NewNop())

	if err := sink.PublishAlert(context.Background(), dockernotify.Alert{Title: "x", Content: "y"}); err == nil {
		t.Fatal("Create 失败必须上抛错误")
	}
	if len(bus.evts) != 0 {
		t.Fatalf("Create 失败不得广播: %+v", bus.evts)
	}
}
