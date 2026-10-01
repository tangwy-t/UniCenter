package dockerstate

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// scanImageID 是测试用的合法内容键（sha256:<64hex> 的完整形态）。
const scanImageID = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newScanCache(t *testing.T) (*ScanCacheStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return NewScanCacheStore(rdb), mr
}

func scanReport() *agentproto.DockerScanReport {
	return &agentproto.DockerScanReport{
		ImageID:   scanImageID,
		ScannedAt: 1790000000,
		Counts:    agentproto.DockerScanCounts{Critical: 2, High: 1},
		Vulns: []agentproto.DockerScanVuln{
			{ID: "CVE-2026-0001", Pkg: "openssl", Severity: agentproto.DockerScanSeverityCritical, FixedVersion: "3.0.12"},
		},
	}
}

// 回环：Save 重盖 scanned_at（core 时钟纪律 —— agent 时钟可偏，「扫描于 N 小时前」
// 以 core 收帧时刻为准），Get 原样取回，TTL 过即「没有」。
func TestScanCacheRoundTrip(t *testing.T) {
	s, mr := newScanCache(t)
	ctx := context.Background()

	if _, err := s.Get(ctx, scanImageID); err != nil {
		t.Fatalf("未写先读应是 (nil,nil) 不是错误: %v", err)
	}
	if got, _ := s.Get(ctx, scanImageID); got != nil {
		t.Fatalf("未写先读必须返回 nil: %+v", got)
	}
	at := time.Unix(1790000123, 0)
	if err := s.Save(ctx, scanImageID, scanReport(), at); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, scanImageID)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("写入后必须能读到")
	}
	if got.ScannedAt != at.Unix() {
		t.Fatalf("scanned_at 必须被重盖成 core 时刻 %d，实际 %d（agent 的 %d 不得透传进缓存）",
			at.Unix(), got.ScannedAt, scanReport().ScannedAt)
	}
	if got.Counts != (agentproto.DockerScanCounts{Critical: 2, High: 1}) || len(got.Vulns) != 1 {
		t.Fatalf("报告字段必须原样回放: %+v", got)
	}
	// 24h TTL 到点 → 缓存消失（键空间自清理是唯一收口，见键设计注释）。
	mr.FastForward(ScanCacheTTL)
	if got, _ := s.Get(ctx, scanImageID); got != nil {
		t.Fatal("TTL 过后缓存必须消失（24h 是新鲜度的全部语义）")
	}
}

// 形态闸：畸形 imageID（截断 ID / 非法字符 / 注入形态）进不了键 ——
// Save 拒绝、Get 视同没有；键空间只有「sha256:hex64」一种形状。
func TestScanCacheKeyShape(t *testing.T) {
	s, _ := newScanCache(t)
	ctx := context.Background()
	for _, bad := range []string{
		"",
		"nginx:1.27",
		"sha256:0123",             // 截断 ID
		"sha256:0123456789ABCDEF", // 大写
		"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcde ", // 尾随空白
		"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcde\nkey",
	} {
		if err := s.Save(ctx, bad, scanReport(), time.Now()); err == nil {
			t.Fatalf("畸形键 %q 必须被拒绝", bad)
		}
		if got, err := s.Get(ctx, bad); err != nil || got != nil {
			t.Fatalf("畸形键 %q 的读取必须视同没有: got=%v err=%v", bad, got, err)
		}
	}
	// 合法形态的正例（显式 —— 白名单是「放行 sha256:hex64」而不是「拒绝以上形态」）。
	if err := s.Save(ctx, scanImageID, scanReport(), time.Now()); err != nil {
		t.Fatalf("完整 sha256 ID 必须被接受: %v", err)
	}
}

// 损坏值按「没有」处理：缓存回放失败的最坏结果是重扫一次，不能把「缓存坏了」
// 升级成「扫描不可用」。
func TestScanCacheCorruptValue(t *testing.T) {
	s, mr := newScanCache(t)
	ctx := context.Background()
	if err := mr.Set(ScanCacheKeyPrefix+scanImageID, "not json"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, scanImageID)
	if err != nil || got != nil {
		t.Fatalf("损坏值必须折成 (nil,nil): got=%v err=%v", got, err)
	}
}
