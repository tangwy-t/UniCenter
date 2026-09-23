// Package config 负责 uni_agent 的启动配置与设备指纹持久化。
package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config 是 agent 的全部可配项。来源优先级：命令行 > 环境变量 > 默认值。
type Config struct {
	// URL 是 core 的 agent WebSocket 端点。
	URL string
	// EnrollToken 是首次注册用的共享令牌。
	EnrollToken string
	// ReportInterval 是上报周期。
	ReportInterval time.Duration
	// HeartbeatInterval 是应用层心跳周期。
	HeartbeatInterval time.Duration
	// StateDir 存放 instance id 与已签发的 agent token。
	// 必须**跨重启保留**：instance id 变了，core 会把它当成一台新设备，
	// 于是每次重启都多出一台「幽灵设备」。
	StateDir string
	// AgentVersion 是上报给 core 的版本号（**编译期注入**，不可配置，见 DefaultVersion）。
	AgentVersion string
	// DownloadBase 是程序包下载基址（形如 http://host:8088）；空表示由 URL 推导。
	DownloadBase string
}

// DownloadBaseURL 返回下载基址（不含路径）：显式配置优先，否则由 -url 推导。
//
// ws→http、wss→https，并**只取主机部分** —— 上报地址的路径（/api/v1/agent/ws）
// 与下载地址的路径（/api/v1/agent/releases/...）不同，推导时不能把路径一起搬过去。
func (c *Config) DownloadBaseURL() (string, error) {
	if c.DownloadBase != "" {
		return strings.TrimRight(c.DownloadBase, "/"), nil
	}
	u, err := url.Parse(c.URL)
	if err != nil {
		return "", fmt.Errorf("解析 -url 以推导下载基址: %w", err)
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	case "http", "https":
		// 已经是 http(s)：原样用（有人会把 URL 写成 http 形式）。
	default:
		return "", fmt.Errorf("无法从 %q 推导下载基址（协议 %q 不认识）", c.URL, u.Scheme)
	}
	return u.Scheme + "://" + u.Host, nil
}

// 默认值。上报周期与 core 侧 sys.agent.reportInterval 保持一致（10s）：
// 不一致时 core 会在 hello_ack 里下发正确值，但初始值合理可以少一次纠偏。
const (
	DefaultReportInterval    = 10 * time.Second
	DefaultHeartbeatInterval = 30 * time.Second
	DefaultStateDir          = "/var/lib/uni_agent"
)

// DefaultVersion 是**编译期注入**的版本号（`-X .../internal/config.DefaultVersion=vX.Y.Z`）。
//
// 必须是 var 而不是 const：ldflags 只能注入变量。默认值刻意是 `dev` 而不是某个
// 具体版本号 —— 一个没注入版本号的构建若自称 0.2.0，控制台会认为它已经是最新的，
// 而真相是「这份二进制不知道自己是哪来的」。
//
// **版本只有一个来源**：曾经有 `-version` 标志与环境变量可以覆盖它，那条路被删掉了。
// 理由是现场的真实故障：unit 文件里写死 `-version 0.1.0` 时，升级后的新进程仍自称
// 0.1.0，服务端据此认为「没升上去」→ 无限重下重装。删掉标志之后，那种 unit 会
// **启动即报错**（未知标志），比悄悄死循环好得多。
var DefaultVersion = "dev"

// Load 解析命令行与环境变量。
func Load(args []string) (*Config, error) {
	fs := flag.NewFlagSet("uni_agent", flag.ContinueOnError)
	var (
		url      = fs.String("url", envStr("UNI_AGENT_URL", ""), "core WebSocket 端点，如 ws://127.0.0.1:8088/api/v1/agent/ws")
		enroll   = fs.String("enroll-token", envStr("UNI_AGENT_ENROLL_TOKEN", ""), "首次注册令牌")
		interval = fs.Duration("interval", envDur("UNI_AGENT_REPORT_INTERVAL", DefaultReportInterval), "上报周期")
		hb       = fs.Duration("heartbeat", envDur("UNI_AGENT_HEARTBEAT_INTERVAL", DefaultHeartbeatInterval), "心跳周期")
		stateDir = fs.String("state-dir", envStr("UNI_AGENT_STATE_DIR", DefaultStateDir), "状态目录（存 instance id 与 agent token）")
		// downloadBase 只在「core 的下载入口与上报入口不同源」时才需要（例如上报走
		// 内网直连、下载走对外域名）。缺省由 -url 推导同源地址，服务端因此不必知道
		// 自己的对外地址（设计 §4）。
		downloadBase = fs.String("download-base", envStr("UNI_AGENT_DOWNLOAD_BASE", ""),
			"程序包下载基址（缺省由 -url 推导，如 http://host:8088）")
	)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	cfg := &Config{
		URL:               strings.TrimSpace(*url),
		EnrollToken:       strings.TrimSpace(*enroll),
		ReportInterval:    *interval,
		HeartbeatInterval: *hb,
		StateDir:          *stateDir,
		AgentVersion:      DefaultVersion,
		DownloadBase:      strings.TrimSpace(*downloadBase),
	}
	if cfg.URL == "" {
		return nil, errors.New("缺少 -url（或环境变量 UNI_AGENT_URL）")
	}
	if cfg.ReportInterval < time.Second {
		return nil, fmt.Errorf("上报周期过小：%s（最小 1s）", cfg.ReportInterval)
	}
	return cfg, nil
}

// Store 管理状态目录里的指纹与凭据。
type Store struct {
	dir string
}

// NewStore 打开（必要时创建）状态目录。
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("状态目录为空")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建状态目录 %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

const (
	instanceIDFile = "instance_id"
	agentTokenFile = "agent_token"
)

// InstanceID 读取设备指纹；不存在时生成并落盘。
//
// 指纹**必须稳定**：core 用 instance_id 做注册幂等与顶号判定。
// 若每次启动都变，设备列表会不断堆出重复条目，而且历史指标会与新设备脱钩。
func (s *Store) InstanceID() (string, error) {
	path := filepath.Join(s.dir, instanceIDFile)
	if b, err := os.ReadFile(path); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			return v, nil
		}
		// 文件存在但为空：说明上次写入被中断，重新生成而不是报错卡住启动。
	}
	id, err := randomHex(16)
	if err != nil {
		return "", fmt.Errorf("生成 instance id: %w", err)
	}
	// 用 0600：instance id 本身不算机密，但同目录还放 token，
	// 统一收紧权限比按文件区分更不容易漏。
	if err := os.WriteFile(path, []byte(id), 0o600); err != nil {
		return "", fmt.Errorf("写入 instance id: %w", err)
	}
	return id, nil
}

// AgentToken 读取上次 enroll 得到的凭据；不存在时返回空串（走 enroll 流程）。
func (s *Store) AgentToken() string {
	b, err := os.ReadFile(filepath.Join(s.dir, agentTokenFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// SaveAgentToken 持久化新签发的凭据。
//
// 落盘的意义：core 每次 enroll 都会**轮换** token（旧 token 立即失效），
// 若只在内存里存着，agent 一重启就又得拿 enroll token 重新注册；
// 而生产环境往往只下发 enroll token 一次。
func (s *Store) SaveAgentToken(tok string) error {
	if tok == "" {
		return nil
	}
	path := filepath.Join(s.dir, agentTokenFile)
	// 先写临时文件再 rename：避免写到一半断电留下半截 token，
	// 那会导致启动时读到一个无效凭据、每次连接都被 4001 拒绝。
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(tok), 0o600); err != nil {
		return fmt.Errorf("写入 agent token: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("提交 agent token: %w", err)
	}
	return nil
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func envStr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envDur(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	// 先按 Go duration 解析（"10s"），失败再当成纯秒数（"10"）。
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second
	}
	return def
}
