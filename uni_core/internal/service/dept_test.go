package service

import (
	"context"
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"

	"gorm.io/gorm"
)

// ── 测试替身 ────────────────────────────────────────────

// stubDeptRepo 实现 DeptRepositoryInterface：内存切片，模拟真实仓库。
// UpdateSort 仅记录调用参数（真实仓库只更新 sort 一列，这里是服务层语义测试）。
type stubDeptRepo struct {
	depts  []entity.SysDept
	nextID uint64

	updateSortIDs  []uint64
	updateSortVals []int
	updateSortErr  error
}

func newStubDeptRepo(depts []entity.SysDept) *stubDeptRepo {
	return &stubDeptRepo{depts: depts, nextID: 1000}
}

func (r *stubDeptRepo) FindAll(context.Context) ([]entity.SysDept, error) {
	out := make([]entity.SysDept, len(r.depts))
	copy(out, r.depts)
	return out, nil
}

func (r *stubDeptRepo) FindByID(_ context.Context, id uint64) (*entity.SysDept, error) {
	for i := range r.depts {
		if r.depts[i].ID == id {
			d := r.depts[i]
			return &d, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (r *stubDeptRepo) Create(_ context.Context, d *entity.SysDept) error {
	if d.ID == 0 {
		d.ID = r.nextID
		r.nextID++
	}
	r.depts = append(r.depts, *d)
	return nil
}

func (r *stubDeptRepo) CreateWithAncestorsTx(ctx context.Context, d *entity.SysDept, _ string) error {
	return r.Create(ctx, d)
}

func (r *stubDeptRepo) Update(_ context.Context, d *entity.SysDept) error {
	for i := range r.depts {
		if r.depts[i].ID == d.ID {
			r.depts[i] = *d
			return nil
		}
	}
	return gorm.ErrRecordNotFound
}

func (r *stubDeptRepo) UpdateWithAncestorsTx(_ context.Context, d *entity.SysDept) error {
	return r.Update(context.Background(), d)
}

func (r *stubDeptRepo) Delete(_ context.Context, id uint64) error {
	for i := range r.depts {
		if r.depts[i].ID == id {
			r.depts = append(r.depts[:i], r.depts[i+1:]...)
			return nil
		}
	}
	return gorm.ErrRecordNotFound
}

func (r *stubDeptRepo) HasChildren(_ context.Context, id uint64) (bool, error) {
	for i := range r.depts {
		pid := uint64(0)
		if r.depts[i].ParentID != nil {
			pid = *r.depts[i].ParentID
		}
		if pid == id {
			return true, nil
		}
	}
	return false, nil
}

func (r *stubDeptRepo) HasUsers(context.Context, uint64) (bool, error) { return false, nil }
func (r *stubDeptRepo) FindChildDeptIDs(context.Context, uint64) ([]uint64, error) {
	return nil, nil
}

func (r *stubDeptRepo) UpdateSort(_ context.Context, id uint64, sort int) error {
	r.updateSortIDs = append(r.updateSortIDs, id)
	r.updateSortVals = append(r.updateSortVals, sort)
	return r.updateSortErr
}

func newTestDeptService(repo DeptRepositoryInterface) *DeptService {
	return NewDeptService(repo, logger.NewNop())
}

// ── UpdateSort 用例 ─────────────────────────────────────

func TestDeptUpdateSort_BatchOverwrite(t *testing.T) {
	repo := newStubDeptRepo([]entity.SysDept{
		{BaseEntity: entity.BaseEntity{ID: 10}, Name: "A", Sort: util.Ptr(1)},
		{BaseEntity: entity.BaseEntity{ID: 11}, Name: "B", Sort: util.Ptr(2)},
		{BaseEntity: entity.BaseEntity{ID: 12}, Name: "C", Sort: util.Ptr(3)},
	})
	svc := newTestDeptService(repo)
	req := &request.UpdateDeptSortReq{
		Items: []request.DeptSortItem{
			{ID: 10, Sort: 3},
			{ID: 11, Sort: 1},
			{ID: 12, Sort: 2},
		},
	}
	if err := svc.UpdateSort(context.Background(), req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.updateSortIDs) != 3 {
		t.Fatalf("expected 3 UpdateSort calls, got %d", len(repo.updateSortIDs))
	}
	// 调用顺序与请求条目一致（批量覆盖，不重排）。
	wantIDs := []uint64{10, 11, 12}
	wantVals := []int{3, 1, 2}
	for i := range wantIDs {
		if repo.updateSortIDs[i] != wantIDs[i] || repo.updateSortVals[i] != wantVals[i] {
			t.Fatalf("call %d: got id=%d sort=%d, want id=%d sort=%d",
				i, repo.updateSortIDs[i], repo.updateSortVals[i], wantIDs[i], wantVals[i])
		}
	}
}

func TestDeptUpdateSort_DeptNotFound(t *testing.T) {
	repo := newStubDeptRepo([]entity.SysDept{
		{BaseEntity: entity.BaseEntity{ID: 10}, Name: "A"},
	})
	svc := newTestDeptService(repo)
	req := &request.UpdateDeptSortReq{
		Items: []request.DeptSortItem{{ID: 999, Sort: 1}},
	}
	err := svc.UpdateSort(context.Background(), req)
	assertCode(t, err, apperror.CodeNotFound)
	if len(repo.updateSortIDs) != 0 {
		t.Fatal("UpdateSort should not be called when dept not found")
	}
}

// ── FindTree 过滤用例 ───────────────────────────────────

// seedDeptTree 构造一棵三层部门树：总公司→研发部→前端组，另有一棵独立的空分公司。
func seedDeptTree() []entity.SysDept {
	return []entity.SysDept{
		{BaseEntity: entity.BaseEntity{ID: 1}, ParentID: util.Ptr(uint64(0)), Name: "总公司", Status: util.Ptr(int8(1))},
		{BaseEntity: entity.BaseEntity{ID: 2}, ParentID: util.Ptr(uint64(1)), Name: "研发部", Status: util.Ptr(int8(1))},
		{BaseEntity: entity.BaseEntity{ID: 3}, ParentID: util.Ptr(uint64(2)), Name: "前端组", Status: util.Ptr(int8(1))},
		{BaseEntity: entity.BaseEntity{ID: 4}, ParentID: util.Ptr(uint64(0)), Name: "分公司", Status: util.Ptr(int8(0))},
	}
}

func TestDeptFindTree_FilterByNameKeepsAncestors(t *testing.T) {
	repo := newStubDeptRepo(seedDeptTree())
	svc := newTestDeptService(repo)

	tree, err := svc.FindTree(context.Background(), request.DeptQuery{Name: "前端"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tree) != 1 || tree[0].Name != "总公司" {
		t.Fatalf("root = %+v, want single 总公司", tree)
	}
	if len(tree[0].Children) != 1 || tree[0].Children[0].Name != "研发部" {
		t.Fatalf("children of 总公司 = %+v, want [研发部]", tree[0].Children)
	}
	if len(tree[0].Children[0].Children) != 1 || tree[0].Children[0].Children[0].Name != "前端组" {
		t.Fatalf("children of 研发部 = %+v, want [前端组]", tree[0].Children[0].Children)
	}
}

func TestDeptFindTree_FilterByNameMatchesAncestor(t *testing.T) {
	// 命中祖先节点（研发部）时保留其完整子树，不因筛选而丢子。
	repo := newStubDeptRepo(seedDeptTree())
	svc := newTestDeptService(repo)

	tree, err := svc.FindTree(context.Background(), request.DeptQuery{Name: "研发"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tree) != 1 || tree[0].Name != "总公司" {
		t.Fatalf("root = %+v, want single 总公司", tree)
	}
	dev := tree[0].Children[0]
	if dev.Name != "研发部" || len(dev.Children) != 1 || dev.Children[0].Name != "前端组" {
		t.Fatalf("研发部 subtree = %+v, want full subtree", dev)
	}
}

func TestDeptFindTree_FilterByStatus(t *testing.T) {
	repo := newStubDeptRepo(seedDeptTree())
	svc := newTestDeptService(repo)

	tree, err := svc.FindTree(context.Background(), request.DeptQuery{Status: util.Ptr(int8(0))})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tree) != 1 || tree[0].Name != "分公司" {
		t.Fatalf("root = %+v, want single 分公司", tree)
	}
}

func TestDeptFindTree_NoFilterReturnsFullTree(t *testing.T) {
	repo := newStubDeptRepo(seedDeptTree())
	svc := newTestDeptService(repo)

	tree, err := svc.FindTree(context.Background(), request.DeptQuery{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tree) != 2 {
		t.Fatalf("root count = %d, want 2", len(tree))
	}
}

// TestDeptFindTree_PartialListBuildsLocalRoot 数据范围过滤只会返回「部门 + 其子孙」
// 的子树、不含祖先链,建树时必须把父节点被裁剪掉的部门作为局部根,否则
// 非根部门用户(如「本部门及以下」)会得到空树。
func TestDeptFindTree_PartialListBuildsLocalRoot(t *testing.T) {
	repo := newStubDeptRepo([]entity.SysDept{
		{BaseEntity: entity.BaseEntity{ID: 2}, ParentID: util.Ptr(uint64(1)), Name: "研发部"},
		{BaseEntity: entity.BaseEntity{ID: 3}, ParentID: util.Ptr(uint64(2)), Name: "前端组"},
	})
	svc := newTestDeptService(repo)

	tree, err := svc.FindTree(context.Background(), request.DeptQuery{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tree) != 1 {
		t.Fatalf("root count = %d, want 1 (研发部 作为局部根)", len(tree))
	}
	dev := tree[0]
	if dev.ID != 2 || dev.Name != "研发部" {
		t.Fatalf("root = %+v, want 研发部", dev)
	}
	if len(dev.Children) != 1 || dev.Children[0].ID != 3 || dev.Children[0].Name != "前端组" {
		t.Fatalf("children of 研发部 = %+v, want [前端组]", dev.Children)
	}
}
