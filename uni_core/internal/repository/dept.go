package repository

import (
	"context"
	"strconv"

	"github.com/tangwy-t/webmanager-server/internal/model/entity"

	"gorm.io/gorm"
)

type DeptRepo struct {
	db *gorm.DB
}

// NewDeptRepository returns a DeptRepository backed by the given *gorm.DB.
func NewDeptRepository(db *gorm.DB) *DeptRepo {
	return &DeptRepo{db: db}
}

func (r *DeptRepo) FindAll(ctx context.Context) ([]entity.SysDept, error) {
	var depts []entity.SysDept
	err := r.db.WithContext(ctx).Order("sort ASC, id ASC").Find(&depts).Error
	return depts, err
}

func (r *DeptRepo) FindByID(ctx context.Context, id uint64) (*entity.SysDept, error) {
	var dept entity.SysDept
	err := r.db.WithContext(ctx).First(&dept, id).Error
	if err != nil {
		return nil, err
	}
	return &dept, nil
}

// FindExistingIDs returns the subset of ids that exist in sys_dept.
// Used by the service layer to validate deptIDs before an Association write
// (which would otherwise upsert a phantom dept for an unknown ID).
func (r *DeptRepo) FindExistingIDs(ctx context.Context, ids []uint64) ([]uint64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var existing []uint64
	err := r.db.WithContext(ctx).Model(&entity.SysDept{}).
		Where("id IN ?", ids).Pluck("id", &existing).Error
	return existing, err
}

func (r *DeptRepo) Create(ctx context.Context, dept *entity.SysDept) error {
	return r.db.WithContext(ctx).Create(dept).Error
}

// CreateWithAncestorsTx atomizes Create + UpdateAncestors in one transaction.
// dept.ID is assigned by the GORM id:generate callback during Create, so the
// ancestors string (which ends with dept.ID) can only be finalized inside the
// transaction. Caller must validate parent existence beforehand.
// parentAncestors is "" for root departments.
func (r *DeptRepo) CreateWithAncestorsTx(ctx context.Context, dept *entity.SysDept, parentAncestors string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(dept).Error; err != nil {
			return err
		}
		ancestors := strconv.FormatUint(dept.ID, 10)
		if parentAncestors != "" {
			ancestors = parentAncestors + "," + ancestors
		}
		return tx.Model(&entity.SysDept{}).Where("id = ?", dept.ID).
			Update("ancestors", ancestors).Error
	})
}

func (r *DeptRepo) Update(ctx context.Context, dept *entity.SysDept) error {
	return r.db.WithContext(ctx).Model(dept).Updates(dept).Error
}

// UpdateWithAncestorsTx atomizes Update + descendant ancestor recalculation in
// one transaction. Without this, Update could succeed while
// UpdateDescendantAncestors fails, leaving the subtree pointing at a stale
// parent — breaking tree queries and dept-dimension data scope resolution.
func (r *DeptRepo) UpdateWithAncestorsTx(ctx context.Context, dept *entity.SysDept) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(dept).Updates(dept).Error; err != nil {
			return err
		}
		return r.updateDescendantAncestorsTx(ctx, tx, dept.ID, dept.Ancestors)
	})
}

func (r *DeptRepo) Delete(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Delete(&entity.SysDept{}, id).Error
}

// UpdateSort 仅更新 sort 一列（「保存排序」批量提交），不触碰 ancestors/名称等
// 其他字段——和完整 Update 不同,批量排序是纯展示调整,不应触发祖先链重算。
func (r *DeptRepo) UpdateSort(ctx context.Context, id uint64, sort int) error {
	return r.db.WithContext(ctx).Model(&entity.SysDept{}).Where("id = ?", id).
		Update("sort", sort).Error
}

func (r *DeptRepo) HasChildren(ctx context.Context, id uint64) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&entity.SysDept{}).Where("parent_id = ?", id).Count(&count).Error
	return count > 0, err
}

func (r *DeptRepo) HasUsers(ctx context.Context, id uint64) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&entity.SysUser{}).Where("dept_id = ?", id).Count(&count).Error
	return count > 0, err
}

func (r *DeptRepo) UpdateAncestorsTx(ctx context.Context, tx *gorm.DB, id uint64, ancestors string) error {
	return tx.WithContext(ctx).Model(&entity.SysDept{}).Where("id = ?", id).
		Update("ancestors", ancestors).Error
}

func (r *DeptRepo) updateDescendantAncestorsTx(ctx context.Context, tx *gorm.DB, parentID uint64, parentAncestors string) error {
	// Read children through tx so the recursion sees in-transaction state.
	var children []entity.SysDept
	if err := tx.WithContext(ctx).Where("parent_id = ?", parentID).Find(&children).Error; err != nil {
		return err
	}
	for _, child := range children {
		newAncestors := parentAncestors + "," + strconv.FormatUint(child.ID, 10)
		if err := r.UpdateAncestorsTx(ctx, tx, child.ID, newAncestors); err != nil {
			return err
		}
		if err := r.updateDescendantAncestorsTx(ctx, tx, child.ID, newAncestors); err != nil {
			return err
		}
	}
	return nil
}

func (r *DeptRepo) FindChildDeptIDs(ctx context.Context, deptID uint64) ([]uint64, error) {
	var ids []uint64
	deptIDStr := strconv.FormatUint(deptID, 10)
	pattern := "%," + deptIDStr + ",%"
	err := r.db.WithContext(ctx).Model(&entity.SysDept{}).
		Where("(',' || ancestors || ',') LIKE ? AND id != ?", pattern, deptID).
		Pluck("id", &ids).Error
	return ids, err
}
