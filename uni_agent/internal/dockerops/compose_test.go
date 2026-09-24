package dockerops

import (
	"errors"
	"strings"
	"testing"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 探测结果的四种真实形态（.105/.106 实测 + v1 的历史输出）。
//
// 这一条守的是「单机 flavor 一旦定下就不换」的前提：探测错了 flavor，后续所有
// compose 命令都会用另一个客户端去操作同一个项目 —— 项目名规范化规则不同，
// 会算出两个项目并留下孤儿容器。
func TestParseComposeProbe(t *testing.T) {
	cases := []struct {
		name                string
		pluginOut           string
		pluginErr           error
		standaloneOut       string
		standaloneErr       error
		wantFlavor, wantVer string
		wantOK              bool
	}{
		{"插件（.105 实测）", "Docker Compose version v2.27.0\n", nil, "", errors.New("not found"),
			agentproto.DockerComposeFlavorPlugin, "v2.27.0", true},
		{"独立二进制 v2（.105 实测）", "", errors.New("docker: 'compose' is not a docker command"),
			"docker-compose version 2.26.1\n", nil,
			agentproto.DockerComposeFlavorStandaloneV2, "2.26.1", true},
		{"独立二进制 v1", "", errors.New("no plugin"), "docker-compose version 1.29.2, build 5becea4c\n", nil,
			agentproto.DockerComposeFlavorV1, "1.29.2", true},
		{"两者都不可用", "", errors.New("x"), "", errors.New("y"), "", "", false},
		{"输出里没有版本号", "", errors.New("x"), "docker-compose: command not found", nil, "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			flavor, ver, ok := ParseComposeProbe(c.pluginOut, c.pluginErr, c.standaloneOut, c.standaloneErr)
			if ok != c.wantOK || flavor != c.wantFlavor || ver != c.wantVer {
				t.Fatalf("got (%q,%q,%v), want (%q,%q,%v)", flavor, ver, ok, c.wantFlavor, c.wantVer, c.wantOK)
			}
		})
	}
}

// Ping 的结论句必须能区分两类原因：它们的处置方向完全相反（起服务 vs 改权限），
// 糊成一句英文会让运维朝错方向排查。
func TestTranslateDockerError(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?",
			"无法连接 docker.sock（Docker 服务未运行？）"},
		{"permission denied while trying to connect to the Docker daemon socket",
			"无权访问 docker.sock（请让 agent 以 root 运行，或加入 docker 组）"},
		{"dial unix /var/run/docker.sock: connect: no such file or directory",
			"无法连接 docker.sock（Docker 服务未运行？）"},
		{"some unexpected internal error", "无法连接 Docker"},
	}
	for _, c := range cases {
		if got := translateDockerError(errors.New(c.in)); got != c.want {
			t.Errorf("translateDockerError(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if translateDockerError(nil) != "" {
		t.Error("nil 错误必须给空串（它是「可用」的表达）")
	}
	// 结论句**不得照抄 SDK 的英文原文**（页面直接显示它）。
	// 注意：`docker.sock` 这类产品名是允许的（它就长这样，翻译成中文反而指不清），
	// 要拦的是把 "Cannot connect to the Docker daemon at unix://..." 原样丢给用户。
	for _, c := range cases {
		got := translateDockerError(errors.New(c.in))
		for _, english := range []string{"Cannot connect", "permission denied", "no such file", "connection refused"} {
			if strings.Contains(got, english) {
				t.Errorf("结论句 %q 照抄了 SDK 英文原文 %q", got, english)
			}
		}
	}
}
