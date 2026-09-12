package captcha

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Result carries the Captcha key and base64-encoded PNG image.
type Result struct {
	Key   string `json:"captchaKey"`
	Image string `json:"captchaImage"` // data:image/png;base64,...
}

// ConfigGetterInterface abstracts configuration retrieval for Captcha parameters.

type Captcha struct {
	cacheStore CacheStoreInterface
	cfgProv    ConfigGetterInterface
	logger     logger.LoggerInterface
}

// NewCaptcha creates Interface backed by Redis.
func NewCaptcha(rdb CacheStoreInterface, cfgProv ConfigGetterInterface, log logger.LoggerInterface) *Captcha {
	return &Captcha{cacheStore: rdb, cfgProv: cfgProv, logger: log}
}

func (s *Captcha) Generate(ctx context.Context, clientIP string) (*Result, error) {
	// Rate limiting: fixed window ("N per minute"). The Lua script does
	// INCR + EXPIRE atomically (no race where the key persists without TTL)
	// and only sets EXPIRE on the first increment — sliding-TTL semantics
	// here would turn "10 per minute" into "10 since the last request",
	// punishing steady low-rate users.
	rateKey := "captcha_rate:" + clientIP
	count, err := s.cacheStore.FixedWindowIncrWithTTL(ctx, rateKey, 60*time.Second)
	if err != nil {
		return nil, apperror.Internal("验证码服务异常", err)
	}
	rateLimit := s.cfgProv.GetInt(ctx, "sys.auth.captchaRateLimitPerMinute", 10)
	if count > int64(rateLimit) {
		return nil, apperror.RateLimited("操作过于频繁，请稍后再试")
	}

	question, answer := generateMathQuestion()
	imgData, err := renderImage(question)
	if err != nil {
		return nil, apperror.Internal("验证码服务异常", err)
	}

	key := uuid.New().String()
	redisKey := "Captcha:" + key
	expireSeconds := s.cfgProv.GetInt(ctx, "sys.auth.captchaExpireSeconds", 300)
	if err := s.cacheStore.Set(ctx, redisKey, strconv.Itoa(answer), time.Duration(expireSeconds)*time.Second); err != nil {
		return nil, apperror.Internal("验证码服务异常", err)
	}

	return &Result{
		Key:   key,
		Image: "data:image/png;base64," + base64.StdEncoding.EncodeToString(imgData),
	}, nil
}

func (s *Captcha) Verify(ctx context.Context, key, code string) error {
	redisKey := "Captcha:" + key
	stored, err := s.cacheStore.Get(ctx, redisKey)
	if err != nil {
		return apperror.Internal("验证码服务异常", err)
	}
	if stored == "" {
		return apperror.CaptchaExpired("验证码已过期，请重新获取")
	}
	if stored != code {
		// 失败同样立即失效该 key:算术验证码答案空间很小(0~39),
		// 保留 key 允许攻击者在同一 key 上反复枚举直到命中。
		if err := s.cacheStore.Del(ctx, redisKey); err != nil {
			s.logger.Warn("Captcha: failed to delete key after failed verification",
				zap.String("key", redisKey), zap.Error(err))
		}
		return apperror.CaptchaIncorrect("验证码错误")
	}
	// One-time use: delete after successful verification
	if err := s.cacheStore.Del(ctx, redisKey); err != nil {
		s.logger.Warn("Captcha: failed to delete Captcha key after verification",
			zap.String("key", redisKey), zap.Error(err))
	}
	return nil
}

func (s *Captcha) failKey(userID uint64) string {
	return fmt.Sprintf("login_fail:%d", userID)
}

func (s *Captcha) GetFailCount(ctx context.Context, userID uint64) (int, error) {
	val, err := s.cacheStore.Get(ctx, s.failKey(userID))
	if err != nil {
		return 0, apperror.Internal("验证码服务异常", err)
	}
	if val == "" {
		return 0, nil
	}
	count, err := strconv.Atoi(val)
	if err != nil {
		return 0, apperror.Internal("验证码服务异常", err)
	}
	return count, nil
}

func (s *Captcha) IncrementFailCount(ctx context.Context, userID uint64) (int, error) {
	key := s.failKey(userID)
	lockWindow := s.cfgProv.GetInt(ctx, "sys.auth.lockWindow", 15)
	count, err := s.cacheStore.IncrWithTTL(ctx, key, time.Duration(lockWindow)*time.Minute)
	if err != nil {
		return 0, apperror.Internal("验证码服务异常", err)
	}
	return int(count), nil
}

func (s *Captcha) ResetFailCount(ctx context.Context, userID uint64) error {
	if err := s.cacheStore.Del(ctx, s.failKey(userID)); err != nil {
		return apperror.Internal("验证码服务异常", err)
	}
	return nil
}

func (s *Captcha) IsCaptchaRequired(ctx context.Context, userID uint64) (bool, error) {
	threshold := s.cfgProv.GetInt(ctx, "sys.auth.captchaFailThreshold", 5)
	count, err := s.GetFailCount(ctx, userID)
	if err != nil {
		return false, err
	}
	return count >= threshold, nil
}

func (s *Captcha) lockKey(userID uint64) string {
	return fmt.Sprintf("login_lock:%d", userID)
}

func (s *Captcha) IsLocked(ctx context.Context, userID uint64) (bool, int, error) {
	dur, err := s.cacheStore.TTL(ctx, s.lockKey(userID))
	if err != nil {
		return false, 0, apperror.Internal("验证码服务异常", err)
	}
	// TTL returns -2 if key does not exist, -1 if key exists but has no expiration.
	// Both cases mean the user is not locked.
	if dur <= 0 {
		return false, 0, nil
	}
	return true, int(dur.Seconds()), nil
}

func (s *Captcha) SetLock(ctx context.Context, userID uint64) error {
	// Lock FIRST, then reset the fail counter. The previous order (reset
	// then lock) opened a fail-open window: if the reset succeeded but the
	// lock write failed, the attacker got a fresh fail quota without being
	// locked. Set is an idempotent overwrite, so locking first is safe.
	lockDuration := s.cfgProv.GetInt(ctx, "sys.auth.lockDuration", 30)
	if err := s.cacheStore.Set(ctx, s.lockKey(userID), "1", time.Duration(lockDuration)*time.Minute); err != nil {
		return apperror.Internal("验证码服务异常", err)
	}
	if err := s.ResetFailCount(ctx, userID); err != nil {
		return err
	}
	return nil
}

func (s *Captcha) Unlock(ctx context.Context, userID uint64) error {
	// Split into individual Del calls to avoid CROSSSLOT error in Redis cluster mode
	// (login_fail:<userID> and login_lock:<userID> hash to different slots).
	if err := s.cacheStore.Del(ctx, s.failKey(userID)); err != nil {
		return err
	}
	return s.cacheStore.Del(ctx, s.lockKey(userID))
}
