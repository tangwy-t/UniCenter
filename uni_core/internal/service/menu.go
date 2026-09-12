package service

import (
	"context"
	"strings"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"

	"go.uber.org/zap"
)

// MenuRepositoryInterface 由 service/interface.go 迁移至此:接口定义在消费方,
// 不再维护包级中央接口文件。
// MenuRepositoryInterface defines the data-access contract for menu management operations.
type MenuRepositoryInterface interface {
	FindAll(ctx context.Context) ([]entity.SysMenu, error)
	FindByID(ctx context.Context, id uint64) (*entity.SysMenu, error)
	Create(ctx context.Context, menu *entity.SysMenu) error
	Update(ctx context.Context, menu *entity.SysMenu) error
	Delete(ctx context.Context, id uint64) error
	HasChildren(ctx context.Context, id uint64) (bool, error)
}

type MenuService struct {
	repo         MenuRepositoryInterface
	logger       logger.LoggerInterface
	sessionStore SessionStoreInterface
}

// NewMenuService constructs a MenuService with the given dependencies.
// Snowflake ID generation is handled by GORM callbacks (database.RegisterIDCallback).
func NewMenuService(repo MenuRepositoryInterface, sessionStore SessionStoreInterface, logger logger.LoggerInterface) *MenuService {
	return &MenuService{repo: repo, sessionStore: sessionStore, logger: logger}
}

// invalidateAllPerms 菜单权限点(perms)变更后失效全部用户权限缓存。
// 失败只告警不阻断:缓存最终一致(最迟 accessExpire 过期),而菜单写操作
// 本身已成功,不应因缓存清理失败回滚业务。
func (s *MenuService) invalidateAllPerms(ctx context.Context, op string) {
	if s.sessionStore == nil {
		return
	}
	if err := s.sessionStore.RevokeAllPerms(ctx); err != nil {
		s.logger.Warn("failed to revoke all perms cache after menu change",
			zap.String("op", op), zap.Error(err))
	}
}

func (s *MenuService) FindTree(ctx context.Context, query request.MenuQuery) ([]response.MenuResp, error) {
	menus, err := s.repo.FindAll(ctx)
	if err != nil {
		return nil, err
	}
	tree := s.buildMenuTree(menus)
	// 无筛选条件时直接返回全树，行为与历史版本一致。
	if query.Name == "" && query.Status == nil && query.Type == "" {
		return tree, nil
	}
	return s.filterMenuTree(tree, query), nil
}

// filterMenuTree 对已构建的菜单树按查询条件裁剪：
// 保留「自身匹配」或「存在匹配的后代」的节点，使命中子节点时其祖先链路不丢失。
func (s *MenuService) filterMenuTree(tree []response.MenuResp, query request.MenuQuery) []response.MenuResp {
	result := make([]response.MenuResp, 0, len(tree))
	for _, node := range tree {
		// 先递归处理子节点，判断该分支是否含命中节点。
		children := s.filterMenuTree(node.Children, query)
		childrenMatch := len(children) > 0
		selfMatch := s.menuMatches(node, query)

		if selfMatch {
			// 自身命中：保留完整子树（沿用原 children，不因筛选而丢子）。
			result = append(result, node)
			continue
		}
		if childrenMatch {
			node.Children = children
			result = append(result, node)
		}
	}
	return result
}

// menuMatches 判断单个节点是否命中查询条件（名称模糊、状态精确、类型精确）。
func (s *MenuService) menuMatches(node response.MenuResp, query request.MenuQuery) bool {
	if query.Name != "" && !strings.Contains(strings.ToLower(node.Name), strings.ToLower(query.Name)) {
		return false
	}
	if query.Status != nil && node.Status != *query.Status {
		return false
	}
	if query.Type != "" && node.Type != query.Type {
		return false
	}
	return true
}

func (s *MenuService) FindByID(ctx context.Context, id uint64) (*response.MenuResp, error) {
	menu, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, translateNotFound(err, "菜单不存在")
	}
	resp := util.MapEntity[response.MenuResp](menu, s.logger)
	return &resp, nil
}

// sortOf returns the effective sort value of a menu (nil → 0).
func sortOf(m *entity.SysMenu) int {
	if m.Sort == nil {
		return 0
	}
	return *m.Sort
}

// parentOf returns the effective parent id of a menu (nil → 0, i.e. root).
func parentOf(m *entity.SysMenu) uint64 {
	if m.ParentID == nil {
		return 0
	}
	return *m.ParentID
}

// resolveSort computes the sort value to assign to a menu so that the requested
// position is honored exactly:
//
//   - requested == nil → append at the end of the sibling group (max sort + 1),
//     so a menu created without an explicit sort never displaces existing ones;
//   - requested != nil → every sibling (except excludeID) whose sort >= requested
//     is shifted down by one, making the requested value that menu's exact
//     position — sort=0 therefore always lands at the very top, even when other
//     siblings already hold 0 (previously they tied and the id took precedence,
//     so a menu could never be moved to the top).
func (s *MenuService) resolveSort(ctx context.Context, parentID, excludeID uint64, requested *int) (int, error) {
	all, err := s.repo.FindAll(ctx)
	if err != nil {
		return 0, err
	}
	if requested == nil {
		maxSort := -1
		for i := range all {
			m := &all[i]
			if m.ID == excludeID || parentOf(m) != parentID {
				continue
			}
			if sv := sortOf(m); sv > maxSort {
				maxSort = sv
			}
		}
		return maxSort + 1, nil
	}

	target := *requested
	for i := range all {
		m := &all[i]
		if m.ID == excludeID || parentOf(m) != parentID {
			continue
		}
		if sv := sortOf(m); sv >= target {
			next := sv + 1
			m.Sort = &next
			if err := s.repo.Update(ctx, m); err != nil {
				return 0, err
			}
		}
	}
	return target, nil
}

func (s *MenuService) Create(ctx context.Context, req *request.CreateMenuReq) (uint64, error) {
	// ID is auto-generated by the GORM BeforeCreate callback (database.RegisterIDCallback).
	menu := &entity.SysMenu{}
	// ParentID is *util.JsonUint64 in the request to preserve snowflake-ID
	// precision; copier converts it to the entity's *uint64 automatically
	// (behavior locked by util.TestCopyEntityJsonUint64Pointer).
	util.CopyEntity(menu, req, s.logger)

	// Defaults.
	if menu.Visible == nil {
		v := entity.MenuVisible
		menu.Visible = &v
	}
	if menu.Status == nil {
		enabled := entity.MenuStatusEnabled
		menu.Status = &enabled
	}

	// Resolve the sort position: explicit value → exact position (siblings with
	// the same or larger sort are shifted down), omitted → append at the end.
	sortValue, err := s.resolveSort(ctx, parentOf(menu), 0, req.Sort)
	if err != nil {
		return 0, err
	}
	menu.Sort = &sortValue

	if err := s.repo.Create(ctx, menu); err != nil {
		s.logger.Warn("failed to create menu", zap.String("name", req.Name), zap.Error(err))
		return 0, err
	}
	s.logger.Info("menu created", zap.Uint64("menuId", menu.ID), zap.String("name", menu.Name))
	return menu.ID, nil
}

func (s *MenuService) Update(ctx context.Context, req *request.UpdateMenuReq) error {
	menu, err := s.repo.FindByID(ctx, req.ID)
	if err != nil {
		return translateNotFound(err, "菜单不存在")
	}

	oldParentID := uint64(0)
	if menu.ParentID != nil {
		oldParentID = *menu.ParentID
	}
	oldSort := sortOf(menu)

	// ParentID is *util.JsonUint64 in the request to preserve snowflake-ID
	// precision; copier converts it to the entity's *uint64 automatically
	// (behavior locked by util.TestCopyEntityJsonUint64Pointer).
	util.CopyEntity(menu, req, s.logger)

	// Circular reference detection when parentID changes.
	newParentID := uint64(0)
	if menu.ParentID != nil {
		newParentID = *menu.ParentID
	}
	if newParentID != oldParentID {
		if menu.ParentID != nil && *menu.ParentID == menu.ID {
			return apperror.BadRequest("不能将菜单移动到自身下")
		}
		if menu.ParentID != nil && *menu.ParentID != 0 {
			allMenus, err := s.repo.FindAll(ctx)
			if err != nil {
				return err
			}
			if isDescendant(allMenus, *menu.ParentID, menu.ID) {
				return apperror.BadRequest("不能将菜单移动到其子菜单下")
			}
		}
	}

	// Resolve the sort position. Only shift siblings when the parent changed or
	// the sort value actually changed — saving an unchanged form must not
	// re-run the insertion shift (it would wrongly bump unrelated siblings).
	if req.Sort != nil && (newParentID != oldParentID || *req.Sort != oldSort) {
		sortValue, err := s.resolveSort(ctx, newParentID, req.ID, req.Sort)
		if err != nil {
			return err
		}
		menu.Sort = &sortValue
	}

	if err := s.repo.Update(ctx, menu); err != nil {
		s.logger.Warn("failed to update menu", zap.Uint64("menuId", req.ID), zap.Error(err))
		return err
	}
	// perms 字段变更影响所有引用此菜单的角色 → 全部用户,全量失效缓存。
	s.invalidateAllPerms(ctx, "update")
	s.logger.Info("menu updated", zap.Uint64("menuId", req.ID))
	return nil
}

func (s *MenuService) Delete(ctx context.Context, id uint64) error {
	_, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return translateNotFound(err, "菜单不存在")
	}

	hasChildren, err := s.repo.HasChildren(ctx, id)
	if err != nil {
		return err
	}
	if hasChildren {
		return apperror.BadRequest("请先删除子菜单")
	}

	if err := s.repo.Delete(ctx, id); err != nil {
		s.logger.Warn("failed to delete menu", zap.Uint64("menuId", id), zap.Error(err))
		return err
	}
	// 删除菜单同样移除了其权限点,全量失效权限缓存。
	s.invalidateAllPerms(ctx, "delete")
	s.logger.Info("menu deleted", zap.Uint64("menuId", id))
	return nil
}

// UpdateSort 批量应用排序值（「保存排序」）。
// 与 Update 的「精确落位(shift 兄弟)」语义不同：这里对每个条目直接覆盖赋值，
// 因为批量保存时用户在表格里已排好最终顺序，不应再触发逐条 shift 造成反复搬移。
func (s *MenuService) UpdateSort(ctx context.Context, req *request.UpdateMenuSortReq) error {
	for _, item := range req.Items {
		id := uint64(item.ID)
		menu, err := s.repo.FindByID(ctx, id)
		if err != nil {
			return translateNotFound(err, "菜单不存在")
		}
		sortValue := item.Sort
		menu.Sort = &sortValue
		if err := s.repo.Update(ctx, menu); err != nil {
			s.logger.Warn("failed to update menu sort", zap.Uint64("menuId", id), zap.Error(err))
			return err
		}
	}
	s.logger.Info("menu sort updated", zap.Int("count", len(req.Items)))
	return nil
}

// buildMenuTree builds a tree structure from a flat list of menus.
func (s *MenuService) buildMenuTree(menus []entity.SysMenu) []response.MenuResp {
	// 1. Convert entity to response.
	list := make([]response.MenuResp, 0, len(menus))
	for i := range menus {
		list = append(list, util.MapEntity[response.MenuResp](&menus[i], s.logger))
	}

	// 2. Group by parentID.
	childrenMap := make(map[uint64][]response.MenuResp)
	for _, item := range list {
		childrenMap[item.ParentID] = append(childrenMap[item.ParentID], item)
	}

	// 3. Recursively build children.
	var build func(parentID uint64) []response.MenuResp
	build = func(parentID uint64) []response.MenuResp {
		children := childrenMap[parentID]
		if len(children) == 0 {
			return nil
		}
		result := make([]response.MenuResp, 0, len(children))
		for _, child := range children {
			child.Children = build(child.ID)
			if child.Children == nil {
				child.Children = []response.MenuResp{}
			}
			result = append(result, child)
		}
		return result
	}

	// 顶层兜底：空树返回 [] 而非 nil，避免 Go 将 nil slice 序列化为 JSON null。
	root := build(0)
	if root == nil {
		return []response.MenuResp{}
	}
	return root
}

// isDescendant returns true if descendantID is (transitively) a descendant of ancestorID
// in the given flat menu list.
func isDescendant(menus []entity.SysMenu, descendantID, ancestorID uint64) bool {
	parentMap := make(map[uint64]uint64, len(menus))
	for _, m := range menus {
		pid := uint64(0)
		if m.ParentID != nil {
			pid = *m.ParentID
		}
		parentMap[m.ID] = pid
	}
	current := descendantID
	for current != 0 {
		if current == ancestorID {
			return true
		}
		current = parentMap[current]
	}
	return false
}
