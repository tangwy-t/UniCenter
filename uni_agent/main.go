// Command uni_agent 是 UniCenter 的设备侧采集代理。
//
// 职责：采集本机真实指标 → 经一根外向 WebSocket 上报给 uni_core。
// **不开放任何入站端口**，core 也不反向连接 agent（spec §2）。
//
// 运行：
//
//	uni_agent -url ws://127.0.0.1:8088/api/v1/agent/ws -enroll-token <token>
//
// 配置也可走环境变量（UNI_AGENT_URL / UNI_AGENT_ENROLL_TOKEN / ...）。
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tangwy-t/UniCenter/uni_agent/internal/collect"
	"github.com/tangwy-t/UniCenter/uni_agent/internal/config"
	"github.com/tangwy-t/UniCenter/uni_agent/internal/dockerops"
	"github.com/tangwy-t/UniCenter/uni_agent/internal/transport"
	"github.com/tangwy-t/UniCenter/uni_agent/internal/upgrade"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "uni_agent: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// -self-check：打印版本后退出。**必须在解析配置之前**处理 —— 它是给
	// 「冒烟自检」与人工查询用的（`uni_agent -self-check` 回答「这份二进制是哪一版」），
	// 不该因为缺配置而失败。
	if len(os.Args) > 1 && os.Args[1] == "-self-check" {
		fmt.Println(upgrade.SelfCheck(config.DefaultVersion))
		return nil
	}

	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		return err
	}
	log := newLogger()

	// ── 升级事务检查：**读配置之后、连网之前** ────────────────────────
	//
	// 放在这里而不是更早，是因为它要用 state-dir（配置的一部分）。但它必须在
	// 建立连接之前跑完：一个「起来就崩」的新版本要在连网之前就判定自己该退回，
	// 而不是先连一次再退。
	restart, err := upgrade.CheckOnStartup(upgrade.Deps{
		StateDir: cfg.StateDir,
		Log:      log,
		Exec:     syscall.Exec,
	})
	if err != nil {
		log.Warn("upgrade startup check failed", "err", err.Error())
	}
	if restart {
		// 回滚已经完成（备份已还原）。此处**退出**而不是继续跑：退出后由服务管理器
		// （systemd Restart=always）拉起的就是还原后的旧版本；直接继续跑的话，
		// 内存里仍是那份有问题的新版本代码。
		return errors.New("已回滚到上一版本，退出以便重新启动")
	}

	store, err := config.NewStore(cfg.StateDir)
	if err != nil {
		return err
	}
	instanceID, err := store.InstanceID()
	if err != nil {
		return err
	}

	// 采集器与传输层互相需要一点点状态（积压数、连接数），用回调解开循环依赖。
	col := collect.New(collect.Options{Interval: cfg.ReportInterval})

	hello := col.Static(cfg.AgentVersion)
	hello.InstanceID = instanceID

	client := transport.New(transport.Config{
		URL:               cfg.URL,
		EnrollToken:       cfg.EnrollToken,
		AgentToken:        store.AgentToken(),
		InstanceID:        instanceID,
		HeartbeatInterval: cfg.HeartbeatInterval,
		// wss 自签服务端的信任来源；空 = 系统信任库（明文 ws 用不到）。
		CAFile: cfg.CAFile,
		Logger: log,
	})
	client.SetHello(hello)

	// ── 升级运行时 ────────────────────────────────────────────────────
	// 它由连接层驱动（指令来自 hello_ack 内嵌或 core.agent.upgrade 推送），
	// 自身不持有连接：连接的状态机、退避、读循环都归 transport，运行时只管
	// 「拿到目标之后做什么」。三个连接侧事件（指令/连上/连失败）由 hook 注入。
	downloadBase, err := cfg.DownloadBaseURL()
	if err != nil {
		// 推导失败不阻断启动：存量设备仍要能上报指标，升级只是可选能力。
		log.Warn("cannot derive download base, auto-upgrade disabled", "err", err.Error())
	}
	// **用 NewForProcess**：它填好 os.Executable / syscall.Exec / runtime.GOOS ——
	// 这三个字段漏设不会报错，只会让升级「替换了文件却不重启进程」（端到端抓到过）。
	//
	// 下载客户端要与 WS 用**同一份 CA 信任**：切到 wss 之后产物地址由 wss→https 推导
	//（config.DownloadBaseURL），服务端仍是那张自签证书 —— 只给 WS 配 CA 而漏了这里，
	// 症状是「连接一直正常，但下一次升级永远失败在 x509」，且失败出现在升级链路上。
	//
	// 超时为什么是 30 分钟：http.Client.Timeout 是**整条请求**（建连 + 收完响应体）
	// 的绝对上限，不是 nginx 那种「空闲多久算死」—— 5 分钟会让弱网下仍在推进的慢下载
	// 死在 agent 侧，而反代（nginx 3600s 空闲超时）与 core（LongLived 滚动截止，
	// 无整条请求总时限）都已放行了它，症状是「升级永远下不完」。也不能干脆取消总时限：
	// download 的调用上下文由 upgrade.run 内部构造（Background，无取消源），没有总时限
	// 时一条半死连接（连着但不再来数据）会把升级事务永久卡在 downloading（running
	// 单飞还会连带挡掉后续所有指令）。30 分钟对默认上限 ≤200MB 的程序包 ≈ 均速
	// 114KiB/s 即可完成；更慢的链路走 download_failed 上报 + 重试，好过把状态机挂死。
	const downloadTimeout = 30 * time.Minute
	downloadClient, err := transport.CAClient(cfg.CAFile, downloadTimeout)
	if err != nil {
		// 不阻断启动：指数上报是主链路，升级是可选能力（与 downloadBase 推导失败同一取向）。
		log.Warn("cannot build download client with CA, auto-upgrade may fail", "err", err.Error())
	}
	if downloadClient == nil {
		// CAFile 为空（或无 CA 可加载）时 CAClient 契约返回 nil = 用系统信任库；
		// 而 upgrade.New 对 nil 客户端的兜底默认同样是 5 分钟 —— 同一条「慢下载死在
		// 总时限」缺陷的另一半（无 CA 链路）。显式补一个同口径的客户端：信任库语义
		// 不变（nil Transport = 系统信任库），只把两条下载链路的时限对齐。
		downloadClient = &http.Client{Timeout: downloadTimeout}
	}
	upgradeRuntime := upgrade.NewForProcess(upgrade.Deps{
		Version:      cfg.AgentVersion,
		StateDir:     cfg.StateDir,
		DownloadBase: downloadBase,
		Token:        client.AgentToken,
		SendStatus:   client.SendUpgradeStatus,
		SaveToken:    store.SaveAgentToken,
		HTTPClient:   downloadClient,
		Log:          log,
		JitterMax:    30 * time.Second,
	})
	client.SetHook(upgradeRuntime)
	// 试用期看门狗：覆盖「新版本卡住、连连接尝试都没有」这条否则无解的路径
	//（另一种回滚触发靠连接失败事件，而那条路径假设连接循环在跑）。
	upgradeRuntime.Start(context.Background())

	// ── Docker 运行时 ────────────────────────────────────────────────
	// 与升级运行时同一形状：门面由连接层驱动（hook 注入），自己持有快照循环与
	// 指令 worker。两个能力**互不依赖**：任一个装配失败都不该让另一个失效 ——
	// docker 探测/API 构造失败时只记日志（存量设备仍要能上报指标）。
	var dockerRuntime *dockerops.Runtime
	if dr, err := dockerops.NewForProcess(dockerops.Deps{
		StateDir:   cfg.StateDir,
		SendState:  client.SendDockerState,
		SendResult: client.SendDockerResult,
		// 流数据帧出口（三期日志 follow / 终端）：与会话管理器一起构成帧纪律
		//（agent 侧绝不能被 core 的 4006 限流断连 —— 那会连带指标与指令）。
		SendFrame: client.SendDockerFrame,
		Log:       log,
		// 初始周期用协议默认值；真实值由 hello_ack 下发覆盖（重连生效，§3.1.1）。
		Interval: 30 * time.Second,
	}); err != nil {
		log.Warn("docker runtime disabled", "err", err.Error())
	} else {
		dockerRuntime = dr
		client.SetDockerHook(dockerRuntime)
	}

	col.SetBacklogSource(
		func() int64 { return int64(client.Pending()) },
		func() int64 { return int64(client.DropCount()) },
	)

	log.Info("uni_agent starting",
		"version", cfg.AgentVersion,
		"url", cfg.URL,
		"instanceId", instanceID,
		"hostname", hello.Hostname,
		"interval", cfg.ReportInterval.String(),
		"hasToken", store.AgentToken() != "",
		"downloadBase", downloadBase,
		// 是否注入了自签 CA：wss 场景下这一项为 false 而 url 是 wss 时，
		// 连接失败几乎注定是信任链问题 —— 让它出现在启动日志里省一轮排障。
		"caFile", cfg.CAFile,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 采集循环：独立 goroutine，与上报完全解耦。
	//
	// 解耦的意义在于**断线时采集不停**（spec §4.3）：连接断了，
	// 采集照常把样本投进有界缓冲，恢复后能补发最近 5 分钟的数据。
	// 若两者串行（采一帧才能发一帧），断线期间就彻底没有数据了。
	go collectLoop(ctx, col, client, cfg.ReportInterval, log)

	if dockerRuntime != nil {
		// 采集与指令执行**分成两个循环**：一条 15 分钟的大指令（二期的 save）
		// 不该让快照停摆。
		go dockerRuntime.SnapshotLoop(ctx)
		go dockerRuntime.Run(ctx)
	}

	// 连接主循环（内部自带指数退避重连），阻塞到 ctx 取消。
	if err := client.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}

	// 退出前把最新凭据落盘，供下次启动直接鉴权。
	if tok := client.AgentToken(); tok != "" {
		if err := store.SaveAgentToken(tok); err != nil {
			log.Warn("save agent token failed", "err", err.Error())
		}
	}
	log.Info("uni_agent stopped")
	return nil
}

// collectLoop 周期采集并投递样本。
func collectLoop(ctx context.Context, col *collect.Collector, client *transport.Client, interval time.Duration, log *logger) {
	// 启动时先采一帧「预热」：速率类指标（disk_io/nics）需要基线，
	// 首帧只为播下基线（其速率字段为 0），从第二帧起才有真实速率。
	if _, err := col.Sample(time.Now()); err != nil {
		log.Warn("warmup sample failed", "err", err.Error())
	}
	// core 可能在 hello_ack 里下发不同的上报周期，故按短间隔轮询取最新值，
	// 而不是把 interval 固定成启动时的值。
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	next := time.Now().Add(interval)

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			now := time.Now()
			if now.Before(next) {
				continue
			}
			// 每次都用 core 下发的周期重算下一次时点（拿到后用下发的值）。
			iv := client.ReportInterval()
			if iv <= 0 {
				iv = interval
			}
			next = now.Add(iv)

			s, err := col.Sample(now)
			if err != nil {
				// 采集失败只跳过本帧：多数是某个指标临时读不到，
				// 不该让整个 agent 退出（下一帧大概率能恢复）。
				log.Warn("collect failed", "err", err.Error())
				continue
			}
			client.Send(s)
		}
	}
}

// logger 是 transport.Logger 的简单实现（不引入 zap，保持 agent 轻量）。
type logger struct{}

func newLogger() *logger { return &logger{} }

func (l *logger) Info(msg string, kv ...any)  { l.print("INFO", msg, kv...) }
func (l *logger) Warn(msg string, kv ...any)  { l.print("WARN", msg, kv...) }
func (l *logger) Debug(msg string, kv ...any) { l.print("DEBUG", msg, kv...) }

func (l *logger) print(level, msg string, kv ...any) {
	fmt.Fprintf(os.Stderr, "%s %-5s %s", time.Now().Format("2006-01-02 15:04:05"), level, msg)
	for i := 0; i+1 < len(kv); i += 2 {
		fmt.Fprintf(os.Stderr, " %v=%v", kv[i], kv[i+1])
	}
	fmt.Fprintln(os.Stderr)
}
