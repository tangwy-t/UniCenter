// Package lifecycle provides a reusable manager for graceful shutdown.
// Each component registers a stop hook during construction; at process exit
// the manager runs all hooks in reverse registration order (LIFO), so
// resources created first (infrastructure) are torn down last.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"

	"go.uber.org/zap"
)

type ManagerInterface interface {
	// RegisterTo registers a shutdown hook to a specific phase.
	RegisterTo(phaseName, name string, fn StopFunc)
}

// StopFunc is a shutdown hook. ctx carries the shutdown deadline; hooks should
// honor it (e.g. http.Server.Shutdown does) and return nil on success.
type StopFunc func(ctx context.Context) error

// Phase defines a named shutdown phase. Timeout is the budget granted to
// EACH hook in the phase (not shared across hooks): a hung hook only spends
// its own budget, and every subsequent component still gets its full chance
// to clean up. Worst-case phase duration is len(hooks) × Timeout.
type Phase struct {
	Name    string
	Timeout time.Duration
}

type namedStop struct {
	name string
	fn   StopFunc
}

// Manager collects shutdown hooks organized into phases and runs them in LIFO
// order per phase. Methods are nil-safe: a nil *Manager makes all methods no-ops,
// so callers (e.g. tests) may pass nil to opt out of lifecycle management.
type Manager struct {
	stops  map[string][]namedStop
	phases map[string]time.Duration
	order  []string
	log    logger.LoggerInterface
}

// New creates a Manager that logs shutdown progress via log.
func New(log logger.LoggerInterface) *Manager {
	return &Manager{
		stops:  make(map[string][]namedStop),
		phases: make(map[string]time.Duration),
		log:    log,
	}
}

// DefinePhases defines the shutdown phases and their order.
// Phases are executed in the order they are defined.
func (m *Manager) DefinePhases(phases ...Phase) {
	if m == nil {
		return
	}
	for _, p := range phases {
		m.phases[p.Name] = p.Timeout
		m.order = append(m.order, p.Name)
		if _, ok := m.stops[p.Name]; !ok {
			m.stops[p.Name] = nil
		}
	}
}

// RegisterTo registers a shutdown hook to a specific phase.
// Panics if the phase has not been defined via DefinePhases.
func (m *Manager) RegisterTo(phaseName, name string, fn StopFunc) {
	if m == nil {
		return
	}
	if _, ok := m.phases[phaseName]; !ok {
		panic("lifecycle: phase " + phaseName + " not defined. Call DefinePhases first.")
	}
	m.stops[phaseName] = append(m.stops[phaseName], namedStop{name: name, fn: fn})
}

// ShutdownStaged runs shutdown hooks phase by phase in definition order.
// Within each phase, hooks run in reverse registration order (LIFO).
// Each hook gets a fresh Timeout budget of its own; a hook that ignores its
// ctx is force-abandoned after the budget expires and shutdown moves on.
// Phases continue even if a prior phase returns errors; all errors are
// collected and joined.
func (m *Manager) ShutdownStaged() error {
	if m == nil {
		return nil
	}
	var errs []error
	for _, phaseName := range m.order {
		timeout := m.phases[phaseName]
		m.log.Info("shutting down phase", zap.String("phase", phaseName))
		for i := len(m.stops[phaseName]) - 1; i >= 0; i-- {
			s := m.stops[phaseName][i]
			start := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			m.log.Info("shutting down", zap.String("component", s.name))
			err := m.runHook(ctx, phaseName, s)
			cancel()
			if err != nil {
				if !errors.Is(err, errHookTimeout) {
					m.log.Error("shutdown error",
						zap.String("phase", phaseName),
						zap.String("component", s.name),
						zap.Error(err))
				}
				errs = append(errs, fmt.Errorf("%s/%s: %w", phaseName, s.name, err))
				continue
			}
			m.log.Info("stopped",
				zap.String("phase", phaseName),
				zap.String("component", s.name),
				zap.Duration("elapsed", time.Since(start)))
		}
	}
	return errors.Join(errs...)
}

// errHookTimeout 标记 hook 超时(区别于 hook 自身返回的错误)。
var errHookTimeout = errors.New("shutdown timed out")

// runHook 执行单个停止钩子并强制受 ctx 截止时间约束。
// 此前 hook 是同步调用的:忽略 ctx 的钩子(scheduler.Stop、broker.Stop 这类
// 自管超时但未必处处受控的实现)会无限阻塞整个关闭流程。现在超时后记录
// 错误并继续后续钩子 —— 孤儿 goroutine 在进程退出时被回收,这是非协作式
// 取消的固有代价,换来的是关闭流程始终有界。
func (m *Manager) runHook(ctx context.Context, phase string, s namedStop) error {
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				m.log.Error("shutdown hook panic",
					zap.String("phase", phase),
					zap.String("component", s.name),
					zap.Any("panic", r))
				done <- fmt.Errorf("panic: %v", r)
			}
		}()
		done <- s.fn(ctx)
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		m.log.Error("shutdown hook timed out, continuing",
			zap.String("phase", phase),
			zap.String("component", s.name),
			zap.Duration("elapsed", time.Since(start)),
			zap.Error(ctx.Err()))
		return fmt.Errorf("%w after %s", errHookTimeout, time.Since(start).Round(time.Millisecond))
	}
}
