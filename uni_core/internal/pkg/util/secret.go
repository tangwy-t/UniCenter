package util

import "regexp"

// MaskedValue 是 API 对外返回敏感配置值时使用的占位符。
const MaskedValue = "******"

// sensitiveConfigKeyRe 匹配值不得通过 API 暴露的配置键名
// （签名密钥、密码、私钥、凭据等）。
var sensitiveConfigKeyRe = regexp.MustCompile(`(?i)(secret|password|passwd|private[_-]?key|credential)`)

// IsSensitiveConfigKey 判断给定配置键的值是否必须在 API 响应中掩码。
//
// 迁移自 service 包：敏感键判定是纯字符串正则匹配，收敛到 util 后
// 供 config 域（列表/详情/按键查询掩码）与 cache 域（缓存页 hash 字段
// 掩码）共用，避免两处各自维护正则可产生漂移。
func IsSensitiveConfigKey(key string) bool {
	return sensitiveConfigKeyRe.MatchString(key)
}

// MaskIfSensitive 对敏感配置键的值做掩码：敏感键返回 MaskedValue，
// 否则原样返回 val。
func MaskIfSensitive(key, val string) string {
	if IsSensitiveConfigKey(key) {
		return MaskedValue
	}
	return val
}
