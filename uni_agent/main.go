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
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tangwy-t/UniCenter/uni_agent/internal/collect"
	"github.com/tangwy-t/UniCenter/uni_agent/internal/config"
	"github.com/tangwy-t/UniCenter/uni_agent/internal/transport"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "uni_agent: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		return err
	}

	store, err := config.NewStore(cfg.StateDir)
	if err != nil {
		return err
	}
	instanceID, err := store.InstanceID()
	if err != nil {
		return err
	}

	log := newLogger()

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
		Logger:            log,
	})
	client.SetHello(hello)
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
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 采集循环：独立 goroutine，与上报完全解耦。
	//
	// 解耦的意义在于**断线时采集不停**（spec §4.3）：连接断了，
	// 采集照常把样本投进有界缓冲，恢复后能补发最近 5 分钟的数据。
	// 若两者串行（采一帧才能发一帧），断线期间就彻底没有数据了。
	go collectLoop(ctx, col, client, cfg.ReportInterval, log)

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
