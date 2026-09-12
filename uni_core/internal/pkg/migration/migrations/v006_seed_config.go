package migrations

import (
	"gorm.io/gorm"

	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/migration"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

func init() {
	migration.Register(migration.Migration{
		Version:     6,
		Description: "初始化系统配置项",
		Up:          seedConfig,
	})
}

// configDef 描述一个系统配置项。Type 为 N(数值)/S(字符串)/B(布尔)。
type configDef struct {
	Key     string
	Value   string
	Type    string
	Name    string
	Remark  string
	Enabled bool
}

// 上传文件白名单：与文件管理 7 分类扩展名清单对齐（历史 v020 终态）。
// 安全修复：移除可在同源内联预览时执行脚本的「活动内容」扩展名
// （.html/.htm/.svg/.js/.mjs/.jsx/.tsx/.vue）。这些后缀此前会让攻击者
// 上传同源 HTML/SVG/JS 并经 Preview 内联渲染执行脚本（存储型 XSS）。
// 代码类文件仍可上传，但 Preview 对它们强制 Content-Disposition: attachment
// 下载，不再内联渲染（见 handler/file.go Preview 的 nosniff + attachment 逻辑）。
const fileAllowedExtsFull = ".jpg,.jpeg,.png,.gif,.webp,.bmp,.ico,.avif,.mp4,.avi,.mov,.mkv,.webm,.flv,.wmv,.m4v,.rmvb,.mp3,.wav,.flac,.aac,.ogg,.wma,.m4a,.amr,.pdf,.doc,.docx,.xls,.xlsx,.ppt,.pptx,.txt,.md,.csv,.rtf,.odt,.zip,.rar,.7z,.tar,.gz,.bz2,.xz,.ts,.py,.go,.java,.c,.cpp,.h,.css,.scss,.json,.xml,.yml,.yaml,.sql,.sh,.toml"

// configDefinitions 是全部系统配置项的唯一来源，Name 已内联（历史 v018 回填终态）。
var configDefinitions = []configDef{
	{Key: "sys.jwt.secret", Value: "default-jwt-secret-key-at-least-32-bytes", Type: "S", Name: "JWT签名密钥", Remark: "JWT签名密钥", Enabled: true},
	{Key: "sys.jwt.accessExpire", Value: "7200", Type: "N", Name: "访问令牌过期时间", Remark: "访问令牌过期时间(秒)", Enabled: true},
	{Key: "sys.jwt.refreshExpire", Value: "604800", Type: "N", Name: "刷新令牌过期时间", Remark: "刷新令牌过期时间(秒)", Enabled: true},
	{Key: "sys.jwt.issuer", Value: "server", Type: "S", Name: "JWT签发者", Remark: "JWT签发者", Enabled: true},
	{Key: "sys.file.upload.maxSize", Value: "10485760", Type: "N", Name: "上传文件最大大小", Remark: "上传文件最大大小(字节)", Enabled: true},
	{Key: "sys.file.upload.path", Value: "./uploads", Type: "S", Name: "上传文件存储路径", Remark: "上传文件存储路径", Enabled: true},
	{Key: "sys.file.upload.allowedExts", Value: fileAllowedExtsFull, Type: "S", Name: "允许上传文件扩展名", Remark: "允许上传的文件扩展名", Enabled: true},
	{Key: "sys.scheduler.enabled", Value: "true", Type: "B", Name: "调度器开关", Remark: "调度器是否启用", Enabled: true},
	{Key: "sys.scheduler.stopTimeout", Value: "30", Type: "N", Name: "调度器停止超时", Remark: "调度器停止超时(秒)", Enabled: true},
	{Key: "sys.scheduler.lockTTL", Value: "60", Type: "N", Name: "任务锁TTL", Remark: "任务锁TTL(秒)", Enabled: true},
	{Key: "sys.scheduler.maxExecutionTime", Value: "300", Type: "N", Name: "任务最大执行时间", Remark: "任务最大执行时间(秒)", Enabled: true},
	{Key: "sys.scheduler.maxRetryCount", Value: "3", Type: "N", Name: "任务最大重试次数", Remark: "任务最大重试次数", Enabled: true},
	{Key: "sys.scheduler.resyncInterval", Value: "60", Type: "N", Name: "调度器同步间隔", Remark: "调度器同步间隔(秒)", Enabled: true},
	{Key: "sys.auth.asyncLogTimeout", Value: "5", Type: "N", Name: "异步日志超时", Remark: "异步日志超时(秒)", Enabled: true},
	{Key: "sys.auth.bcryptCost", Value: "10", Type: "N", Name: "密码哈希成本", Remark: "密码哈希成本", Enabled: true},
	{Key: "sys.log.retentionDays", Value: "90", Type: "N", Name: "日志保留天数", Remark: "日志保留天数", Enabled: true},
	{Key: "sys.config.resyncInterval", Value: "86400", Type: "N", Name: "配置同步间隔", Remark: "配置参数同步间隔(秒)", Enabled: true},
	{Key: "sys.auth.captchaFailThreshold", Value: "5", Type: "N", Name: "验证码触发阈值", Remark: "触发验证码的登录失败次数阈值", Enabled: true},
	{Key: "sys.auth.captchaExpireSeconds", Value: "300", Type: "N", Name: "验证码有效期", Remark: "验证码有效期(秒)", Enabled: true},
	{Key: "sys.auth.captchaFailWindowSeconds", Value: "900", Type: "N", Name: "失败计数窗口", Remark: "已废弃: 被 sys.auth.lockWindow 替代 (单位已改为分钟)", Enabled: true},
	{Key: "sys.auth.captchaRateLimitPerMinute", Value: "10", Type: "N", Name: "验证码获取限流", Remark: "同IP每分钟最大验证码获取次数", Enabled: true},
	{Key: "sys.auth.password.minLength", Value: "8", Type: "N", Name: "密码最小长度", Remark: "用户密码最小长度", Enabled: true},
	{Key: "sys.auth.password.minCategories", Value: "3", Type: "N", Name: "密码最少组合", Remark: "用户密码种类最少组合", Enabled: true},
	{Key: "sys.auth.password.forbidContainingUsername", Value: "true", Type: "B", Name: "密码是否禁止包含用户名", Remark: "用户密码是否禁止包含用户名", Enabled: true},

	// 账号锁定（历史 v009）
	{Key: "sys.auth.lockThreshold", Value: "10", Type: "N", Name: "锁定失败次数阈值", Remark: "触发账号锁定的连续登录失败次数", Enabled: true},
	{Key: "sys.auth.lockDuration", Value: "30", Type: "N", Name: "锁定持续时间", Remark: "账号锁定持续时间(分钟)", Enabled: true},
	{Key: "sys.auth.lockWindow", Value: "15", Type: "N", Name: "失败计数窗口", Remark: "登录失败计数窗口(分钟)，替代 captchaFailWindowSeconds", Enabled: true},

	// API 限流（历史 v010）
	{Key: "sys.rateLimit.global.enabled", Value: "true", Type: "B", Name: "全局限流开关", Remark: "全局 API 限流开关", Enabled: true},
	{Key: "sys.rateLimit.global.limit", Value: "100", Type: "N", Name: "全局每窗口最大请求数", Remark: "全局每窗口最大请求数", Enabled: true},
	{Key: "sys.rateLimit.global.windowSecs", Value: "60", Type: "N", Name: "全局限流窗口大小", Remark: "全局限流窗口大小(秒)", Enabled: true},
	{Key: "sys.rateLimit.login.enabled", Value: "true", Type: "B", Name: "登录限流开关", Remark: "登录接口限流开关", Enabled: true},
	{Key: "sys.rateLimit.login.limit", Value: "10", Type: "N", Name: "登录每窗口最大请求数", Remark: "登录接口每窗口最大请求数", Enabled: true},
	{Key: "sys.rateLimit.login.windowSecs", Value: "60", Type: "N", Name: "登录限流窗口大小", Remark: "登录接口限流窗口大小(秒)", Enabled: true},

	// 观测性（历史 v014）
	{Key: "sys.pprof.enabled", Value: "false", Type: "B", Name: "pprof开关", Remark: "pprof 是否启用（运行时动态切换）", Enabled: true},
	{Key: "sys.pprof.autoOffSeconds", Value: "300", Type: "N", Name: "pprof自动关闭超时", Remark: "pprof 自动关闭超时（秒），0 表示不自动关闭", Enabled: true},
}

// seedConfig 批量写入全部系统配置项。
func seedConfig(tx *gorm.DB) error {
	configs := make([]entity.SysConfig, 0, len(configDefinitions))
	for _, c := range configDefinitions {
		configs = append(configs, entity.SysConfig{
			Name:        c.Name,
			ConfigKey:   c.Key,
			ConfigValue: c.Value,
			ConfigType:  c.Type,
			Remark:      util.Ptr(c.Remark),
			Status:      util.Ptr[int8](boolToInt8(c.Enabled)),
		})
	}
	return tx.CreateInBatches(configs, 100).Error
}
