package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// DictTypeRepositoryInterface 由 service/interface.go 迁移至此:接口定义在消费方,
// 不再维护包级中央接口文件。
// DictTypeRepositoryInterface defines the data-access contract for dictionary type operations.
type DictTypeRepositoryInterface interface {
	// FindPage returns a paginated list of dictionary types matching the given query, along with the total count.
	FindPage(ctx context.Context, query *request.DictTypeQuery) ([]entity.SysDictType, int64, error)
	// FindByID looks up a dictionary type by its primary key.
	FindByID(ctx context.Context, id uint64) (*entity.SysDictType, error)
	// FindByCode looks up a dictionary type by its unique code.
	FindByCode(ctx context.Context, code string) (*entity.SysDictType, error)
	// Create inserts a new dictionary type record.
	Create(ctx context.Context, dt *entity.SysDictType) error
	// Update persists changes to an existing dictionary type (only non-zero fields).
	Update(ctx context.Context, dt *entity.SysDictType) error
	// Delete soft-deletes a dictionary type by its primary key.
	Delete(ctx context.Context, id uint64) error
	DeleteTypeTx(ctx context.Context, typeID uint64) error
	// FindAllEnabled returns all dict types with status = enabled.
	FindAllEnabled(ctx context.Context) ([]entity.SysDictType, error)
}

// DictDataRepositoryInterface 由 service/interface.go 迁移至此:接口定义在消费方,
// 不再维护包级中央接口文件。
// DictDataRepositoryInterface defines the data-access contract for dictionary data operations.
type DictDataRepositoryInterface interface {
	// FindByTypeID returns all dictionary data entries for a given type, ordered by sort.
	FindByTypeID(ctx context.Context, typeID uint64) ([]entity.SysDictData, error)
	// FindByID looks up a dictionary data entry by its primary key.
	FindByID(ctx context.Context, id uint64) (*entity.SysDictData, error)
	// Create inserts a new dictionary data record.
	Create(ctx context.Context, dd *entity.SysDictData) error
	// Update persists changes to an existing dictionary data entry (only non-zero fields).
	Update(ctx context.Context, dd *entity.SysDictData) error
	// Delete soft-deletes a dictionary data entry by its primary key.
	Delete(ctx context.Context, id uint64) error
	// DeleteByTypeID soft-deletes all dictionary data entries for the given type.
	DeleteByTypeID(ctx context.Context, typeID uint64) error
	// ClearDefault sets is_default to 0 for all data entries of the given type.
	ClearDefault(ctx context.Context, typeID uint64) error
	CreateWithDefaultTx(ctx context.Context, dd *entity.SysDictData) error
	UpdateWithDefaultTx(ctx context.Context, dd *entity.SysDictData) error
	// FindEnabledByTypeID returns all enabled dictionary data entries for a given type, ordered by sort.
	FindEnabledByTypeID(ctx context.Context, typeID uint64) ([]entity.SysDictData, error)
}

// DictHashKey 是字典缓存的 Redis Hash key。导出供 task/dict_sync 复用:
// 此前 task 与 service 各自维护同值常量,改一处即静默漂移。
const DictHashKey = "dict:values"

// MarshalDictItems 将字典数据实体序列化为缓存 JSON 文本。导出供
// task/dict_sync 复用:序列化格式与 hash key 同为缓存契约的一部分,
// 双源实现会在任一侧变更时静默漂移导致缓存读不出。
// toDictItem 将一个 SysDictData 映射为响应 DTO(Label/Value/IsDefault/Sort)。
// 该映射曾在 MarshalDictItems 与 FindDataByCode 内重复;两者唯一区别是
// 状态过滤(MarshalDictItems 由 repo 预筛 enabled,FindDataByCode 内联过滤),
// 故把公共字段映射抽到此,状态判断留给调用方。
func toDictItem(d entity.SysDictData) response.DictItem {
	item := response.DictItem{
		Label: d.Label,
		Value: d.Value,
	}
	if d.IsDefault != nil && *d.IsDefault == entity.DictDataDefaultYes {
		item.IsDefault = true
	}
	if d.Sort != nil {
		item.Sort = *d.Sort
	}
	if d.ListClass != nil {
		item.ListClass = *d.ListClass
	}
	return item
}

func MarshalDictItems(data []entity.SysDictData) (string, error) {
	items := make([]response.DictItem, 0, len(data))
	for _, d := range data {
		items = append(items, toDictItem(d))
	}
	jsonBytes, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	return string(jsonBytes), nil
}

type DictService struct {
	typeRepo DictTypeRepositoryInterface
	dataRepo DictDataRepositoryInterface
	store    HashStoreInterface // 与 ConfigService 共用同一 Redis Hash 存储契约(定义在 config.go)
	logger   logger.LoggerInterface
}

// NewDictService constructs a DictService with the given dependencies.
func NewDictService(
	typeRepo DictTypeRepositoryInterface,
	dataRepo DictDataRepositoryInterface,
	store HashStoreInterface,
	logger logger.LoggerInterface,
) *DictService {
	return &DictService{
		typeRepo: typeRepo,
		dataRepo: dataRepo,
		store:    store,
		logger:   logger,
	}
}

// ─── private cache helpers ────────────────────────────────────────────

// dictGet retrieves cached dictionary items for the given code from Redis Hash.
// Returns (nil, false, nil) when store is nil or the field does not exist.
func (s *DictService) dictGet(ctx context.Context, code string) ([]response.DictItem, bool, error) {
	if s.store == nil {
		return nil, false, nil
	}
	val, err := s.store.HGet(ctx, DictHashKey, code)
	if err != nil {
		return nil, false, err
	}
	if val == "" {
		return nil, false, nil
	}

	var items []response.DictItem
	if err := json.Unmarshal([]byte(val), &items); err != nil {
		s.logger.Warn("dict: failed to unmarshal cached items",
			zap.String("code", code), zap.Error(err))
		return nil, false, nil
	}
	return items, true, nil
}

// dictGetLabel retrieves the label for a given dict type code and value from the cache.
func (s *DictService) dictGetLabel(ctx context.Context, code, value string) (string, bool, error) {
	items, ok, err := s.dictGet(ctx, code)
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
	}
	for _, item := range items {
		if item.Value == value {
			return item.Label, true, nil
		}
	}
	return "", false, nil
}

// dictRefresh reloads a single dict type from DB into Redis Hash.
// Skips when store is nil.
func (s *DictService) dictRefresh(ctx context.Context, code string) {
	if s.store == nil {
		return
	}

	dt, err := s.typeRepo.FindByCode(ctx, code)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.Warn("dict: dictRefresh FindByCode failed",
				zap.String("code", code), zap.Error(err))
			return
		}
		dt = nil
	}
	if dt == nil {
		// Type not found — remove from cache.
		if delErr := s.store.HDel(ctx, DictHashKey, code); delErr != nil {
			s.logger.Warn("dict: dictRefresh HDel failed",
				zap.String("code", code), zap.Error(delErr))
		}
		return
	}

	// Check if the type is disabled.
	if dt.Status == nil || *dt.Status != entity.DictTypeStatusEnabled {
		if delErr := s.store.HDel(ctx, DictHashKey, code); delErr != nil {
			s.logger.Warn("dict: dictRefresh HDel for disabled type failed",
				zap.String("code", code), zap.Error(delErr))
		}
		return
	}

	// Load enabled data for this type, ordered by sort.
	data, err := s.dataRepo.FindEnabledByTypeID(ctx, dt.ID)
	if err != nil {
		s.logger.Warn("dict: dictRefresh FindEnabledByTypeID failed",
			zap.String("code", code), zap.Error(err))
		return
	}

	val, err := MarshalDictItems(data)
	if err != nil {
		s.logger.Warn("dict: dictRefresh json marshal failed",
			zap.String("code", code), zap.Error(err))
		return
	}

	if err := s.store.HSet(ctx, DictHashKey, code, val); err != nil {
		s.logger.Warn("dict: dictRefresh HSet failed",
			zap.String("code", code), zap.Error(err))
	}
}

// dictInvalidate removes a dict type entry from Redis Hash.
// Skips when store is nil.
func (s *DictService) dictInvalidate(ctx context.Context, code string) {
	if s.store == nil {
		return
	}
	if err := s.store.HDel(ctx, DictHashKey, code); err != nil {
		s.logger.Warn("dict: dictInvalidate HDel failed",
			zap.String("code", code), zap.Error(err))
	}
}

// ─── DictType methods ───────────────────────────────────────────────

func (s *DictService) FindPage(ctx context.Context, query *request.DictTypeQuery) (*app.PageResponse, error) {
	types, total, err := s.typeRepo.FindPage(ctx, query)
	if err != nil {
		return nil, err
	}
	list := make([]response.DictTypeResp, 0, len(types))
	for i := range types {
		list = append(list, util.MapEntity[response.DictTypeResp](&types[i], s.logger))
	}
	return app.NewPageResponse(list, total, query.GetPage(), query.GetPageSize()), nil
}

func (s *DictService) FindByID(ctx context.Context, id uint64) (*response.DictTypeResp, error) {
	dt, err := s.typeRepo.FindByID(ctx, id)
	if err != nil {
		return nil, translateNotFound(err, "字典类型不存在")
	}
	resp := util.MapEntity[response.DictTypeResp](dt, s.logger)
	return &resp, nil
}

func (s *DictService) CreateType(ctx context.Context, req *request.CreateDictTypeReq) (uint64, error) {
	existing, err := s.typeRepo.FindByCode(ctx, req.Code)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, err
		}
		existing = nil
	}
	if existing != nil {
		return 0, apperror.Conflict(fmt.Sprintf("字典类型编码 %s 已存在", req.Code))
	}

	// CreateDictTypeReq 与 SysDictType 字段同名同构:一次 CopyEntity 拷贝
	// (Code/Name 必填,Status/Remark 指针 nil 即不设置),与 UpdateType 对齐。
	dt := &entity.SysDictType{}
	util.CopyEntity(dt, req, s.logger)
	if err := s.typeRepo.Create(ctx, dt); err != nil {
		return 0, err
	}
	return dt.ID, nil
}

func (s *DictService) UpdateType(ctx context.Context, id uint64, req *request.UpdateDictTypeReq) error {
	dt, err := s.typeRepo.FindByID(ctx, id)
	if err != nil {
		return translateNotFound(err, "字典类型不存在")
	}

	if dt.Code != req.Code {
		existing, err := s.typeRepo.FindByCode(ctx, req.Code)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return apperror.Internal("校验字典编码失败")
		}
		if existing != nil && existing.ID != id {
			return apperror.Conflict("字典编码已存在")
		}
	}

	oldCode := dt.Code
	util.CopyEntity(dt, req, s.logger)

	if err := s.typeRepo.Update(ctx, dt); err != nil {
		return err
	}

	s.dictRefresh(ctx, oldCode)
	if oldCode != req.Code {
		s.dictRefresh(ctx, req.Code)
	}
	return nil
}

func (s *DictService) DeleteType(ctx context.Context, id uint64) error {
	dt, err := s.typeRepo.FindByID(ctx, id)
	if err != nil {
		return translateNotFound(err, "字典类型不存在")
	}

	// DeleteTypeTx atomizes data+type deletes: two writes without
	// transaction leave empty type when data is already gone + skip
	// cache invalidation.
	if err := s.typeRepo.DeleteTypeTx(ctx, id); err != nil {
		return err
	}

	s.dictInvalidate(ctx, dt.Code)
	return nil
}

// ─── DictData methods ───────────────────────────────────────────────

func (s *DictService) FindDataByType(ctx context.Context, typeID uint64) ([]response.DictDataResp, error) {
	// 校验字典类型存在（不存在返回 404）。
	if _, err := s.typeRepo.FindByID(ctx, typeID); err != nil {
		return nil, translateNotFound(err, "字典类型不存在")
	}

	// 管理端列表需要完整字段（id/typeId/status/remark 等），直接查询 DB。
	// 缓存（DictItem）仅服务于消费端 /dict/codes/:code，不含主键与状态字段，
	// 若从缓存构造会丢失 ID/TypeID 导致编辑/删除失效。
	data, err := s.dataRepo.FindByTypeID(ctx, typeID)
	if err != nil {
		return nil, err
	}

	list := make([]response.DictDataResp, 0, len(data))
	for i := range data {
		list = append(list, util.MapEntity[response.DictDataResp](&data[i], s.logger))
	}

	return list, nil
}

func (s *DictService) FindDataByID(ctx context.Context, id uint64) (*response.DictDataResp, error) {
	dd, err := s.dataRepo.FindByID(ctx, id)
	if err != nil {
		return nil, translateNotFound(err, "字典数据不存在")
	}
	resp := util.MapEntity[response.DictDataResp](dd, s.logger)
	return &resp, nil
}

func (s *DictService) CreateData(ctx context.Context, typeID uint64, req *request.CreateDictDataReq) (uint64, error) {
	dt, err := s.typeRepo.FindByID(ctx, typeID)
	if err != nil {
		return 0, translateNotFound(err, "字典类型不存在")
	}

	// CreateDictDataReq 与 SysDictData 字段同名同构:一次 CopyEntity 拷贝,
	// TypeID 不在请求内单独补上(与 UpdateData 的合并语义对齐)。
	dd := &entity.SysDictData{}
	util.CopyEntity(dd, req, s.logger)
	dd.TypeID = typeID

	if dd.IsDefault != nil && *dd.IsDefault == entity.DictDataDefaultYes {
		// 事务保证 Clear+Create 原子:Clear 成功而 Create 失败会
		// 清光该类型所有默认(真实业务数据损坏)。
		if err := s.dataRepo.CreateWithDefaultTx(ctx, dd); err != nil {
			return 0, err
		}
	} else {
		if err := s.dataRepo.Create(ctx, dd); err != nil {
			return 0, err
		}
	}

	s.dictRefresh(ctx, dt.Code)
	return dd.ID, nil
}

func (s *DictService) UpdateData(ctx context.Context, id uint64, req *request.UpdateDictDataReq) error {
	dd, err := s.dataRepo.FindByID(ctx, id)
	if err != nil {
		return translateNotFound(err, "字典数据不存在")
	}

	util.CopyEntity(dd, req, s.logger)

	// ClearDefault 必须在 CopyEntity 合并请求后判断新值:判断放在合并前
	// 用的是旧值——"非默认→默认"不清其它默认(产生双默认),"默认→非默认"
	// 反而清光全部默认。CreateData 一直是合并后判断,此处对齐。
	if dd.IsDefault != nil && *dd.IsDefault == entity.DictDataDefaultYes {
		if err := s.dataRepo.UpdateWithDefaultTx(ctx, dd); err != nil {
			return err
		}
	} else {
		if err := s.dataRepo.Update(ctx, dd); err != nil {
			return err
		}
	}

	dt, findErr := s.typeRepo.FindByID(ctx, dd.TypeID)
	if findErr != nil {
		s.logger.Warn("dict cache warm failed: cannot find type",
			zap.Uint64("typeID", dd.TypeID), zap.Error(findErr))
	} else if dt != nil {
		s.dictRefresh(ctx, dt.Code)
	}
	return nil
}

func (s *DictService) DeleteData(ctx context.Context, id uint64) error {
	dd, err := s.dataRepo.FindByID(ctx, id)
	if err != nil {
		return translateNotFound(err, "字典数据不存在")
	}

	if err := s.dataRepo.Delete(ctx, id); err != nil {
		return err
	}

	dt, findErr := s.typeRepo.FindByID(ctx, dd.TypeID)
	if findErr != nil {
		s.logger.Warn("dict cache warm failed: cannot find type",
			zap.Uint64("typeID", dd.TypeID), zap.Error(findErr))
	} else if dt != nil {
		s.dictRefresh(ctx, dt.Code)
	}
	return nil
}

// ─── DictConsumer methods ────────────────────────────────────────────

// FindDataByCode returns all enabled dictionary data items for the given type code.
// It uses Redis Hash first, then falls back to DB.
func (s *DictService) FindDataByCode(ctx context.Context, code string) ([]response.DictItem, error) {
	// Tier 1: Redis Hash.
	if s.store != nil {
		items, ok, err := s.dictGet(ctx, code)
		if err != nil {
			s.logger.Warn("dict cache Get failed for FindDataByCode, falling back to DB",
				zap.String("code", code), zap.Error(err))
		} else if ok {
			return items, nil
		}
	}

	// Tier 2: DB fallback.
	dt, err := s.typeRepo.FindByCode(ctx, code)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return []response.DictItem{}, nil
		}
		return nil, err
	}

	data, err := s.dataRepo.FindByTypeID(ctx, dt.ID)
	if err != nil {
		return nil, err
	}

	items := make([]response.DictItem, 0, len(data))
	for _, d := range data {
		if d.Status != nil && *d.Status == entity.DictDataStatusEnabled {
			items = append(items, toDictItem(d))
		}
	}

	// Populate cache for next request.
	if s.store != nil {
		s.dictRefresh(ctx, code)
	}

	return items, nil
}

// FindDataByCodes returns a map of code -> items by calling FindDataByCode for each code.
func (s *DictService) FindDataByCodes(ctx context.Context, codes []string) (map[string][]response.DictItem, error) {
	result := make(map[string][]response.DictItem, len(codes))
	for _, code := range codes {
		items, err := s.FindDataByCode(ctx, code)
		if err != nil {
			return nil, err
		}
		result[code] = items
	}
	return result, nil
}

// GetLabel returns the label for a given dict type code and value.
func (s *DictService) GetLabel(ctx context.Context, code, value string) (string, error) {
	if s.store != nil {
		label, ok, err := s.dictGetLabel(ctx, code, value)
		if err != nil {
			s.logger.Warn("dict cache GetLabel failed",
				zap.String("code", code), zap.String("value", value), zap.Error(err))
		} else if ok {
			return label, nil
		}
	}

	// Fallback to FindDataByCode + linear search.
	items, err := s.FindDataByCode(ctx, code)
	if err != nil {
		return "", err
	}
	for _, item := range items {
		if item.Value == value {
			return item.Label, nil
		}
	}
	return "", nil
}
