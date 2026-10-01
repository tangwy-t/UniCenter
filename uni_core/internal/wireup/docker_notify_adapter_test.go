package wireup

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
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
// dockerNoticeSink 是联动器与 system-notice 服务层之间唯一的翻译点：受众
// （docker:list 角色定向）、降级/跳过口径、优先级（重要）、两步发布
// （Create → Publish）都集中在这里。它调的是**真实 NoticeService**（mock 其
// repo 与 eventBus），因此钉住的正是服务层口径：角色定向要走
// PublishType=custom 的「角色 → 成员」解析路径、发布后只向成员定向广播。
//
// 受众反查的 SQL 形态（多菜单同码、CSV 多值、软删）由 repository 与
// dockernotify 的测试钉住；这里的 fake 只需要「给出角色集合」这一层。

// fakePermQuery 是受众反查替身（dockernotify.RolePermQuery 的 record 型实现，
// 与 wireup 生产装配喂的是同一个真实解析件）。
type fakePermQuery struct {
	roles []uint64
	err   error
}

func (q *fakePermQuery) FindRoleIDsByPerm(context.Context, string) ([]uint64, error) {
	return q.roles, q.err
}

// newTestSink 组装被测面：真实 PermAudienceResolver + fake 反查 + 真实
// NoticeService（repo/bus 全 fake）。解析件用默认 TTL 与真时钟 —— 每个用例
// 自建一套，互不见缓存。
func newTestSink(repo *fakeNoticeRepo, bus *fakeEventBus, query *fakePermQuery) *dockerNoticeSink {
	svc := service.NewNoticeService(repo, bus, logger.NewNop())
	audience := dockernotify.NewPermAudienceResolver(
		dockernotify.PermAudienceOptions{}, query, logger.NewNop())
	return newDockerNoticeSink(svc, audience, logger.NewNop())
}

// fakeNoticeRepo 只实现 Create/FindByID/PublishTx/FindUserIDsByRoleIDs 四条
// 真实路径（NoticeService 的发布链用到的），其余方法零值返回 —— 不是测试
// 对象的部分不该有行为。
type fakeNoticeRepo struct {
	mu      sync.Mutex
	nextID  uint64
	created []*entity.SysNotice
	pubbed  []*entity.SysNotice
	records []entity.SysNoticeUser // PublishTx 落库的收件人关联（定向断言源）
	// roleUsers 是「角色 → 成员」解析的替身结果；nil = 角色没有成员
	//（发布链应报「未配置接收人」）。
	roleUsers []uint64
	err       error
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

func (r *fakeNoticeRepo) PublishTx(_ context.Context, n *entity.SysNotice, records []entity.SysNoticeUser) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.pubbed = append(r.pubbed, n)
	r.records = append(r.records, records...)
	return nil
}

func (r *fakeNoticeRepo) FindUserIDsByRoleIDs(_ context.Context, roleIDs []uint64) ([]uint64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	return r.roleUsers, nil
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

// fakeEventBus 记录通知广播（NoticeService 发布后的推送断言用）。
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

// sortedUserIDs 给 map 迭代序不定的 UserIDs 断言取个稳定形态。
func sortedUserIDs(ids []uint64) []uint64 {
	out := append([]uint64(nil), ids...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// 适配器把一条 Alert 变成「docker:list 角色定向、已发布、重要」的通知：
// 走 Create+Publish 两步既有口径，收件人由 notice 层发布时按角色解析成
// 成员（101/102），广播也只发这些人 —— 这就是「被通知」的最后一段。
func TestDockerNoticeSinkPublishesToPermHolders(t *testing.T) {
	repo := &fakeNoticeRepo{roleUsers: []uint64{101, 102}}
	bus := &fakeEventBus{}
	sink := newTestSink(repo, bus, &fakePermQuery{roles: []uint64{5, 9}})

	err := sink.PublishAlert(context.Background(), dockernotify.Alert{
		Title:   "容器异常退出：web（alpha）",
		Content: "主机：alpha（#7）\n退出码：137",
	})
	if err != nil {
		t.Fatalf("PublishAlert: %v", err)
	}

	repo.mu.Lock()
	created, pubbed, records := repo.created, repo.pubbed, repo.records
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
	if n.TargetType == nil || *n.TargetType != entity.NoticeTargetTypeRole ||
		n.TargetIDs != "5,9" ||
		n.PublishType == nil || *n.PublishType != entity.NoticePublishTypeCustom {
		t.Fatalf("受众必须是角色定向（TargetIDs=5,9）: %+v", n)
	}
	if pubbed[0].Status == nil || *pubbed[0].Status != entity.NoticeStatusPublished {
		t.Fatalf("发布后状态应为已发布: %+v", pubbed[0])
	}
	// 收件人关联落库：角色 5/9 → 成员 101/102（notice 层发布时解析）。
	got := make([]uint64, 0, len(records))
	for _, rec := range records {
		got = append(got, rec.UserID)
		if rec.ReadStatus != entity.NoticeReadStatusUnread {
			t.Fatalf("收件人初始应为未读: %+v", rec)
		}
	}
	sortedGot := sortedUserIDs(got)
	if len(sortedGot) != 2 || sortedGot[0] != 101 || sortedGot[1] != 102 {
		t.Fatalf("收件人应为 [101 102]，实际 %v", sortedGot)
	}

	bus.mu.Lock()
	evts := bus.evts
	bus.mu.Unlock()
	if len(evts) != 1 || evts[0].IsAll || evts[0].Type != ws.MsgTypeNewNotice {
		t.Fatalf("发布后必须向成员定向（非全员）广播新通知: %+v", evts)
	}
	if got := sortedUserIDs(evts[0].UserIDs); len(got) != 2 || got[0] != 101 || got[1] != 102 {
		t.Fatalf("广播受众应为成员 [101 102]，实际 %v", got)
	}
	var payload struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(evts[0].NoticeData, &payload); err != nil || payload.Title != "容器异常退出：web（alpha）" {
		t.Fatalf("广播载荷必须带标题: %v %s", err, evts[0].NoticeData)
	}
}

// 无人持有权限：跳过（不 Create、不 Publish、不广播、无草稿残留）——
// 没人该收的通报就是噪音。
func TestDockerNoticeSinkSkipsWhenNoHolders(t *testing.T) {
	repo := &fakeNoticeRepo{roleUsers: []uint64{101}}
	bus := &fakeEventBus{}
	sink := newTestSink(repo, bus, &fakePermQuery{roles: nil})

	if err := sink.PublishAlert(context.Background(), dockernotify.Alert{Title: "x", Content: "y"}); err != nil {
		t.Fatalf("无人持有时应跳过且不报错，实际 err=%v", err)
	}
	if len(repo.created) != 0 || len(repo.pubbed) != 0 {
		t.Fatalf("跳过即零副作用（无草稿、无发布）: created=%d pubbed=%d",
			len(repo.created), len(repo.pubbed))
	}
	if len(bus.evts) != 0 {
		t.Fatalf("跳过不得广播: %+v", bus.evts)
	}
}

// 反查失败：降级全员 + warn（宁误报不漏报）—— 通知照发，受众退回全员广播。
func TestDockerNoticeSinkDegradesToAllOnResolveFailure(t *testing.T) {
	repo := &fakeNoticeRepo{roleUsers: []uint64{101}}
	bus := &fakeEventBus{}
	sink := newTestSink(repo, bus, &fakePermQuery{err: errors.New("db down")})

	if err := sink.PublishAlert(context.Background(), dockernotify.Alert{Title: "x", Content: "y"}); err != nil {
		t.Fatalf("降级全员不是失败路径: %v", err)
	}
	if len(repo.created) != 1 {
		t.Fatalf("降级仍应创建通知: created=%d", len(repo.created))
	}
	n := repo.created[0]
	if n.TargetType == nil || *n.TargetType != entity.NoticeTargetTypeAll ||
		n.PublishType == nil || *n.PublishType != entity.NoticePublishTypeAll {
		t.Fatalf("降级受众必须是全员: %+v", n)
	}
	bus.mu.Lock()
	evts := bus.evts
	bus.mu.Unlock()
	if len(evts) != 1 || !evts[0].IsAll {
		t.Fatalf("降级发布必须全员广播: %+v", evts)
	}
}

// 角色有、成员为空的极端情形：notice 层发布时以「未配置接收人」拒绝 ——
// 错误上抛、草稿留存（既有失败口径），适配器不预查成员（不反查用户的裁定）。
func TestDockerNoticeSinkRoleWithoutMembersFailsPublish(t *testing.T) {
	repo := &fakeNoticeRepo{roleUsers: nil}
	bus := &fakeEventBus{}
	sink := newTestSink(repo, bus, &fakePermQuery{roles: []uint64{5}})

	if err := sink.PublishAlert(context.Background(), dockernotify.Alert{Title: "x", Content: "y"}); err == nil {
		t.Fatal("角色无成员时发布必须失败上抛")
	}
	if len(repo.created) != 1 {
		t.Fatalf("发布失败草稿应留存: created=%d", len(repo.created))
	}
	if len(bus.evts) != 0 {
		t.Fatalf("发布失败不得广播: %+v", bus.evts)
	}
}

// Create 失败 = 发布链终止且错误上抛（联动器只记日志，草稿都不会留下）。
func TestDockerNoticeSinkCreateFailurePropagates(t *testing.T) {
	repo := &fakeNoticeRepo{err: errors.New("db down"), roleUsers: []uint64{101}}
	bus := &fakeEventBus{}
	sink := newTestSink(repo, bus, &fakePermQuery{roles: []uint64{5}})

	if err := sink.PublishAlert(context.Background(), dockernotify.Alert{Title: "x", Content: "y"}); err == nil {
		t.Fatal("Create 失败必须上抛错误")
	}
	if len(bus.evts) != 0 {
		t.Fatalf("Create 失败不得广播: %+v", bus.evts)
	}
}
