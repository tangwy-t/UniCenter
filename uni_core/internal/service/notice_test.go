package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/contextkeys"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
	"github.com/tangwy-t/webmanager-server/internal/pkg/ws"

	"gorm.io/gorm"
)

// ── 测试替身 ────────────────────────────────────────────

type stubNoticeRepo struct {
	findByIDNotice *entity.SysNotice
	findPageList   []entity.SysNotice
	findPageTotal  int64
	findReadMap    map[uint64]int8
	created        *entity.SysNotice
	updated        *entity.SysNotice

	userNames map[uint64]string
	roleUsers map[uint64][]uint64
	deptUsers map[uint64][]uint64

	publishedNotice *entity.SysNotice
	publishedRows   []entity.SysNoticeUser

	readUsers      []response.NoticeReadUserResp
	readUsersTotal int64
	findReadUsers  *request.NoticeReadUsersQuery

	targetUsers []entity.SysUser

	myNoticesWithRead []entity.NoticeWithRead

	// markAllReadCalled / markAllReadErr 供 MarkAllRead 断言与错误注入。
	markAllReadCalled bool
	markAllReadErr    error
}

func (r *stubNoticeRepo) FindPage(context.Context, *request.NoticeQuery) ([]entity.SysNotice, int64, error) {
	return r.findPageList, r.findPageTotal, nil
}

func (r *stubNoticeRepo) FindReadStatusMap(context.Context, uint64, []uint64) (map[uint64]int8, error) {
	return r.findReadMap, nil
}
func (r *stubNoticeRepo) FindByID(_ context.Context, id uint64) (*entity.SysNotice, error) {
	if r.findByIDNotice != nil && r.findByIDNotice.ID == id {
		return r.findByIDNotice, nil
	}
	return nil, gorm.ErrRecordNotFound
}
func (r *stubNoticeRepo) Create(_ context.Context, notice *entity.SysNotice) error {
	r.created = notice
	if notice.ID == 0 {
		notice.ID = 100
	}
	return nil
}
func (r *stubNoticeRepo) Update(_ context.Context, notice *entity.SysNotice) error {
	r.updated = notice
	return nil
}
func (r *stubNoticeRepo) Delete(context.Context, uint64) error { return nil }
func (r *stubNoticeRepo) FindMyNotices(context.Context, uint64) ([]entity.SysNotice, error) {
	return nil, nil
}
func (r *stubNoticeRepo) FindMyNoticesWithRead(context.Context, uint64) ([]entity.NoticeWithRead, error) {
	return r.myNoticesWithRead, nil
}
func (r *stubNoticeRepo) PublishTx(_ context.Context, n *entity.SysNotice, rows []entity.SysNoticeUser) error {
	r.publishedNotice = n
	r.publishedRows = rows
	return nil
}
func (r *stubNoticeRepo) UpsertNoticeUser(context.Context, *entity.SysNoticeUser) error {
	return nil
}
func (r *stubNoticeRepo) MarkAllRead(_ context.Context, userID uint64) error {
	r.markAllReadCalled = true
	return r.markAllReadErr
}
func (r *stubNoticeRepo) FindUserIDsByRoleIDs(_ context.Context, roleIDs []uint64) ([]uint64, error) {
	var out []uint64
	for _, rid := range roleIDs {
		out = append(out, r.roleUsers[rid]...)
	}
	return out, nil
}
func (r *stubNoticeRepo) FindUserIDsByDeptIDs(_ context.Context, deptIDs []uint64) ([]uint64, error) {
	var out []uint64
	for _, did := range deptIDs {
		out = append(out, r.deptUsers[did]...)
	}
	return out, nil
}
func (r *stubNoticeRepo) FindUserNamesByIDs(_ context.Context, ids []uint64) (map[uint64]string, error) {
	out := make(map[uint64]string, len(ids))
	for _, id := range ids {
		if name, ok := r.userNames[id]; ok {
			out[id] = name
		}
	}
	return out, nil
}
func (r *stubNoticeRepo) FindUsersByIDs(context.Context, []uint64) ([]entity.SysUser, error) {
	return r.targetUsers, nil
}
func (r *stubNoticeRepo) FindReadUsers(_ context.Context, _ uint64, q *request.NoticeReadUsersQuery) ([]response.NoticeReadUserResp, int64, error) {
	r.findReadUsers = q
	return r.readUsers, r.readUsersTotal, nil
}

type stubEventBus struct {
	last *ws.PushEvent
}

func (b *stubEventBus) PublishNotice(_ context.Context, evt *ws.PushEvent) error {
	b.last = evt
	return nil
}

func newTestNoticeService(repo NoticeRepositoryInterface) (*NoticeService, *stubEventBus) {
	bus := &stubEventBus{}
	return NewNoticeService(repo, bus, logger.NewNop()), bus
}

// ── targetDesc ─────────────────────────────────────────

func TestTargetDesc(t *testing.T) {
	svc, _ := newTestNoticeService(&stubNoticeRepo{})
	cases := []struct {
		name   string
		notice *entity.SysNotice
		want   string
	}{
		{"遗留-全员发布", &entity.SysNotice{PublishType: util.Ptr[int8](entity.NoticePublishTypeAll)}, "全体成员"},
		{"遗留-自定义发布", &entity.SysNotice{PublishType: util.Ptr[int8](entity.NoticePublishTypeCustom)}, "指定成员"},
		{"全体成员", &entity.SysNotice{TargetType: util.Ptr[int8](0)}, "全体成员"},
		{"指定角色", &entity.SysNotice{TargetType: util.Ptr[int8](1), TargetIDs: "3,4,5"}, "指定角色（3个）"},
		{"指定部门", &entity.SysNotice{TargetType: util.Ptr[int8](2), TargetIDs: "7"}, "指定部门（1个）"},
		{"指定个人-空ID", &entity.SysNotice{TargetType: util.Ptr[int8](3), TargetIDs: ""}, "指定个人"},
		{"未知类型兜底", &entity.SysNotice{TargetType: util.Ptr[int8](9)}, "全体成员"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := svc.targetDesc(tc.notice); got != tc.want {
				t.Fatalf("targetDesc = %q, want %q", got, tc.want)
			}
		})
	}
}

// ── applyTargetScope ───────────────────────────────────

func TestApplyTargetScope_Validation(t *testing.T) {
	svc, _ := newTestNoticeService(&stubNoticeRepo{})

	// 非法类型
	n := &entity.SysNotice{}
	assertCode(t, svc.applyTargetScope(n, util.Ptr[int8](9), ""), apperror.CodeBadRequest)
	// 指定范围但未选对象
	assertCode(t, svc.applyTargetScope(n, util.Ptr[int8](entity.NoticeTargetTypeRole), "  "), apperror.CodeBadRequest)
	// ID 非法
	assertCode(t, svc.applyTargetScope(n, util.Ptr[int8](entity.NoticeTargetTypeUser), "1,abc"), apperror.CodeBadRequest)
	// nil 目标类型(旧客户端)不报错、不改字段
	old := &entity.SysNotice{PublishType: util.Ptr[int8](entity.NoticePublishTypeAll), TargetType: nil}
	if err := svc.applyTargetScope(old, nil, ""); err != nil {
		t.Fatalf("nil targetType should pass, got %v", err)
	}
}

func TestApplyTargetScope_DerivesPublishType(t *testing.T) {
	svc, _ := newTestNoticeService(&stubNoticeRepo{})

	n := &entity.SysNotice{}
	if err := svc.applyTargetScope(n, util.Ptr[int8](entity.NoticeTargetTypeRole), " 11 , 12 "); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n.TargetType == nil || *n.TargetType != entity.NoticeTargetTypeRole {
		t.Fatalf("TargetType = %v", n.TargetType)
	}
	if n.TargetIDs != "11,12" {
		t.Fatalf("TargetIDs = %q, want trimmed csv", n.TargetIDs)
	}
	if n.PublishType == nil || *n.PublishType != entity.NoticePublishTypeCustom {
		t.Fatalf("PublishType = %v, want custom", n.PublishType)
	}

	m := &entity.SysNotice{}
	if err := svc.applyTargetScope(m, util.Ptr[int8](entity.NoticeTargetTypeAll), ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.PublishType == nil || *m.PublishType != entity.NoticePublishTypeAll {
		t.Fatalf("PublishType = %v, want all", m.PublishType)
	}
}

// ── Create 应用接收范围 ────────────────────────────────

func TestCreate_AppliesTargetScope(t *testing.T) {
	repo := &stubNoticeRepo{}
	svc, _ := newTestNoticeService(repo)

	id, err := svc.Create(context.Background(), &request.CreateNoticeReq{
		Title:      "测试",
		Content:    util.Ptr("内容"),
		TargetType: util.Ptr[int8](entity.NoticeTargetTypeDept),
		TargetIDs:  "20,21",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == 0 {
		t.Fatalf("expected id")
	}
	if repo.created == nil {
		t.Fatalf("created nil")
	}
	if repo.created.Status == nil || *repo.created.Status != entity.NoticeStatusDraft {
		t.Fatalf("Status = %v, want draft", repo.created.Status)
	}
	if repo.created.TargetType == nil || *repo.created.TargetType != entity.NoticeTargetTypeDept {
		t.Fatalf("TargetType = %v", repo.created.TargetType)
	}
	if repo.created.PublishType == nil || *repo.created.PublishType != entity.NoticePublishTypeCustom {
		t.Fatalf("PublishType = %v, want custom", repo.created.PublishType)
	}
}

// ── Publish 回退存储的接收范围 ─────────────────────────

func TestPublish_FallsBackToStoredScope(t *testing.T) {
	repo := &stubNoticeRepo{
		findByIDNotice: &entity.SysNotice{
			BaseEntity:  entity.BaseEntity{ID: 55},
			Title:       "T",
			Status:      util.Ptr[int8](entity.NoticeStatusDraft),
			PublishType: util.Ptr[int8](entity.NoticePublishTypeCustom),
			TargetType:  util.Ptr[int8](entity.NoticeTargetTypeRole),
			TargetIDs:   "31,32",
		},
		roleUsers: map[uint64][]uint64{31: {101, 102}, 32: {103}},
	}
	svc, bus := newTestNoticeService(repo)

	err := svc.Publish(context.Background(), 55, &request.PublishNoticeReq{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.publishedNotice == nil || repo.publishedNotice.Status == nil || *repo.publishedNotice.Status != entity.NoticeStatusPublished {
		t.Fatalf("published status = %v", repo.publishedNotice)
	}
	got := make(map[uint64]bool)
	for _, row := range repo.publishedRows {
		got[row.UserID] = true
	}
	for _, uid := range []uint64{101, 102, 103} {
		if !got[uid] {
			t.Fatalf("user %d missing from published rows", uid)
		}
	}
	if bus.last == nil || bus.last.IsAll {
		t.Fatalf("push event = %+v, want custom scope", bus.last)
	}
	if len(bus.last.UserIDs) != 3 {
		t.Fatalf("push user ids = %v", bus.last.UserIDs)
	}
}

func TestPublish_ExplicitIDsOverrideStoredScope(t *testing.T) {
	repo := &stubNoticeRepo{
		findByIDNotice: &entity.SysNotice{
			BaseEntity:  entity.BaseEntity{ID: 56},
			Title:       "T",
			Status:      util.Ptr[int8](entity.NoticeStatusDraft),
			PublishType: util.Ptr[int8](entity.NoticePublishTypeCustom),
			TargetType:  util.Ptr[int8](entity.NoticeTargetTypeRole),
			TargetIDs:   "31",
		},
		roleUsers: map[uint64][]uint64{31: {101}},
		deptUsers: map[uint64][]uint64{9: {999}},
	}
	svc, _ := newTestNoticeService(repo)

	err := svc.Publish(context.Background(), 56, &request.PublishNoticeReq{
		DeptIDs: []uint64{9},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.publishedRows) != 1 || repo.publishedRows[0].UserID != 999 {
		t.Fatalf("rows = %+v, want only user 999 from explicit dept", repo.publishedRows)
	}
}

func TestPublish_CustomWithoutAnyScopeFails(t *testing.T) {
	repo := &stubNoticeRepo{
		findByIDNotice: &entity.SysNotice{
			BaseEntity:  entity.BaseEntity{ID: 57},
			Title:       "T",
			Status:      util.Ptr[int8](entity.NoticeStatusDraft),
			PublishType: util.Ptr[int8](entity.NoticePublishTypeCustom),
		},
	}
	svc, _ := newTestNoticeService(repo)

	err := svc.Publish(context.Background(), 57, &request.PublishNoticeReq{})
	assertCode(t, err, apperror.CodeBadRequest)
}

// ── attachCreators(len) ────────────────────────────────

func TestFindPage_AttachesCreatorsAndTargetDesc(t *testing.T) {
	repo := &stubNoticeRepo{
		findPageList: []entity.SysNotice{
			{
				BaseEntity: entity.BaseEntity{ID: 1, CreatedBy: util.Ptr(uint64(41))},
				Title:      "A",
				TargetType: util.Ptr[int8](entity.NoticeTargetTypeAll),
			},
			{
				BaseEntity: entity.BaseEntity{ID: 2, CreatedBy: util.Ptr(uint64(42))},
				Title:      "B",
				TargetType: util.Ptr[int8](entity.NoticeTargetTypeUser),
				TargetIDs:  "71",
			},
		},
		findPageTotal: 2,
		userNames:     map[uint64]string{41: "alice", 42: "bob"},
	}
	svc, _ := newTestNoticeService(repo)

	resp, err := svc.FindPage(context.Background(), &request.NoticeQuery{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	list := resp.List.([]response.NoticeResp)
	if len(list) != 2 {
		t.Fatalf("len = %d", len(list))
	}
	if list[0].CreateBy != "alice" || list[1].CreateBy != "bob" {
		t.Fatalf("createBy = %q, %q", list[0].CreateBy, list[1].CreateBy)
	}
	if list[0].TargetDesc != "全体成员" || list[1].TargetDesc != "指定个人（1个）" {
		t.Fatalf("targetDesc = %q, %q", list[0].TargetDesc, list[1].TargetDesc)
	}
}

// ── FindReadUsers / FindTargetUsers ────────────────────

func TestFindReadUsers_NotFound(t *testing.T) {
	svc, _ := newTestNoticeService(&stubNoticeRepo{})

	_, err := svc.FindReadUsers(context.Background(), 1, &request.NoticeReadUsersQuery{})
	assertCode(t, err, apperror.CodeNotFound)
}

func TestFindReadUsers_OK(t *testing.T) {
	repo := &stubNoticeRepo{
		findByIDNotice: &entity.SysNotice{BaseEntity: entity.BaseEntity{ID: 5}},
		readUsers: []response.NoticeReadUserResp{
			{UserID: 11, Username: "alice", RealName: "Alice"},
		},
		readUsersTotal: 1,
	}
	svc, _ := newTestNoticeService(repo)

	resp, err := svc.FindReadUsers(context.Background(), 5, &request.NoticeReadUsersQuery{SearchValue: "ali"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Total != 1 {
		t.Fatalf("total = %d", resp.Total)
	}
	if repo.findReadUsers == nil || repo.findReadUsers.SearchValue != "ali" {
		t.Fatalf("query not forwarded: %+v", repo.findReadUsers)
	}
}

func TestFindTargetUsers_OrderPreserved(t *testing.T) {
	repo := &stubNoticeRepo{
		targetUsers: []entity.SysUser{
			{BaseEntity: entity.BaseEntity{ID: 3}, Username: "c3", RealName: util.Ptr("丙")},
			{BaseEntity: entity.BaseEntity{ID: 1}, Username: "a1", RealName: util.Ptr("甲")},
			{BaseEntity: entity.BaseEntity{ID: 2}, Username: "b2"},
		},
	}
	svc, _ := newTestNoticeService(repo)

	rows, err := svc.FindTargetUsers(context.Background(), "2,3,1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("len = %d", len(rows))
	}
	want := []uint64{2, 3, 1}
	for i := range want {
		if rows[i].ID != want[i] {
			t.Fatalf("rows[%d].ID = %d, want %d", i, rows[i].ID, want[i])
		}
	}
	if rows[0].RealName != "" {
		t.Fatalf("realName = %q, want empty for nil", rows[0].RealName)
	}
	if rows[1].RealName != "丙" {
		t.Fatalf("realName = %q", rows[1].RealName)
	}
}

func TestFindTargetUsers_InvalidCSV(t *testing.T) {
	svc, _ := newTestNoticeService(&stubNoticeRepo{})

	_, err := svc.FindTargetUsers(context.Background(), "1,x")
	assertCode(t, err, apperror.CodeBadRequest)
}

// ── MyNotices ─────────────────────────────────────────

func TestMyNotices_MapsReadStatusAndUnreadCount(t *testing.T) {
	read := entity.NoticeReadStatusRead
	pubAt := time.Now().Add(-time.Hour)
	content := "正文"
	typ := entity.NoticeTypeAnnounce
	pri := entity.NoticePriorityImportant
	repo := &stubNoticeRepo{
		myNoticesWithRead: []entity.NoticeWithRead{
			{
				SysNotice: entity.SysNotice{
					BaseEntity:  entity.BaseEntity{ID: 11},
					Title:       "已读公告",
					Content:     &content,
					NoticeType:  &typ,
					Priority:    &pri,
					PublishTime: &pubAt,
				},
				ReadStatus: &read,
			},
			{
				SysNotice: entity.SysNotice{BaseEntity: entity.BaseEntity{ID: 12}, Title: "未读公告"},
			},
		},
	}
	svc, _ := newTestNoticeService(repo)
	ctx := contextkeys.WithUserID(context.Background(), 42)

	resp, err := svc.MyNotices(ctx)
	if err != nil {
		t.Fatalf("MyNotices error: %v", err)
	}
	if resp.UnreadCount != 1 {
		t.Fatalf("UnreadCount = %d, want 1", resp.UnreadCount)
	}
	if len(resp.List) != 2 {
		t.Fatalf("len(List) = %d, want 2", len(resp.List))
	}
	if !resp.List[0].IsRead {
		t.Fatalf("List[0].IsRead = false, want true")
	}
	if resp.List[1].IsRead {
		t.Fatalf("List[1].IsRead = true, want false")
	}
	if resp.List[0].Title != "已读公告" || resp.List[0].Content != "正文" {
		t.Fatalf("List[0] mapping wrong: %+v", resp.List[0])
	}
	if resp.List[0].NoticeType != entity.NoticeTypeAnnounce || resp.List[0].Priority != entity.NoticePriorityImportant {
		t.Fatalf("List[0] type/priority mapping wrong: %+v", resp.List[0])
	}
	if resp.List[0].PublishTime == nil {
		t.Fatalf("List[0].PublishTime nil, want mapped")
	}
	// 未读条目缺省值兜底:类型/优先级取默认、发布时间为空。
	if resp.List[1].NoticeType != entity.NoticeTypeNotice || resp.List[1].Priority != entity.NoticePriorityNormal {
		t.Fatalf("List[1] defaults wrong: %+v", resp.List[1])
	}
}

func TestMyNotices_NoUserInCtx(t *testing.T) {
	svc, _ := newTestNoticeService(&stubNoticeRepo{})

	_, err := svc.MyNotices(context.Background())
	assertCode(t, err, apperror.CodeUnauthorized)
}

// ── MarkAllRead ────────────────────────────────────────

func TestNoticeServiceMarkAllRead(t *testing.T) {
	repo := &stubNoticeRepo{}
	svc, _ := newTestNoticeService(repo)

	if err := svc.MarkAllRead(context.Background(), 7); err != nil {
		t.Fatalf("MarkAllRead: %v", err)
	}
	if !repo.markAllReadCalled {
		t.Fatal("repo.MarkAllRead not called")
	}

	repo.markAllReadCalled = false
	repo.markAllReadErr = errors.New("db down")
	err := svc.MarkAllRead(context.Background(), 7)
	if !repo.markAllReadCalled {
		t.Fatal("repo.MarkAllRead not called on error path")
	}
	assertCode(t, err, apperror.CodeInternal)
}

// ── FindPage · 阅读状态聚合 ──────────────────────────

func TestFindPage_AttachesReadStatus(t *testing.T) {
	published := util.Ptr[int8](entity.NoticeStatusPublished)
	repo := &stubNoticeRepo{
		findPageList: []entity.SysNotice{
			{BaseEntity: entity.BaseEntity{ID: 1}, Title: "A", Status: published},
			{BaseEntity: entity.BaseEntity{ID: 2}, Title: "B", Status: util.Ptr[int8](entity.NoticeStatusPublished)},
			{BaseEntity: entity.BaseEntity{ID: 3}, Title: "C", Status: util.Ptr[int8](entity.NoticeStatusDraft)},
		},
		findPageTotal: 3,
		findReadMap:   map[uint64]int8{1: 1},
	}
	svc, _ := newTestNoticeService(repo)
	ctx := context.WithValue(context.Background(), contextkeys.UserID, uint64(9))

	resp, err := svc.FindPage(ctx, &request.NoticeQuery{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	list := resp.List.([]response.NoticeResp)
	if len(list) != 3 {
		t.Fatalf("len = %d", len(list))
	}
	// 已发布·有行→1
	if list[0].ReadStatus == nil || *list[0].ReadStatus != 1 {
		t.Fatalf("readStatus[0] = %v, want 1", list[0].ReadStatus)
	}
	// 已发布·无行→0 未读
	if list[1].ReadStatus == nil || *list[1].ReadStatus != 0 {
		t.Fatalf("readStatus[1] = %v, want 0", list[1].ReadStatus)
	}
	// 草稿→nil
	if list[2].ReadStatus != nil {
		t.Fatalf("readStatus[2] = %v, want nil", *list[2].ReadStatus)
	}
}
