// Package agentproto 是 uni_agent 与 uni_core 之间消息的唯一契约来源。
//
// 本模块零第三方依赖，只使用标准库。
package agentproto

// MinVersion 与 CurrentVersion 定义信封版本 v 的受支持闭区间 [MinVersion, CurrentVersion]。
const (
	MinVersion     = 1
	CurrentVersion = 1
)

// IsSupported 报告信封版本 v 是否被本实现接受。
// 使用区间而非 == CurrentVersion，是为将来兼容旧版客户端留出空间。
func IsSupported(v int) bool {
	return v >= MinVersion && v <= CurrentVersion
}

// SupportedVersions 返回升序排列的全部受支持版本。
func SupportedVersions() []int {
	out := make([]int, 0, CurrentVersion-MinVersion+1)
	for v := MinVersion; v <= CurrentVersion; v++ {
		out = append(out, v)
	}
	return out
}
