package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// fakeHistoryWriter 记录写入的历史行（并可按需报错）。
type fakeHistoryWriter struct {
	rows []*entity.DockerTaskHistory
	err  error
}

func (f *fakeHistoryWriter) Upsert(_ context.Context, row *entity.DockerTaskHistory) error {
	if f.err != nil {
		return f.err
	}
	f.rows = append(f.rows, row)
	return nil
}

func terminalRec(ref string, userID uint64, action string) *dockerstate.CmdRecord {
	return &dockerstate.CmdRecord{
		Ref: ref, DeviceID: 7, Action: action, Target: "alpine", UserID: userID,
		Status: dockerstate.StatusSucceeded, CreatedAt: 1000, FinishedAt: 1500,
	}
}

// TestTaskHistoryRecorderFilters 钉住写入侧的剔除尺子（与读面同一把）：
// docker:events 常驻订阅（UserID=0）与只读动作（inspect 档）都不进历史 ——
// 否则每台主机每次重连、每次打开容器详情页都会往历史里塞行。
func TestTaskHistoryRecorderFilters(t *testing.T) {
	w := &fakeHistoryWriter{}
	r := NewDockerTaskHistoryRecorder(w, logger.NewNop())
	ctx := context.Background()

	// 常驻订阅记录（真实形态：UserID=0）与只读动作（inspect 档）都不落库。
	r.Record(ctx, terminalRec("sys1", 0, agentproto.DockerActionEvents))
	r.Record(ctx, terminalRec("ro1", 1, agentproto.DockerActionContainerLogs))
	r.Record(ctx, terminalRec("ro2", 1, agentproto.DockerActionContainerStats))
	r.Record(ctx, terminalRec("ro3", 1, agentproto.DockerActionImageInspect))
	if len(w.rows) != 0 {
		t.Fatalf("订阅/只读记录不得落库, got %d 行", len(w.rows))
	}

	// 变更类动作照常落库，且字段逐列对齐（历史行 = 终态记录的照实投影）。
	r.Record(ctx, terminalRec("ok1", 42, agentproto.DockerActionImagePull))
	if len(w.rows) != 1 {
		t.Fatalf("变更类动作必须落库, got %d 行", len(w.rows))
	}
	got := w.rows[0]
	if got.Ref != "ok1" || got.DeviceID != 7 || got.UserID != 42 ||
		got.Action != agentproto.DockerActionImagePull || got.Target != "alpine" ||
		got.Status != dockerstate.StatusSucceeded || got.AcceptedAt != 1000 || got.FinishedAt != 1500 {
		t.Fatalf("历史行字段必须与记录逐列一致: %+v", got)
	}
	if got.Summary != dockerTaskSummarySucceeded {
		t.Fatalf("成功终态的兜底结论句 = %q, want %q", got.Summary, dockerTaskSummarySucceeded)
	}

	// 只读判定来自策略表（inspect 档），image:scan 这类「读语义但 manage 档」的动作
	// 照旧留痕（与读面同一条判定，两边不得分岔）。
	r.Record(ctx, terminalRec("scan1", 42, agentproto.DockerActionImageScan))
	if len(w.rows) != 2 || w.rows[1].Ref != "scan1" {
		t.Fatalf("manage 档的读语义动作必须留痕: %+v", w.rows)
	}
}

// TestTaskHistoryRecorderSummaryAndTruncation 钉住结论句与行宽兜底：
// 结论句取 terminalSummary（与任务条目/审计同一句话），超长目标/结论按上限截断
// （尾部带 "...(truncated)" 标记 —— 列是 text，截断只为行宽有界）。
func TestTaskHistoryRecorderSummaryAndTruncation(t *testing.T) {
	w := &fakeHistoryWriter{}
	r := NewDockerTaskHistoryRecorder(w, logger.NewNop())
	ctx := context.Background()

	failed := terminalRec("f1", 1, agentproto.DockerActionImagePull)
	failed.Status = dockerstate.StatusFailed
	failed.Error = "拉取镜像失败：dial tcp: connection refused"
	r.Record(ctx, failed)
	if w.rows[0].Summary != failed.Error {
		t.Fatalf("失败结论句必须照录原文: %q", w.rows[0].Summary)
	}

	long := terminalRec("t1", 1, agentproto.DockerActionImageBuild)
	long.Target = strings.Repeat("x", dockerTaskTextMax+100)
	long.Status = dockerstate.StatusFailed
	long.Error = strings.Repeat("e", dockerTaskTextMax+100)
	r.Record(ctx, long)
	got := w.rows[1]
	if len(got.Target) > dockerTaskTextMax+len("...(truncated)") || !strings.HasSuffix(got.Target, "...(truncated)") {
		t.Fatalf("超长目标必须截断且带标记: len=%d", len(got.Target))
	}
	if len(got.Summary) > dockerTaskTextMax+len("...(truncated)") || !strings.HasSuffix(got.Summary, "...(truncated)") {
		t.Fatalf("超长结论必须截断且带标记: len=%d", len(got.Summary))
	}
}

// TestTaskHistoryRecorderFailOpen 钉住观察者纪律：落库失败只降级（不能 panic、
// 不能把错误往主链上抛 —— Record 是无返回值的 void），未装配（nil writer / nil 记录）
// 是空操作。
func TestTaskHistoryRecorderFailOpen(t *testing.T) {
	ctx := context.Background()

	w := &fakeHistoryWriter{err: errors.New("db down")}
	r := NewDockerTaskHistoryRecorder(w, logger.NewNop())
	r.Record(ctx, terminalRec("f1", 1, agentproto.DockerActionImagePull)) // 不 panic 即通过
	if len(w.rows) != 0 {
		t.Fatal("写入失败时不得留存半行")
	}

	// nil writer / nil rec / nil 接收者：全部空操作（测试装配与未装配形态）。
	NewDockerTaskHistoryRecorder(nil, logger.NewNop()).Record(ctx, terminalRec("f2", 1, agentproto.DockerActionImagePull))
	r.Record(ctx, nil)
	var nilRecorder *DockerTaskHistoryRecorder
	nilRecorder.Record(ctx, terminalRec("f3", 1, agentproto.DockerActionImagePull))
}
