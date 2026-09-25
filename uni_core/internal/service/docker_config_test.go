package service

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

type fakeConfigGetter struct{ vals map[string]string }

func (f fakeConfigGetter) GetString(ctx context.Context, key, def string) string {
	if v, ok := f.vals[key]; ok {
		return v
	}
	return def
}

func (f fakeConfigGetter) GetInt(ctx context.Context, key string, def int) int {
	if v, ok := f.vals[key]; ok {
		var n int
		for _, r := range v {
			if r < '0' || r > '9' {
				return def
			}
			n = n*10 + int(r-'0')
		}
		return n
	}
	return def
}

func TestDockerConfigProviderReadsThreeKeys(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	cfg := fakeConfigGetter{vals: map[string]string{
		"sys.docker.snapshotInterval": "30",
		"sys.docker.protected":        "mysql,project:uni-center",
		"sys.docker.transferDir":      "/var/lib/uni_agent/transfer",
	}}
	p := NewDockerConfigProvider(cfg, rdb, nil)
	got := p.DockerConfig(context.Background())
	if got == nil {
		t.Fatal("必须返回配置块（nil 会让 agent 一直用内置默认值）")
	}
	if got.SnapshotInterval != 30 || got.Protected != "mysql,project:uni-center" ||
		got.TransferDir != "/var/lib/uni_agent/transfer" {
		t.Fatalf("三键未读全: %+v", got)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("下发的配置必须能过协议校验: %v", err)
	}
	// 未配置时给零值（协议允许：字段可省，agent 用内置默认）
	empty := NewDockerConfigProvider(fakeConfigGetter{}, rdb, nil).DockerConfig(context.Background())
	if empty.Protected != "" || empty.SnapshotInterval != 0 {
		t.Fatalf("未配置时不该编造值: %+v", empty)
	}
	// 非法值（协议会拒）必须在**下发前**被折成合法值：一条非法的 hello_ack 会让
	// 握手整体失败（agent 判载荷非法），那比「用默认值」糟得多。
	bad := NewDockerConfigProvider(fakeConfigGetter{vals: map[string]string{
		"sys.docker.snapshotInterval": "3",
	}}, rdb, nil).DockerConfig(context.Background())
	if bad.SnapshotInterval != 0 {
		t.Fatalf("小于 10 的周期必须省略（而不是下发一个非法值），实际 %d", bad.SnapshotInterval)
	}
}

func TestConfigVersionBumpsOnlyForDockerKeys(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	p := NewDockerConfigProvider(fakeConfigGetter{}, rdb, nil)
	ctx := context.Background()
	if v := p.DockerConfig(ctx).ConfigVersion; v != 0 {
		t.Fatalf("初始版本号应为 0，实际 %d", v)
	}
	if err := p.BumpConfigVersion(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.BumpConfigVersion(ctx); err != nil {
		t.Fatal(err)
	}
	if v := p.DockerConfig(ctx).ConfigVersion; v != 2 {
		t.Fatalf("版本号必须单调递增，实际 %d", v)
	}
}
