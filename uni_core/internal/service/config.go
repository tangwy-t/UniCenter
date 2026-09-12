package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/jwt"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// BrokerInterface 由 service/interface.go 迁移至此:接口定义在消费方,
// 不再维护包级中央接口文件。
type BrokerInterface interface {
	Publish(ctx context.Context, eventType string, payload any) error
}

// HashStoreInterface 由 service/interface.go 迁移至此:接口定义在消费方,
// 不再维护包级中央接口文件。
type HashStoreInterface interface {
	HSet(ctx context.Context, key, field, value string) error
	HGet(ctx context.Context, key, field string) (string, error)
	HDel(ctx context.Context, key string, fields ...string) error
}

// ConfigRepositoryInterface 由 service/interface.go 迁移至此:接口定义在消费方,
// 不再维护包级中央接口文件。
type ConfigRepositoryInterface interface {
	FindPage(ctx context.Context, query *request.ConfigQuery) ([]entity.SysConfig, int64, error)
	FindByID(ctx context.Context, id uint64) (*entity.SysConfig, error)
	FindByKey(ctx context.Context, key string) (*entity.SysConfig, error)
	CheckKeyExists(ctx context.Context, key string, excludeID uint64) (bool, error)
	Create(ctx context.Context, cfg *entity.SysConfig) error
	Update(ctx context.Context, cfg *entity.SysConfig) error
	Delete(ctx context.Context, id uint64) error
	FindAllEnabled(ctx context.Context) ([]entity.SysConfig, error)
}

const (
	// ConfigHashKey is the Redis Hash key for config values. Exported for
	// task/config_sync reuse: dual-source constants drift silently.
	ConfigHashKey = "config:values"
	// ConfigChangedChannel is the Redis PubSub channel for config changes.
	// Exported for scheduler/sync reuse: dual-source channel names drift silently.
	ConfigChangedChannel = "config.changed"
)

// ConfigChangedMsg 是 config.changed Pub/Sub 消息载荷。
// 发布侧(configWarm/configInvalidate)与订阅侧(scheduler 热更新)共享此类型,
// 字段名即协议:{"key":"sys.xxx"}。修改需同步订阅方解析。
type ConfigChangedMsg struct {
	Key string `json:"key"`
}

type ConfigService struct {
	repo      ConfigRepositoryInterface
	hashStore HashStoreInterface
	broker    BrokerInterface
	logger    logger.LoggerInterface
}

func NewConfigService(repo ConfigRepositoryInterface, hashStore HashStoreInterface, broker BrokerInterface, logger logger.LoggerInterface) *ConfigService {
	return &ConfigService{repo: repo, hashStore: hashStore, broker: broker, logger: logger}
}

// defaultJWTSecretMarkers lists known insecure fallback values for
// "sys.jwt.secret". Any of them committed in source (the v006 seed) lets
// anyone with repository access forge admin tokens.
var defaultJWTSecretMarkers = map[string]bool{
	"default-jwt-secret-key-at-least-32-bytes": true, // seeded by migration v006
	jwt.DefaultSecretFallback:                  true, // legacy GetString fallback
}

// RotateDefaultJWTSecret replaces a known default JWT secret with a random
// value at startup. It must run before anything reads or snapshots the secret
// (hub construction, first login). The rotation also warms the Redis hash and
// publishes a change notification so other instances drop the old value.
func (s *ConfigService) RotateDefaultJWTSecret(ctx context.Context) {
	val, err := s.getByKey(ctx, jwt.SecretConfigKey)
	if err != nil || !defaultJWTSecretMarkers[val] {
		return
	}
	secret, err := util.RandomHex(48)
	if err != nil {
		s.logger.Error("failed to generate random jwt secret, insecure default still active", zap.Error(err))
		return
	}
	cfg, err := s.repo.FindByKey(ctx, jwt.SecretConfigKey)
	if err != nil {
		s.logger.Error("failed to load jwt secret config for rotation", zap.Error(err))
		return
	}
	cfg.ConfigValue = secret
	if err := s.repo.Update(ctx, cfg); err != nil {
		s.logger.Error("failed to rotate default jwt secret, insecure default still active", zap.Error(err))
		return
	}
	s.configWarm(ctx, cfg.ConfigKey, cfg.ConfigValue)
	s.logger.Warn("default jwt secret detected and rotated to a random value; " +
		"existing tokens signed with the old secret are now invalid")
}

func (s *ConfigService) FindPage(ctx context.Context, query *request.ConfigQuery) (*app.PageResponse, error) {
	list, total, err := s.repo.FindPage(ctx, query)
	if err != nil {
		s.logger.Error("ConfigService.FindPage failed", zap.Error(err))
		return nil, apperror.Internal("查询参数配置失败")
	}
	var resp []response.ConfigResp
	for i := range list {
		resp = append(resp, util.MapEntity[response.ConfigResp](&list[i], s.logger))
	}
	// 敏感配置(如 sys.jwt.secret)的值不得通过列表/详情/按键查询泄漏:
	// 持较低权限的账号读到签名密钥即可伪造任意用户 token。
	// 掩码在组装 ConfigResp 时统一完成,任何新的读路径自动继承。
	for i := range resp {
		resp[i].ConfigValue = util.MaskIfSensitive(resp[i].ConfigKey, resp[i].ConfigValue)
	}
	return app.NewPageResponse(resp, total, query.GetPage(), query.GetPageSize()), nil
}

func (s *ConfigService) FindByID(ctx context.Context, id uint64) (*response.ConfigResp, error) {
	cfg, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, translateNotFound(err, "参数配置不存在")
	}
	resp := util.MapEntity[response.ConfigResp](cfg, s.logger)
	resp.ConfigValue = util.MaskIfSensitive(resp.ConfigKey, resp.ConfigValue)
	return &resp, nil
}

// ─── private cache helpers ────────────────────────────────────────────

// configWarm stores a config value in the Redis Hash and publishes a change notification.
// Skips HSet when hashStore is nil; skips Publish when broker is nil.
func (s *ConfigService) configWarm(ctx context.Context, key, value string) {
	if s.hashStore != nil {
		if err := s.hashStore.HSet(ctx, ConfigHashKey, key, value); err != nil {
			s.logger.Warn("ConfigService.configWarm HSet failed",
				zap.String("key", key), zap.Error(err))
		}
	}
	if s.broker != nil {
		msg := ConfigChangedMsg{Key: key}
		if err := s.broker.Publish(ctx, ConfigChangedChannel, msg); err != nil {
			s.logger.Warn("ConfigService.configWarm publish failed",
				zap.String("key", key), zap.Error(err))
		}
	}
}

// configInvalidate removes a config key from the Redis Hash and publishes a change notification.
// Skips HDel when hashStore is nil; skips Publish when broker is nil.
func (s *ConfigService) configInvalidate(ctx context.Context, key string) {
	if s.hashStore != nil {
		if err := s.hashStore.HDel(ctx, ConfigHashKey, key); err != nil {
			s.logger.Warn("ConfigService.configInvalidate HDel failed",
				zap.String("key", key), zap.Error(err))
		}
	}
	if s.broker != nil {
		msg := ConfigChangedMsg{Key: key}
		if err := s.broker.Publish(ctx, ConfigChangedChannel, msg); err != nil {
			s.logger.Warn("ConfigService.configInvalidate publish failed",
				zap.String("key", key), zap.Error(err))
		}
	}
}

// getByKey returns the RAW config value. Internal callers only: config
// getters (GetString/GetInt/...) and secret rotation must never see the
// masked placeholder. The exported GetByKey below is the API-facing variant.
func (s *ConfigService) getByKey(ctx context.Context, key string) (string, error) {
	if s.hashStore != nil {
		val, err := s.hashStore.HGet(ctx, ConfigHashKey, key)
		if err == nil && val != "" {
			return val, nil
		}
	}
	cfg, err := s.repo.FindByKey(ctx, key)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// HTTP 端点视角:key 不存在是 404;内部 getter(GetString 等)
			// 吞掉错误走默认值,两种消费方都得到正确语义。
			return "", apperror.NotFound("参数配置不存在")
		}
		return "", err
	}
	if s.hashStore != nil {
		if err := s.hashStore.HSet(ctx, ConfigHashKey, cfg.ConfigKey, cfg.ConfigValue); err != nil {
			s.logger.Warn("ConfigService.GetByKey cache backfill failed", zap.String("key", key), zap.Error(err))
		}
	}
	return cfg.ConfigValue, nil
}

// GetByKey is the API-facing variant: sensitive values are masked so the
// HTTP endpoint can return it verbatim. Internal getters must use getByKey.
func (s *ConfigService) GetByKey(ctx context.Context, key string) (string, error) {
	val, err := s.getByKey(ctx, key)
	if err != nil {
		return "", err
	}
	if util.IsSensitiveConfigKey(key) {
		return util.MaskedValue, nil
	}
	return val, nil
}

func (s *ConfigService) GetString(ctx context.Context, key, defaultVal string) string {
	val, err := s.getByKey(ctx, key)
	if err != nil {
		return defaultVal
	}
	return val
}

func (s *ConfigService) GetInt(ctx context.Context, key string, defaultVal int) int {
	val, err := s.getByKey(ctx, key)
	if err != nil {
		return defaultVal
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return defaultVal
	}
	return n
}

func (s *ConfigService) GetBool(ctx context.Context, key string, defaultVal bool) bool {
	val, err := s.getByKey(ctx, key)
	if err != nil {
		return defaultVal
	}
	lower := strings.ToLower(val)
	return lower == "true" || lower == "1"
}

// SetBool 写入 bool 类型配置值:先落 DB(真相源),再回写 Redis Hash
// 并发布变更通知。此前只写 Redis:config-cache-sync 任务
// (FindAllEnabled 全量回写 hash)会静默回滚该值,Redis 重启也会丢。
func (s *ConfigService) SetBool(ctx context.Context, key string, value bool) error {
	val := "false"
	if value {
		val = "true"
	}
	cfg, err := s.repo.FindByKey(ctx, key)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		// 键不存在则创建(启用状态,无备注)。
		status := int8(1)
		cfg = &entity.SysConfig{
			ConfigKey:   key,
			ConfigValue: val,
			ConfigType:  "S",
			Status:      &status,
		}
		if err := s.repo.Create(ctx, cfg); err != nil {
			s.logger.Error("ConfigService.SetBool create failed", zap.String("key", key), zap.Error(err))
			return apperror.Internal("写入配置失败")
		}
		s.configWarm(ctx, key, val)
		return nil
	}
	cfg.ConfigValue = val
	if err := s.repo.Update(ctx, cfg); err != nil {
		s.logger.Error("ConfigService.SetBool update failed", zap.String("key", key), zap.Error(err))
		return apperror.Internal("写入配置失败")
	}
	s.configWarm(ctx, key, val)
	return nil
}

func (s *ConfigService) Create(ctx context.Context, req *request.CreateConfigReq) (uint64, error) {
	if err := s.validateConfigValue(req.ConfigValue, req.ConfigType); err != nil {
		return 0, err
	}
	exists, err := s.repo.CheckKeyExists(ctx, req.ConfigKey, 0)
	if err != nil {
		return 0, err
	}
	if exists {
		return 0, apperror.Conflict("参数键名已存在")
	}

	cfg := &entity.SysConfig{}
	util.CopyEntity(cfg, req, s.logger)
	if err := s.repo.Create(ctx, cfg); err != nil {
		s.logger.Error("ConfigService.Create failed", zap.Error(err))
		return 0, apperror.Internal("创建参数配置失败")
	}
	s.configWarm(ctx, cfg.ConfigKey, cfg.ConfigValue)
	s.logger.Info("config created", zap.Uint64("configId", cfg.ID), zap.String("key", cfg.ConfigKey))
	return cfg.ID, nil
}

func (s *ConfigService) Update(ctx context.Context, id uint64, req *request.UpdateConfigReq) error {
	if err := s.validateConfigValue(req.ConfigValue, req.ConfigType); err != nil {
		return err
	}
	old, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return translateNotFound(err, "参数配置不存在")
	}

	// 敏感配置的值在 API 读取时被掩码；若编辑表单原样回传占位符，视为
	// "不修改该值"，避免把真实密钥覆盖成 "******"。
	if util.IsSensitiveConfigKey(old.ConfigKey) && req.ConfigValue == util.MaskedValue {
		req.ConfigValue = old.ConfigValue
	}

	util.CopyEntity(old, req, s.logger)
	if err := s.repo.Update(ctx, old); err != nil {
		s.logger.Error("ConfigService.Update failed", zap.Error(err))
		return apperror.Internal("更新参数配置失败")
	}
	s.configWarm(ctx, old.ConfigKey, old.ConfigValue)
	s.logger.Info("config updated", zap.Uint64("configId", id))
	return nil
}

func (s *ConfigService) Delete(ctx context.Context, id uint64) error {
	cfg, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return translateNotFound(err, "参数配置不存在")
	}
	if err := s.repo.Delete(ctx, id); err != nil {
		s.logger.Error("ConfigService.Delete failed", zap.Error(err))
		return apperror.Internal("删除参数配置失败")
	}
	s.configInvalidate(ctx, cfg.ConfigKey)
	s.logger.Info("config deleted", zap.Uint64("configId", id))
	return nil
}

func (s *ConfigService) validateConfigValue(value, configType string) error {
	switch configType {
	case "S":
		return nil
	case "N":
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return apperror.BadRequest(fmt.Sprintf("参数值不是有效的数字: %s", value))
		}
	case "B":
		lower := strings.ToLower(value)
		if lower != "true" && lower != "false" && lower != "1" && lower != "0" {
			return apperror.BadRequest(fmt.Sprintf("参数值不是有效的布尔值: %s", value))
		}
	case "J":
		if !json.Valid([]byte(value)) {
			return apperror.BadRequest(fmt.Sprintf("参数值不是有效的JSON: %s", value))
		}
	default:
		return apperror.BadRequest(fmt.Sprintf("不支持的参数类型: %s", configType))
	}
	return nil
}

func (s *ConfigService) LoadAllEnabled(ctx context.Context) map[string]string {
	items, err := s.repo.FindAllEnabled(ctx)
	if err != nil {
		s.logger.Warn("daily config sync failed", zap.Error(err))
		return make(map[string]string)
	}
	kv := make(map[string]string, len(items))
	for _, item := range items {
		kv[item.ConfigKey] = item.ConfigValue
	}
	return kv
}
