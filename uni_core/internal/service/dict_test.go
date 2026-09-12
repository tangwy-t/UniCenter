package service

import (
	"context"
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
)

// ── 测试替身 ────────────────────────────────────────────

// memHashStore 实现 HashStoreInterface，内存 map。
type memHashStore struct{ m map[string]map[string]string }

func newMemHashStore() *memHashStore { return &memHashStore{m: map[string]map[string]string{}} }

func (s *memHashStore) HSet(_ context.Context, key, field, value string) error {
	if s.m[key] == nil {
		s.m[key] = map[string]string{}
	}
	s.m[key][field] = value
	return nil
}
func (s *memHashStore) HGet(_ context.Context, key, field string) (string, error) {
	return s.m[key][field], nil
}
func (s *memHashStore) HDel(_ context.Context, key string, fields ...string) error {
	for _, f := range fields {
		delete(s.m[key], f)
	}
	return nil
}

// stubDictTypeRepo 实现 DictTypeRepositoryInterface 最小集。
type stubDictTypeRepo struct {
	findByID *entity.SysDictType
	err      error
}

func (m *stubDictTypeRepo) FindByID(_ context.Context, _ uint64) (*entity.SysDictType, error) {
	return m.findByID, m.err
}
func (m *stubDictTypeRepo) FindAllEnabled(_ context.Context) ([]entity.SysDictType, error) {
	return nil, nil
}
func (m *stubDictTypeRepo) FindPage(context.Context, *request.DictTypeQuery) ([]entity.SysDictType, int64, error) {
	return nil, 0, nil
}
func (m *stubDictTypeRepo) FindByCode(context.Context, string) (*entity.SysDictType, error) {
	return nil, nil
}
func (m *stubDictTypeRepo) Create(context.Context, *entity.SysDictType) error { return nil }
func (m *stubDictTypeRepo) Update(context.Context, *entity.SysDictType) error { return nil }
func (m *stubDictTypeRepo) Delete(context.Context, uint64) error              { return nil }
func (m *stubDictTypeRepo) DeleteTypeTx(context.Context, uint64) error        { return nil }

// stubDictDataRepo 实现 DictDataRepositoryInterface 最小集。
type stubDictDataRepo struct {
	createData   *entity.SysDictData
	findByID     *entity.SysDictData
	findByTypeID []entity.SysDictData
}

func (m *stubDictDataRepo) Create(_ context.Context, dd *entity.SysDictData) error {
	m.createData = dd
	return nil
}
func (m *stubDictDataRepo) Update(context.Context, *entity.SysDictData) error { return nil }
func (m *stubDictDataRepo) Delete(context.Context, uint64) error              { return nil }
func (m *stubDictDataRepo) FindByID(_ context.Context, _ uint64) (*entity.SysDictData, error) {
	return m.findByID, nil
}
func (m *stubDictDataRepo) FindByTypeID(context.Context, uint64) ([]entity.SysDictData, error) {
	return m.findByTypeID, nil
}
func (m *stubDictDataRepo) FindEnabledByTypeID(context.Context, uint64) ([]entity.SysDictData, error) {
	return nil, nil
}
func (m *stubDictDataRepo) CreateWithDefaultTx(_ context.Context, dd *entity.SysDictData) error {
	m.createData = dd
	return nil
}
func (m *stubDictDataRepo) UpdateWithDefaultTx(context.Context, *entity.SysDictData) error {
	return nil
}
func (m *stubDictDataRepo) DeleteByTypeID(context.Context, uint64) error { return nil }
func (m *stubDictDataRepo) ClearDefault(context.Context, uint64) error   { return nil }

// ── 用例 ────────────────────────────────────────────────

func TestToDictItemListClass(t *testing.T) {
	lc := "danger"
	d := entity.SysDictData{Label: "失败", Value: "2", ListClass: &lc}
	item := toDictItem(d)
	if item.ListClass != "danger" {
		t.Fatalf("ListClass = %q, want danger", item.ListClass)
	}

	// nil → 空串回落
	item = toDictItem(entity.SysDictData{Label: "x", Value: "1"})
	if item.ListClass != "" {
		t.Fatalf("nil ListClass = %q, want empty", item.ListClass)
	}
}

func TestFindDataByTypeReturnsFullRecords(t *testing.T) {
	lc := "danger"
	typeRepo := &stubDictTypeRepo{findByID: &entity.SysDictType{BaseEntity: entity.BaseEntity{ID: 214}, Code: "sys_dict_status"}}
	dataRepo := &stubDictDataRepo{
		findByTypeID: []entity.SysDictData{
			{
				BaseEntity: entity.BaseEntity{ID: 301},
				TypeID:     214,
				Label:      "失败",
				Value:      "2",
				ListClass:  &lc,
			},
		},
	}
	svc := NewDictService(typeRepo, dataRepo, newMemHashStore(), logger.NewNop())

	list, err := svc.FindDataByType(context.Background(), 214)
	if err != nil {
		t.Fatalf("FindDataByType error: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len = %d, want 1", len(list))
	}
	if list[0].ID != 301 || list[0].TypeID != 214 {
		t.Fatalf("ID/TypeID = %d/%d, want 301/214", list[0].ID, list[0].TypeID)
	}
	if list[0].ListClass != "danger" {
		t.Fatalf("ListClass = %q, want danger", list[0].ListClass)
	}
}

func TestCreateDataPersistsListClass(t *testing.T) {
	typeRepo := &stubDictTypeRepo{findByID: &entity.SysDictType{BaseEntity: entity.BaseEntity{ID: 214}, Code: "sys_dict_status"}}
	dataRepo := &stubDictDataRepo{}
	svc := NewDictService(typeRepo, dataRepo, newMemHashStore(), logger.NewNop())

	lc := "success"
	if _, err := svc.CreateData(context.Background(), 214, &request.CreateDictDataReq{
		Label: "启用", Value: "1", ListClass: &lc,
	}); err != nil {
		t.Fatalf("CreateData error: %v", err)
	}
	if dataRepo.createData == nil || dataRepo.createData.ListClass == nil || *dataRepo.createData.ListClass != "success" {
		t.Fatalf("persisted ListClass = %v, want *success", dataRepo.createData.ListClass)
	}

	// nil → 不设置（落库为 NULL）。
	if _, err := svc.CreateData(context.Background(), 214, &request.CreateDictDataReq{
		Label: "停用", Value: "0",
	}); err != nil {
		t.Fatalf("CreateData error: %v", err)
	}
	if dataRepo.createData.ListClass != nil {
		t.Fatalf("nil request ListClass persisted as %v, want nil", *dataRepo.createData.ListClass)
	}
}
