package agentproto

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// ── events 流记录（活动流，docker:events）────────────────────────────────

// 事件 JSON 的线上形状逐字钉住：core 按行解析、前端按字段渲染，任何一个 tag 写错
// 都会让活动流面板静默丢字段 —— 这类漂移在日志里看不出，只在画面上「少一个名字」。
func TestDockerEventItemJSONShape(t *testing.T) {
	e := DockerEventItem{
		T: 1790000000000, Type: DockerEventTypeContainer, Action: "start",
		ActorName: "mysql", ActorID: "ab12cd34",
	}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, key := range []string{
		`"t":1790000000000`,
		`"type":"container"`,
		`"action":"start"`,
		`"actor_name":"mysql"`,
		`"actor_id":"ab12cd34"`,
	} {
		if !strings.Contains(got, key) {
			t.Fatalf("事件 JSON 缺少 %s（线上形状漂移）: %s", key, got)
		}
	}
}

// 记录放到**帧**里走还是完整链路（帧模型复用，不另起消息类型）：编码进 Data 的
// JSON 行必须能被解码回记录并通过校验 —— core 的事件管理器按这个形状逐行解析。
func TestDockerEventItemRidesFrame(t *testing.T) {
	line, err := json.Marshal(DockerEventItem{T: 1790000000000, Type: DockerEventTypeContainer, Action: "die"})
	if err != nil {
		t.Fatal(err)
	}
	f := &DockerFrame{SessionID: "sess-0123456789abcdef", Seq: 1, Data: append(line, '\n')}
	if err := f.Validate(); err != nil {
		t.Fatalf("帧必须合法: %v", err)
	}
	var e DockerEventItem
	if err := json.Unmarshal(line, &e); err != nil || e.Validate() != nil {
		t.Fatalf("帧里事件行必须可解码: %v %v", err, e.Validate())
	}
}

// docker:events 的 options 契约：host 级 action —— 零必填、零确认，空 options 就是
// 完整形态；带上 target 也被放行（host 级动作无视 options 内容，机制上不产生指令）。
func TestDockerEventsActionOptions(t *testing.T) {
	if !IsDockerAction(DockerActionEvents) {
		t.Fatal("docker:events 必须在白名单")
	}
	if err := ValidateDockerCmdOptions(DockerActionEvents, &DockerCmdOptions{}); err != nil {
		t.Fatalf("空 options 必须通过: %v", err)
	}
	if err := ValidateDockerCmdOptions(DockerActionEvents,
		&DockerCmdOptions{Target: "anything"}); err != nil {
		t.Fatalf("带 target 也必须通过（host 级无视 options）: %v", err)
	}
}

// TestDockerEventItemValidate 钉住记录的校验：类型四选一、动作按**前缀**白名单
// （exec 系带命令后缀是合法形态）、时间与尺寸上限 —— core 逐行解码后跑的就是它，
// 白名单漏掉一条合法动作的表现是「活动流里这条永远不出现」，是静默的。
func TestDockerEventItemValidate(t *testing.T) {
	cases := []struct {
		name string
		item DockerEventItem
		want error
	}{
		{"容器 start 合法", DockerEventItem{T: 1790000000000, Type: DockerEventTypeContainer, Action: "start", ActorName: "mysql", ActorID: "ab12cd"}, nil},
		{"镜像带 sha256 id", DockerEventItem{T: 1, Type: DockerEventTypeImage, Action: "tag", ActorName: "mysql:8.0", ActorID: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}, nil},
		{"卷 destroy 合法", DockerEventItem{T: 1, Type: DockerEventTypeVolume, Action: "destroy", ActorName: "data"}, nil},
		{"网络 create 合法", DockerEventItem{T: 1, Type: DockerEventTypeNetwork, Action: "create", ActorName: "bridge"}, nil},
		{"exec 系带命令后缀合法", DockerEventItem{T: 1, Type: DockerEventTypeContainer, Action: "exec_create: /bin/sh -c ls", ActorID: "ab"}, nil},
		{"health_status 合法", DockerEventItem{T: 1, Type: DockerEventTypeContainer, Action: "health_status: healthy", ActorID: "ab"}, nil},
		{"t 缺失", DockerEventItem{Type: DockerEventTypeContainer, Action: "start"}, ErrInvalidPayload},
		{"类型不在订阅范围（service）", DockerEventItem{T: 1, Type: "service", Action: "create"}, ErrInvalidPayload},
		{"动作不在白名单", DockerEventItem{T: 1, Type: DockerEventTypeContainer, Action: "explode"}, ErrInvalidPayload},
		{"空动作", DockerEventItem{T: 1, Type: DockerEventTypeContainer}, ErrInvalidPayload},
		{"动作前缀不在白名单", DockerEventItem{T: 1, Type: DockerEventTypeContainer, Action: "banana: whatever"}, ErrInvalidPayload},
		{"主体名超上限", DockerEventItem{T: 1, Type: DockerEventTypeContainer, Action: "start", ActorName: strings.Repeat("x", MaxDockerStringBytes+1)}, ErrInvalidPayload},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.item.Validate()
			if c.want == nil {
				if err != nil {
					t.Fatalf("应通过，实际 %v", err)
				}
				return
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want errors.Is(..., %v)", err, c.want)
			}
		})
	}
}
