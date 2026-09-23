package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// 升级事务（state_dir/upgrade.state）—— **本地自动回滚**的全部状态。
//
// 为什么需要它（设计 §6.3）：远程回滚依赖「新版本起来后还能连上 core」，而恰恰在
// 最需要回滚的场景它连不上 —— 那条路会断。事务把「我换了什么、备份在哪、换了多久」
// 落盘，于是**下一个启动的进程**（可能是崩了又起的新版本，也可能是被还原的旧版本）
// 都能独立判断该继续、该确认，还是该退回。
//
// 时序：
//
//	替换前      写 phase=pending（attempts=0, at=now）
//	新进程启动   attempts++；attempts≥3 → 回滚（崩溃循环）
//	连上 core   phase 清除（升级确认）—— 成功由服务端在下一次 hello 裁决
//	试用期满后  连接失败累计 3 次 → 回滚（D3 探测预算）
//	回滚        还原备份 → 写 phase=rolled_back → exec 旧版本
//	旧版本连上  上报 rolled_back(原因) → 清除标记
const (
	transactionFile = "upgrade.state"
	attemptFile     = "upgrade.attempt"

	phasePending    = "pending"
	phaseRolledBack = "rolled_back"
)

// transaction 是升级事务的落盘形态。
type transaction struct {
	FromVersion string    `json:"from"`
	ToVersion   string    `json:"to"`
	BackupPath  string    `json:"backup"`
	RequestID   string    `json:"request_id"`
	Phase       string    `json:"phase"`
	At          time.Time `json:"at"`
	// Attempts 是新版本**启动**的次数（每次启动 +1）。到 maxStartAttempts 即判定
	// 崩溃循环 —— 这一条比试用期更快：一个启动就崩的二进制不该让设备等满 3 分钟。
	Attempts int `json:"attempts"`
	// ProbeFailures 是试用期满**之后**的连接失败计数（D3 探测预算）。
	ProbeFailures int `json:"probe_failures"`
	// Reason / Detail 回滚后由旧版本上报；Previous 记录被替换的程序文件路径。
	Reason   string `json:"reason,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Previous string `json:"previous,omitempty"`
}

// CheckOnStartup 是**新进程启动时的第一步**（在读配置、连网之前）。
//
// 它只做两件事：给启动计数 +1；发现崩溃循环就回滚并把 restart 置真（调用方据此
// 立刻 exec 旧版本）。其余判定（试用期满的探测预算）交给 Runtime 的连接事件 ——
// 那时才谈得上「连不上」。
//
// 返回 restart=true 表示「已经把旧版本还原了，请立刻重启到它」。
func CheckOnStartup(deps Deps) (restart bool, err error) {
	log := deps.Log
	if log == nil {
		log = nopLogger{}
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	tx := readTransaction(deps.StateDir)
	if tx == nil {
		return false, nil // 绝大多数启动：没有未结的升级事务
	}
	switch tx.Phase {
	case phasePending:
		tx.Attempts++
		if tx.Attempts >= maxStartAttempts {
			r := &Runtime{deps: withDefaults(deps, log, now), log: log}
			log.Warn("upgrade rollback: new version kept failing to start",
				"attempts", tx.Attempts, "to", tx.ToVersion)
			r.rollback(tx, "not_connected_after_upgrade", "新版本反复启动失败")
			// rollback 内部会 exec（注入了 Exec 时）；未注入（测试态）时也返回 true，
			// 让调用方的语义保持一致：**该重启了**。
			return true, nil
		}
		if err := writeTransaction(deps.StateDir, tx); err != nil {
			return false, err
		}
		log.Info("upgrade probation: new version starting",
			"attempt", tx.Attempts, "of", maxStartAttempts, "to", tx.ToVersion)
		return false, nil
	case phaseRolledBack:
		// 我们就是被回滚回来的旧版本：留着标记，等 Runtime 连上后上报（见 OnConnected）。
		log.Info("started after local rollback, will report",
			"from", tx.ToVersion, "to", tx.FromVersion)
		return false, nil
	default:
		return false, nil
	}
}

// withDefaults 给 rollback 用的 Runtime 补默认值（CheckOnStartup 不走 New）。
func withDefaults(deps Deps, log Logger, now func() time.Time) Deps {
	deps.Log = log
	deps.Now = now
	return deps
}

// readTransaction 读事务；文件不存在或损坏时返回 nil（损坏 = 没有未结事务：
// 一份读不出来的记录无法支撑任何判定，继续猜只会更糟）。
func readTransaction(dir string) *transaction {
	if dir == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(dir, transactionFile))
	if err != nil {
		return nil
	}
	var tx transaction
	if err := json.Unmarshal(b, &tx); err != nil {
		return nil
	}
	return &tx
}

// writeTransaction 原子写事务（临时文件 + rename）：半截 JSON 会让下一次启动
// 读不到事务，等于把设备的自愈能力悄悄关掉。
func writeTransaction(dir string, tx *transaction) error {
	if dir == "" {
		return errors.New("upgrade: 状态目录为空，无法记录升级事务")
	}
	b, err := json.Marshal(tx)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, transactionFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// clearTransaction 清除事务（升级确认 / 回滚上报完毕）。
func clearTransaction(dir string) error {
	if dir == "" {
		return nil
	}
	err := os.Remove(filepath.Join(dir, transactionFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// runSelfCheck 跑一次 `bin -self-check` 并返回它的 stdout。
//
// `-self-check` 是 agent 的一个显式入口（打印版本后退出）：冒烟自检要的不是
// 「进程能启动」，而是「它自述的版本正确」—— 后者才能证明替换的是对的那一份。
func runSelfCheck(ctx context.Context, bin string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, "-self-check")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// SelfCheck 是 `-self-check` 的输出实现（main 调用）。
func SelfCheck(version string) string {
	return "uni_agent " + version
}
