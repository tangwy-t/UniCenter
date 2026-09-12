package service

import (
	"context"
	"sort"
	"testing"

	"gorm.io/gorm"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// ── 测试替身 ────────────────────────────────────────────

// stubMenuRepo 实现 MenuRepositoryInterface：内存切片，模拟真实仓库
// （每次 FindAll 返回快照副本，Update 后持久化）。
type stubMenuRepo struct {
	menus  []entity.SysMenu
	nextID uint64
}

func newStubMenuRepo(menus []entity.SysMenu) *stubMenuRepo {
	return &stubMenuRepo{menus: menus, nextID: 1000}
}

func (r *stubMenuRepo) FindAll(context.Context) ([]entity.SysMenu, error) {
	out := make([]entity.SysMenu, len(r.menus))
	copy(out, r.menus)
	return out, nil
}

func (r *stubMenuRepo) FindByID(_ context.Context, id uint64) (*entity.SysMenu, error) {
	for i := range r.menus {
		if r.menus[i].ID == id {
			m := r.menus[i]
			return &m, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

func (r *stubMenuRepo) Create(_ context.Context, m *entity.SysMenu) error {
	if m.ID == 0 {
		m.ID = r.nextID
		r.nextID++
	}
	r.menus = append(r.menus, *m)
	return nil
}

func (r *stubMenuRepo) Update(_ context.Context, m *entity.SysMenu) error {
	for i := range r.menus {
		if r.menus[i].ID == m.ID {
			r.menus[i] = *m
			return nil
		}
	}
	return gorm.ErrRecordNotFound
}

func (r *stubMenuRepo) Delete(_ context.Context, id uint64) error {
	for i := range r.menus {
		if r.menus[i].ID == id {
			r.menus = append(r.menus[:i], r.menus[i+1:]...)
			return nil
		}
	}
	return gorm.ErrRecordNotFound
}

func (r *stubMenuRepo) HasChildren(_ context.Context, id uint64) (bool, error) {
	for i := range r.menus {
		if parentOf(&r.menus[i]) == id {
			return true, nil
		}
	}
	return false, nil
}

func newTestMenuService(repo MenuRepositoryInterface) *MenuService {
	return NewMenuService(repo, nil, logger.NewNop())
}

// sortOfID 返回指定菜单的当前 sort 值（不存在返回 -1）。
func sortOfID(t *testing.T, repo *stubMenuRepo, id uint64) int {
	t.Helper()
	all, err := repo.FindAll(context.Background())
	if err != nil {
		t.Fatalf("FindAll error: %v", err)
	}
	for i := range all {
		if all[i].ID == id {
			return sortOf(&all[i])
		}
	}
	return -1
}

// siblingSorts 返回 parentID 下所有兄弟节点（不含指定 ID）的 sort 列表，
// 按真实仓库的查询顺序排序（sort ASC, id ASC）。
func siblingSorts(t *testing.T, repo *stubMenuRepo, parentID, excludeID uint64) []int {
	t.Helper()
	all, err := repo.FindAll(context.Background())
	if err != nil {
		t.Fatalf("FindAll error: %v", err)
	}
	var siblings []entity.SysMenu
	for i := range all {
		m := &all[i]
		if m.ID == excludeID || parentOf(m) != parentID {
			continue
		}
		siblings = append(siblings, *m)
	}
	sort.Slice(siblings, func(i, j int) bool {
		if sortOf(&siblings[i]) != sortOf(&siblings[j]) {
			return sortOf(&siblings[i]) < sortOf(&siblings[j])
		}
		return siblings[i].ID < siblings[j].ID
	})
	sorts := make([]int, 0, len(siblings))
	for i := range siblings {
		sorts = append(sorts, sortOf(&siblings[i]))
	}
	return sorts
}

// ── resolveSort 用例 ────────────────────────────────────

func TestResolveSortAppendAtEnd(t *testing.T) {
	repo := newStubMenuRepo([]entity.SysMenu{
		{BaseEntity: entity.BaseEntity{ID: 1}, ParentID: util.Ptr(uint64(0)), Sort: util.Ptr(0)},
		{BaseEntity: entity.BaseEntity{ID: 2}, ParentID: util.Ptr(uint64(0)), Sort: util.Ptr(1)},
		{BaseEntity: entity.BaseEntity{ID: 3}, ParentID: util.Ptr(uint64(0)), Sort: util.Ptr(2)},
		{BaseEntity: entity.BaseEntity{ID: 4}, ParentID: util.Ptr(uint64(1)), Sort: util.Ptr(9)},
	})
	svc := newTestMenuService(repo)

	got, err := svc.resolveSort(context.Background(), 0, 0, nil)
	if err != nil {
		t.Fatalf("resolveSort error: %v", err)
	}
	if got != 3 {
		t.Fatalf("append sort = %d, want 3", got)
	}
	// 未传 sort 不应产生任何位移。
	if sorts := siblingSorts(t, repo, 0, 0); !equalInts(sorts, []int{0, 1, 2}) {
		t.Fatalf("siblings after append = %v, want [0 1 2]", sorts)
	}
}

func TestResolveSortToTop(t *testing.T) {
	// 已有兄弟占据 sort=0 —— 原始实现下无法再插入到它前面。
	repo := newStubMenuRepo([]entity.SysMenu{
		{BaseEntity: entity.BaseEntity{ID: 1}, ParentID: util.Ptr(uint64(0)), Sort: util.Ptr(0)},
		{BaseEntity: entity.BaseEntity{ID: 2}, ParentID: util.Ptr(uint64(0)), Sort: util.Ptr(1)},
		{BaseEntity: entity.BaseEntity{ID: 3}, ParentID: util.Ptr(uint64(0)), Sort: util.Ptr(2)},
	})
	svc := newTestMenuService(repo)

	got, err := svc.resolveSort(context.Background(), 0, 0, util.Ptr(0))
	if err != nil {
		t.Fatalf("resolveSort error: %v", err)
	}
	if got != 0 {
		t.Fatalf("top sort = %d, want 0", got)
	}
	// 原 sort >= 0 的兄弟全部后移一位。[1 2 3]
	if sorts := siblingSorts(t, repo, 0, 0); !equalInts(sorts, []int{1, 2, 3}) {
		t.Fatalf("siblings after top insert = %v, want [1 2 3]", sorts)
	}
}

func TestResolveSortMidInsert(t *testing.T) {
	repo := newStubMenuRepo([]entity.SysMenu{
		{BaseEntity: entity.BaseEntity{ID: 1}, ParentID: util.Ptr(uint64(0)), Sort: util.Ptr(0)},
		{BaseEntity: entity.BaseEntity{ID: 2}, ParentID: util.Ptr(uint64(0)), Sort: util.Ptr(2)},
		{BaseEntity: entity.BaseEntity{ID: 3}, ParentID: util.Ptr(uint64(0)), Sort: util.Ptr(5)},
	})
	svc := newTestMenuService(repo)

	got, err := svc.resolveSort(context.Background(), 0, 0, util.Ptr(2))
	if err != nil {
		t.Fatalf("resolveSort error: %v", err)
	}
	if got != 2 {
		t.Fatalf("mid sort = %d, want 2", got)
	}
	if sorts := siblingSorts(t, repo, 0, 0); !equalInts(sorts, []int{0, 3, 6}) {
		t.Fatalf("siblings after mid insert = %v, want [0 3 6]", sorts)
	}
}

func TestResolveSortIgnoresOtherParentsAndSelf(t *testing.T) {
	repo := newStubMenuRepo([]entity.SysMenu{
		// 另一个父级下的 sort=0 兄弟，不应被位移。
		{BaseEntity: entity.BaseEntity{ID: 1}, ParentID: util.Ptr(uint64(9)), Sort: util.Ptr(0)},
		// 待排除的自身。
		{BaseEntity: entity.BaseEntity{ID: 2}, ParentID: util.Ptr(uint64(0)), Sort: util.Ptr(0)},
		{BaseEntity: entity.BaseEntity{ID: 3}, ParentID: util.Ptr(uint64(0)), Sort: util.Ptr(1)},
	})
	svc := newTestMenuService(repo)

	got, err := svc.resolveSort(context.Background(), 0, 2, util.Ptr(0))
	if err != nil {
		t.Fatalf("resolveSort error: %v", err)
	}
	if got != 0 {
		t.Fatalf("sort = %d, want 0", got)
	}
	// 被排除的自身(原 sort=0)不被位移。
	if got := sortOfID(t, repo, 2); got != 0 {
		t.Fatalf("excluded self sort = %d, want 0 (untouched)", got)
	}
	// 同父级兄弟 sort>=0 → 全部后移:原 1 → 2。
	if sorts := siblingSorts(t, repo, 0, 2); !equalInts(sorts, []int{2}) {
		t.Fatalf("root siblings = %v, want [2]", sorts)
	}
	// 其他父级兄弟保持原样。
	if sorts := siblingSorts(t, repo, 9, 2); !equalInts(sorts, []int{0}) {
		t.Fatalf("other-parent siblings = %v, want [0]", sorts)
	}
}

// ── Create 用例 ─────────────────────────────────────────

func TestCreateOmittedSortAppendsAtEnd(t *testing.T) {
	repo := newStubMenuRepo([]entity.SysMenu{
		{BaseEntity: entity.BaseEntity{ID: 1}, ParentID: util.Ptr(uint64(0)), Name: "A", Sort: util.Ptr(1)},
		{BaseEntity: entity.BaseEntity{ID: 2}, ParentID: util.Ptr(uint64(0)), Name: "B", Sort: util.Ptr(3)},
	})
	svc := newTestMenuService(repo)

	id, err := svc.Create(context.Background(), &request.CreateMenuReq{
		Name: "C", Type: "menu", // Sort == nil → 追加
	})
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if got := sortOfID(t, repo, id); got != 4 {
		t.Fatalf("new menu sort = %d, want 4 (append)", got)
	}
	// 原有兄弟未被位移。
	if sorts := siblingSorts(t, repo, 0, 0); !equalInts(sorts, []int{1, 3, 4}) {
		t.Fatalf("siblings = %v, want [1 3 4]", sorts)
	}
}

func TestCreateExplicitZeroSortLandsAtTop(t *testing.T) {
	repo := newStubMenuRepo([]entity.SysMenu{
		{BaseEntity: entity.BaseEntity{ID: 1}, ParentID: util.Ptr(uint64(0)), Name: "A", Sort: util.Ptr(0)},
		{BaseEntity: entity.BaseEntity{ID: 2}, ParentID: util.Ptr(uint64(0)), Name: "B", Sort: util.Ptr(1)},
	})
	svc := newTestMenuService(repo)

	id, err := svc.Create(context.Background(), &request.CreateMenuReq{
		Name: "C", Type: "menu", Sort: util.Ptr(0),
	})
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if got := sortOfID(t, repo, id); got != 0 {
		t.Fatalf("new menu sort = %d, want 0", got)
	}
	// 原 sort=0 的兄弟被顶到 1，原 1 被顶到 2。
	if sorts := siblingSorts(t, repo, 0, 0); !equalInts(sorts, []int{0, 1, 2}) {
		t.Fatalf("siblings = %v, want [0 1 2]", sorts)
	}
}

// ── Update 用例 ─────────────────────────────────────────

func TestUpdateMovesMenuToTop(t *testing.T) {
	repo := newStubMenuRepo([]entity.SysMenu{
		{BaseEntity: entity.BaseEntity{ID: 1}, ParentID: util.Ptr(uint64(0)), Name: "Top", Sort: util.Ptr(0)},
		{BaseEntity: entity.BaseEntity{ID: 2}, ParentID: util.Ptr(uint64(0)), Name: "Mid", Sort: util.Ptr(1)},
		{BaseEntity: entity.BaseEntity{ID: 3}, ParentID: util.Ptr(uint64(0)), Name: "X", Sort: util.Ptr(5)},
	})
	svc := newTestMenuService(repo)

	// 把 X(3) 的排序改为 0 —— 修复前它永远排在 Top(1) 之后。
	err := svc.Update(context.Background(), &request.UpdateMenuReq{
		ID: 3, Name: "X", Type: "menu", Sort: util.Ptr(0),
	})
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if got := sortOfID(t, repo, 3); got != 0 {
		t.Fatalf("moved menu sort = %d, want 0", got)
	}
	// 原 Top 被顶到 1，Mid 被顶到 2。
	if got := sortOfID(t, repo, 1); got != 1 {
		t.Fatalf("Top sort = %d, want 1", got)
	}
	if got := sortOfID(t, repo, 2); got != 2 {
		t.Fatalf("Mid sort = %d, want 2", got)
	}
}

func TestUpdateUnchangedSortDoesNotShift(t *testing.T) {
	repo := newStubMenuRepo([]entity.SysMenu{
		{BaseEntity: entity.BaseEntity{ID: 1}, ParentID: util.Ptr(uint64(0)), Name: "A", Sort: util.Ptr(0)},
		{BaseEntity: entity.BaseEntity{ID: 2}, ParentID: util.Ptr(uint64(0)), Name: "B", Sort: util.Ptr(2)},
	})
	svc := newTestMenuService(repo)

	err := svc.Update(context.Background(), &request.UpdateMenuReq{
		ID: 2, Name: "B2", Type: "menu", Sort: util.Ptr(2), // 排序未变
	})
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if got := sortOfID(t, repo, 1); got != 0 {
		t.Fatalf("A sort = %d, want 0 (untouched)", got)
	}
	if got := sortOfID(t, repo, 2); got != 2 {
		t.Fatalf("B sort = %d, want 2 (untouched)", got)
	}
}

func TestUpdateMoveToNewParentShiftsNewGroupOnly(t *testing.T) {
	repo := newStubMenuRepo([]entity.SysMenu{
		// 旧父级组。
		{BaseEntity: entity.BaseEntity{ID: 1}, ParentID: util.Ptr(uint64(10)), Name: "A1", Sort: util.Ptr(0)},
		{BaseEntity: entity.BaseEntity{ID: 2}, ParentID: util.Ptr(uint64(10)), Name: "X", Sort: util.Ptr(1)},
		// 新父级组。
		{BaseEntity: entity.BaseEntity{ID: 3}, ParentID: util.Ptr(uint64(20)), Name: "B1", Sort: util.Ptr(0)},
		{BaseEntity: entity.BaseEntity{ID: 4}, ParentID: util.Ptr(uint64(20)), Name: "B2", Sort: util.Ptr(2)},
	})
	svc := newTestMenuService(repo)

	// 把 X(2) 从父级 10 移到父级 20，并置顶。
	newParent := util.JsonUint64(20)
	err := svc.Update(context.Background(), &request.UpdateMenuReq{
		ID: 2, Name: "X", Type: "menu", ParentID: &newParent, Sort: util.Ptr(0),
	})
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if got := sortOfID(t, repo, 2); got != 0 {
		t.Fatalf("X sort = %d, want 0", got)
	}
	// 新组原 sort>=0 的兄弟被顶开。
	if sorts := siblingSorts(t, repo, 20, 2); !equalInts(sorts, []int{1, 3}) {
		t.Fatalf("new-parent siblings = %v, want [1 3]", sorts)
	}
	// 旧组的兄弟不受影响。
	if sorts := siblingSorts(t, repo, 10, 2); !equalInts(sorts, []int{0}) {
		t.Fatalf("old-parent siblings = %v, want [0]", sorts)
	}
}

// ── 工具 ────────────────────────────────────────────────

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
