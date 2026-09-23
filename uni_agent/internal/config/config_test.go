package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestInstanceIDStableAcrossRestarts 是本包最重要的一条断言。
//
// instance_id 一旦变化，core 会把它当成**一台新设备**：
// 重复条目不断堆积，历史指标与新设备脱钩，运维只能手工清理。
// 故必须验证「同一目录读两次得到同一个 id」。
func TestInstanceIDStableAcrossRestarts(t *testing.T) {
	dir := t.TempDir()

	s1, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	id1, err := s1.InstanceID()
	if err != nil {
		t.Fatalf("InstanceID: %v", err)
	}
	if id1 == "" {
		t.Fatal("instance id 为空")
	}

	// 模拟进程重启：新建 Store 读同一目录
	s2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore(2): %v", err)
	}
	id2, err := s2.InstanceID()
	if err != nil {
		t.Fatalf("InstanceID(2): %v", err)
	}
	if id1 != id2 {
		t.Fatalf("重启后 instance id 变了：%q → %q（会导致幽灵设备）", id1, id2)
	}
}

// TestInstanceIDRecoversFromEmptyFile 验证空文件不至于让启动失败。
//
// 场景：上次写到一半被 kill，文件存在但内容为空。
// 此时应重新生成 id（退化为「换了一台设备」），而不是报错卡死启动。
func TestInstanceIDRecoversFromEmptyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, instanceIDFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	id, err := s.InstanceID()
	if err != nil {
		t.Fatalf("空文件应能恢复，实际报错: %v", err)
	}
	if id == "" {
		t.Fatal("恢复后仍为空 id")
	}
}

// TestAgentTokenRoundTrip 验证凭据持久化。
//
// 必须落盘：core 每次 enroll 都会轮换 token，旧 token 立即失效；
// 若只在内存保存，agent 一重启就要重新 enroll，而生产往往只发一次 enroll token。
func TestAgentTokenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 首次启动：没有 token，应返回空串而不是报错
	if got := s.AgentToken(); got != "" {
		t.Errorf("首次启动应为空 token，实际 %q", got)
	}

	// 禁用空 token 写入（空 token 不应覆盖已有值）
	if err := s.SaveAgentToken(""); err != nil {
		t.Errorf("保存空 token 不应报错: %v", err)
	}
	if got := s.AgentToken(); got != "" {
		t.Errorf("空 token 不应被写入，实际 %q", got)
	}

	want := "tok-abcdef123456"
	if err := s.SaveAgentToken(want); err != nil {
		t.Fatalf("SaveAgentToken: %v", err)
	}
	s2, _ := NewStore(dir)
	if got := s2.AgentToken(); got != want {
		t.Errorf("重启后 token = %q, 期望 %q", got, want)
	}
}

// TestAgentTokenFilePerm 验证凭据文件权限收紧。
func TestAgentTokenFilePerm(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	if err := s.SaveAgentToken("secret-value"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, agentTokenFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("token 文件权限 = %o, 期望 600（凭据不应全局可读）", perm)
	}
}

// TestLoadRequiresURL 验证缺 URL 时明确报错。
func TestLoadRequiresURL(t *testing.T) {
	// 清掉可能存在的环境变量，避免测试受外部影响
	t.Setenv("UNI_AGENT_URL", "")
	if _, err := Load(nil); err == nil {
		t.Fatal("缺少 -url 时应报错")
	}
}

// TestLoadParsesFlags 验证命令行解析与默认值。
func TestLoadParsesFlags(t *testing.T) {
	cfg, err := Load([]string{
		"-url", "ws://127.0.0.1:8088/api/v1/agent/ws",
		"-enroll-token", "dev-enroll-token",
		"-interval", "5s",
		"-state-dir", t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.URL != "ws://127.0.0.1:8088/api/v1/agent/ws" {
		t.Errorf("URL = %q", cfg.URL)
	}
	if cfg.EnrollToken != "dev-enroll-token" {
		t.Errorf("EnrollToken = %q", cfg.EnrollToken)
	}
	if cfg.ReportInterval != 5*time.Second {
		t.Errorf("ReportInterval = %v, 期望 5s", cfg.ReportInterval)
	}
}

// TestLoadRejectsTinyInterval 验证过小周期被拒。
//
// 周期过小时速率差量的分母趋近 0，会把瞬时抖动放大成荒谬的速率；
// 而且会打爆 core 的帧率限流（sys.agent.maxFramesPerMin）。
func TestLoadRejectsTinyInterval(t *testing.T) {
	if _, err := Load([]string{"-url", "ws://x/y", "-interval", "50ms"}); err == nil {
		t.Fatal("过小的上报周期应被拒")
	}
}

// TestEnvDur 验证环境变量时长解析支持 "10s" 与纯秒数两种写法。
func TestEnvDur(t *testing.T) {
	t.Setenv("UNI_TEST_DUR", "30s")
	if got := envDur("UNI_TEST_DUR", time.Second); got != 30*time.Second {
		t.Errorf("解析 \"30s\" = %v", got)
	}
	t.Setenv("UNI_TEST_DUR", "30")
	if got := envDur("UNI_TEST_DUR", time.Second); got != 30*time.Second {
		t.Errorf("解析 \"30\" = %v", got)
	}
	t.Setenv("UNI_TEST_DUR", "garbage")
	if got := envDur("UNI_TEST_DUR", 7*time.Second); got != 7*time.Second {
		t.Errorf("非法值应回落默认值，实际 %v", got)
	}
}
