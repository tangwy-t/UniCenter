package dockerops

import (
	"regexp"
	"strings"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// compose 标签（项目发现只读标签，不调 compose CLI —— §6.1）。
const (
	composeProjectLabel     = "com.docker.compose.project"
	composeServiceLabel     = "com.docker.compose.service"
	composeConfigFilesLabel = "com.docker.compose.project.config_files"
)

// composeVersionRe 从 `docker compose version` / `docker-compose version` 的输出里取版本号。
//
// 用「找第一个 x.y.z」而不是按固定格式切分：v1 的输出是
// 「docker-compose version 1.29.2, build 5becea4c」，v2 是「Docker Compose version v2.27.0」，
// 而 `--short` 在 v1 上根本不存在 —— 解析两种形态的唯一稳定锚点就是版本号本身。
var composeVersionRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// ParseComposeProbe 从两次探测的结果判定 compose 形态与版本（纯函数，便于单测）。
//
// 顺序即优先级：**先插件、后独立二进制**。理由不是偏好，而是 .105 同时有两者、
// 而插件是更新的那个（v2.27.0 vs 2.26.1）；定下之后就**不再改**（单一 flavor 纪律）。
func ParseComposeProbe(pluginOut string, pluginErr error, standaloneOut string, standaloneErr error) (flavor, version string, ok bool) {
	if pluginErr == nil {
		if v, found := extractComposeVersion(pluginOut); found {
			return agentproto.DockerComposeFlavorPlugin, v, true
		}
	}
	if standaloneErr == nil {
		v, found := extractComposeVersion(standaloneOut)
		if !found {
			return "", "", false
		}
		// v1 与 v2 独立二进制同名命令、输出形态不同：以主版本号区分，
		// 而 v1 与 v2 对项目名的规范化规则不同（这正是必须区分的原因）。
		if strings.HasPrefix(v, "1.") {
			return agentproto.DockerComposeFlavorV1, v, true
		}
		return agentproto.DockerComposeFlavorStandaloneV2, v, true
	}
	return "", "", false
}

// extractComposeVersion 取输出里的版本号（带 v 前缀则保留 —— 与 docker 自身的显示一致）。
func extractComposeVersion(out string) (string, bool) {
	m := composeVersionRe.FindStringSubmatch(out)
	if m == nil {
		return "", false
	}
	v := m[1] + "." + m[2] + "." + m[3]
	if strings.Contains(out, "v"+v) {
		return "v" + v, true
	}
	return v, true
}
