package tasks

import (
	"context"
	"encoding/json"

	"github.com/tangwy-t/webmanager-server/internal/service"
)

// DictSyncTask 字典缓存同步定时任务。
// 从数据库加载所有启用的字典类型及其数据，全量同步到 Redis Hash。
type DictSyncTask struct {
	typeRepo DictTypeInterface
	dataRepo DictDataRepoInterface
	store    HashStoreInterface
}

// NewDictSyncTask 创建字典缓存同步任务实例。
func NewDictSyncTask(typeRepo DictTypeInterface, dataRepo DictDataRepoInterface, store HashStoreInterface) *DictSyncTask {
	return &DictSyncTask{typeRepo: typeRepo, dataRepo: dataRepo, store: store}
}

func (t *DictSyncTask) Name() string        { return "dict-cache-sync" }
func (t *DictSyncTask) DisplayName() string { return "字典缓存同步" }

// 序列化与 hash key 契约由 service 包统一提供(MarshalDictItems/
// DictHashKey):此处另持一份实现会与 service 静默漂移,缓存读不出。

func (t *DictSyncTask) Execute(ctx context.Context, _ json.RawMessage) error {
	types, err := t.typeRepo.FindAllEnabled(ctx)
	if err != nil {
		return err
	}
	for _, dt := range types {
		data, err := t.dataRepo.FindEnabledByTypeID(ctx, dt.ID)
		if err != nil {
			continue
		}
		val, err := service.MarshalDictItems(data)
		if err != nil {
			continue
		}
		if err := t.store.HSet(ctx, service.DictHashKey, dt.Code, val); err != nil {
			continue
		}
	}
	return nil
}
