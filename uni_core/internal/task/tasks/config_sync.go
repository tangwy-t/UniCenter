package tasks

import (
	"context"
	"encoding/json"

	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/service"
)

// ConfigSyncTask 配置缓存同步定时任务。
// 从数据库加载所有启用的配置项，全量同步到 Redis Hash。
type ConfigSyncTask struct {
	configRepo ConfigRepoInterface
	store      HashStoreInterface
}

// NewConfigSyncTask 创建配置缓存同步任务实例。
func NewConfigSyncTask(configRepo ConfigRepoInterface, store HashStoreInterface) *ConfigSyncTask {
	return &ConfigSyncTask{configRepo: configRepo, store: store}
}

func (t *ConfigSyncTask) Name() string        { return "config-cache-sync" }
func (t *ConfigSyncTask) DisplayName() string { return "配置缓存同步" }

func (t *ConfigSyncTask) Execute(ctx context.Context, _ json.RawMessage) error {
	cfgs, err := t.configRepo.FindAllEnabled(ctx)
	if err != nil {
		return err
	}
	fields := make(map[string]string, len(cfgs))
	for _, c := range cfgs {
		fields[c.ConfigKey] = c.ConfigValue
	}
	// hash key 契约由 service.ConfigHashKey 统一提供,避免双源漂移。
	return t.store.HMSet(ctx, service.ConfigHashKey, fields)
}

// Ensure ConfigSyncTask implements Task.
var _ interface {
	Name() string
	DisplayName() string
	Execute(ctx context.Context, params json.RawMessage) error
} = (*ConfigSyncTask)(nil)

// Ensure entity import is used.
var _ = entity.SysConfig{}.TableName
