package dockerops

import (
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/mount"
)

// toPorts / toMounts 是 SDK 与本域类型之间唯一的翻译层：映射错一位，页面上就会把
// 容器端口显示成宿主端口 —— 这种错误不会让任何东西编译失败，只能靠用例钉住。

func TestToPorts(t *testing.T) {
	cases := []struct {
		name string
		in   []container.Port
		want []PortInfo
	}{
		{"无端口映射", nil, nil},
		{"一条完整映射", []container.Port{{IP: "0.0.0.0", PrivatePort: 3306, PublicPort: 13306, Type: "tcp"}},
			[]PortInfo{{IP: "0.0.0.0", Private: 3306, Public: 13306, Type: "tcp"}}},
		// EXPOSE 出来但没发布到宿主的端口：PublicPort 是 0，**不能**拿私有端口顶上，
		// 否则页面会把「只对内暴露」显示成「映射到同一个宿主端口」。
		{"未发布的端口", []container.Port{{PrivatePort: 8080, Type: "tcp"}},
			[]PortInfo{{Private: 8080, Public: 0, Type: "tcp"}}},
		{"uint16 满值不溢出", []container.Port{{PrivatePort: 65535, PublicPort: 65535, Type: "udp"}},
			[]PortInfo{{Private: 65535, Public: 65535, Type: "udp"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := toPorts(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("got %d 条, want %d 条: %+v", len(got), len(c.want), got)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("第 %d 条 = %+v, want %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestToMounts(t *testing.T) {
	cases := []struct {
		name string
		in   []container.MountPoint
		want []MountInfo
	}{
		{"无挂载", nil, nil},
		{"bind 挂载", []container.MountPoint{{
			Type: mount.TypeBind, Source: "/data/mysql", Destination: "/var/lib/mysql", Mode: "rw", RW: true,
		}}, []MountInfo{{
			Type: "bind", Source: "/data/mysql", Destination: "/var/lib/mysql", Mode: "rw", RW: true,
		}}},
		// daemon 不给 Mode 时就是空串：不要替它脑补 "ro" —— 只读与否只看 RW。
		{"volume 挂载（无 Mode）", []container.MountPoint{{
			Type: mount.TypeVolume, Source: "/var/lib/docker/volumes/x/_data", Destination: "/data", RW: false,
		}}, []MountInfo{{
			Type: "volume", Source: "/var/lib/docker/volumes/x/_data", Destination: "/data", RW: false,
		}}},
		{"tmpfs（无 Source）", []container.MountPoint{{
			Type: mount.TypeTmpfs, Destination: "/tmp", RW: true,
		}}, []MountInfo{{Type: "tmpfs", Destination: "/tmp", RW: true}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := toMounts(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("got %d 条, want %d 条: %+v", len(got), len(c.want), got)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("第 %d 条 = %+v, want %+v", i, got[i], c.want[i])
				}
			}
		})
	}
}

// Docker 的容器名带前导 '/'（"/mysql"），本域类型一律存不带斜杠的名字：
// 名字会进协议、进保护清单匹配，两种写法并存迟早会漏配一条。
func TestPrimaryName(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"/mysql"}, "mysql"},
		{[]string{"/app-web-1", "/app-web-1-alias"}, "app-web-1"},
		{[]string{"no-slash"}, "no-slash"},
	}
	for _, c := range cases {
		if got := primaryName(c.in); got != c.want {
			t.Errorf("primaryName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 读数为全零时（容器刚创建、daemon 还没攒出采样点）两个差值都是 0：必须短路成 0 ——
// 真去算 0/0 会得到 NaN，而快照 JSON 序列化遇到 NaN 会直接失败。
func TestCPUPercent(t *testing.T) {
	cases := []struct {
		name string
		in   container.StatsResponse
		want float64
	}{
		{"两次采样可算", container.StatsResponse{
			CPUStats:    container.CPUStats{CPUUsage: container.CPUUsage{TotalUsage: 200}, SystemUsage: 2000, OnlineCPUs: 4},
			PreCPUStats: container.CPUStats{CPUUsage: container.CPUUsage{TotalUsage: 100}, SystemUsage: 1000},
		}, 40},
		{"OnlineCPUs 缺失时退回 per-cpu 数组", container.StatsResponse{
			CPUStats: container.CPUStats{
				CPUUsage:    container.CPUUsage{TotalUsage: 200, PercpuUsage: []uint64{1, 2}},
				SystemUsage: 2000,
			},
			PreCPUStats: container.CPUStats{CPUUsage: container.CPUUsage{TotalUsage: 100}, SystemUsage: 1000},
		}, 20},
		// 一个数都没有时按单核算：宁可给一个近似值，也不要 0/0。
		{"无 CPU 数时按单核算", container.StatsResponse{
			CPUStats:    container.CPUStats{CPUUsage: container.CPUUsage{TotalUsage: 200}, SystemUsage: 2000},
			PreCPUStats: container.CPUStats{CPUUsage: container.CPUUsage{TotalUsage: 100}, SystemUsage: 1000},
		}, 10},
		{"读数为全零（daemon 还没攒出采样点）", container.StatsResponse{}, 0},
		{"system 没动（除零）", container.StatsResponse{
			CPUStats:    container.CPUStats{CPUUsage: container.CPUUsage{TotalUsage: 200}, SystemUsage: 1000, OnlineCPUs: 4},
			PreCPUStats: container.CPUStats{CPUUsage: container.CPUUsage{TotalUsage: 100}, SystemUsage: 1000},
		}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cpuPercent(c.in); got != c.want {
				t.Errorf("cpuPercent() = %v, want %v", got, c.want)
			}
		})
	}
}

// 网速/流量是整机口径：一个容器挂在多个网络上是常态（本项目 compose 就同时挂内网与
// 反代网络），只取一张网卡会少算一半流量。
func TestSumNet(t *testing.T) {
	v := container.StatsResponse{Networks: map[string]container.NetworkStats{
		"frontend": {RxBytes: 100, TxBytes: 10},
		"backend":  {RxBytes: 250, TxBytes: 20},
	}}
	if got := sumNet(&v, func(n container.NetworkStats) uint64 { return n.RxBytes }); got != 350 {
		t.Errorf("sumNet(RxBytes) = %d, want 350", got)
	}
	if got := sumNet(&v, func(n container.NetworkStats) uint64 { return n.TxBytes }); got != 30 {
		t.Errorf("sumNet(TxBytes) = %d, want 30", got)
	}
	empty := container.StatsResponse{}
	if got := sumNet(&empty, func(n container.NetworkStats) uint64 { return n.RxBytes }); got != 0 {
		t.Errorf("无网卡时应给 0, got %d", got)
	}
}

func TestMBAndRound2(t *testing.T) {
	if got := mb(1572864); got != 1.5 {
		t.Errorf("mb(1572864) = %v, want 1.5", got)
	}
	if got := round2(33.3333); got != 33.33 {
		t.Errorf("round2(33.3333) = %v, want 33.33", got)
	}
	// 负值（计数器回绕、时钟回拨）在页面上没有意义，一律折成 0。
	if got := round2(-0.4); got != 0 {
		t.Errorf("round2(-0.4) = %v, want 0", got)
	}
}

// rateDelta 是 stats 流的速率口径（与快照 collectStats 的公式逐项一致）：
// 正常求差、计数回绕给 0、间隔不正给 0、round2 舍入 —— 四个形态各有一条。
func TestRateDelta(t *testing.T) {
	cases := []struct {
		name      string
		prev, cur uint64
		dt        float64
		want      float64
	}{
		{"正常 1s 间隔", 1000, 4000, 1, 3000},
		{"1s 间隔舍入", 0, 10, 3, 3.33},
		{"计数回绕（容器重启归零）", 4000, 100, 1, 0},
		{"零间隔", 1000, 2000, 0, 0},
		{"负间隔（时钟回拨）", 1000, 2000, -0.5, 0},
		{"持平", 1000, 1000, 1, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := rateDelta(c.prev, c.cur, c.dt); got != c.want {
				t.Errorf("rateDelta(%d, %d, %v) = %v, want %v", c.prev, c.cur, c.dt, got, c.want)
			}
		})
	}
}

// TestToEventItemDieExitCode：die 事件的 exitCode 属性被数值化带出（正常收尾 0 与
// 异常退出 137 都是合法值）；非 die 事件、属性缺失、数值化失败一律 nil ——
// 「不可考」与「0」是两个语义，翻译层不许把前者伪造进后者。
func TestToEventItemDieExitCode(t *testing.T) {
	dieMsg := func(attrs map[string]string) events.Message {
		return events.Message{Type: "container", Action: "die",
			Actor: events.Actor{ID: "ab12", Attributes: attrs}}
	}
	cases := []struct {
		name string
		msg  events.Message
		want *int32
	}{
		{"die 正常收尾 exit 0", dieMsg(map[string]string{"name": "web", "exitCode": "0"}), int32p(0)},
		{"die 被 kill exit 137", dieMsg(map[string]string{"name": "web", "exitCode": "137"}), int32p(137)},
		{"die 无 exitCode 属性", dieMsg(map[string]string{"name": "web"}), nil},
		{"die exitCode 非数值", dieMsg(map[string]string{"name": "web", "exitCode": "boom"}), nil},
		{"非 die 不取值", events.Message{Type: "container", Action: "start",
			Actor: events.Actor{ID: "ab12", Attributes: map[string]string{"exitCode": "1"}}}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := toEventItem(c.msg)
			if got.Type != string(c.msg.Type) || got.Action != string(c.msg.Action) ||
				got.ActorID != c.msg.Actor.ID {
				t.Fatalf("基础字段映射被破坏: %+v", got)
			}
			if c.want == nil {
				if got.ExitCode != nil {
					t.Fatalf("退出码应为不可考(nil)，实际 %d", *got.ExitCode)
				}
				return
			}
			if got.ExitCode == nil || *got.ExitCode != *c.want {
				t.Fatalf("退出码 = %v，want %d", got.ExitCode, *c.want)
			}
		})
	}
}

func int32p(v int32) *int32 { return &v }
