package agenthub

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
)

type fakeDockerState struct {
	saved []uint64
	err   error
}

func (f *fakeDockerState) SaveDockerState(ctx context.Context, deviceID uint64, st *agentproto.DockerState) error {
	f.saved = append(f.saved, deviceID)
	return f.err
}

type fakeDockerResult struct {
	got []string
}

func (f *fakeDockerResult) CompleteDockerCmd(ctx context.Context, deviceID uint64, res *agentproto.DockerCmdResult) error {
	f.got = append(f.got, res.Ref)
	return nil
}

// fakeDockerFrames 是 DockerFrameDeliverer 的替身：记录投递进来的帧与设备归属。
type fakeDockerFrames struct {
	got  []*agentproto.DockerFrame
	devs []uint64
	err  error
}

func (f *fakeDockerFrames) DeliverDockerFrame(ctx context.Context, deviceID uint64, fr *agentproto.DockerFrame) error {
	f.got = append(f.got, fr)
	f.devs = append(f.devs, deviceID)
	return f.err
}

// newTestConn 构造一条**不接管 socket** 的连接（newConn 的测试态形态，与 conn_test.go
// 的夹具同源，见那里的 newFixtureWithDeps）：docker 的三个分派处理是纯函数式接线，
// 直接调用即可驱动，不需要读循环、真实 TCP 或注册表。
func newTestConn(t *testing.T) *Conn {
	t.Helper()
	return newConn(nil, nil, Deps{}, logger.NewNop(), "")
}

// rawJSON 把逐字写出的线上 JSON 形态转成消息载荷。
func rawJSON(s string) json.RawMessage { return json.RawMessage(s) }

// 三个新类型的表项必须存在（守卫测试 TestConnDispatchCoversAllAgentToCoreTypes 会红；
// 这里直接断言表项本身，让失败信息指向「漏接线」而不是一句泛泛的断言）。
func TestDockerHandlersAreWired(t *testing.T) {
	for _, ty := range []string{agentproto.TypeAgentDockerState, agentproto.TypeAgentDockerResult, agentproto.TypeAgentDockerFrame} {
		if _, ok := agentHandlers[ty]; !ok {
			t.Fatalf("%s 未在 agentHandlers 表里（漏接线 = 连接被 4003 关闭）", ty)
		}
	}
}

// docker 载荷解码失败必须**丢弃该帧而不关连接**：docker 是搭便车能力，
// 它的载荷问题不得断掉指标主链路（断连期间指标、心跳、升级全部陪葬）。
func TestDockerStateHandlerDropsBadPayloadWithoutClosing(t *testing.T) {
	c := newTestConn(t) // 复用 conn_test.go 的测试连接构造
	c.deps.DockerState = &fakeDockerState{}
	bad := &agentproto.Message{V: agentproto.CurrentVersion, ID: "1",
		Type: agentproto.TypeAgentDockerState, TS: 1, Data: rawJSON(`{"t":0,"docker_ok":true}`)}
	if keep := c.handleDockerState(context.Background(), bad); !keep {
		t.Fatal("载荷非法时必须保留连接（返回 false = 关连接）")
	}
	if len(c.deps.DockerState.(*fakeDockerState).saved) != 0 {
		t.Fatal("非法载荷不得落库")
	}
}

// 合法 state 必须落库；未装配时不 panic（与升级域的 nil 容忍同一取向）。
func TestDockerStateHandlerSaves(t *testing.T) {
	c := newTestConn(t)
	sink := &fakeDockerState{}
	c.deps.DockerState = sink
	ok := &agentproto.Message{V: agentproto.CurrentVersion, ID: "1", Type: agentproto.TypeAgentDockerState, TS: 1,
		Data: rawJSON(`{"t":1790000000000,"docker_ok":true,"containers":[]}`)}
	if keep := c.handleDockerState(context.Background(), ok); !keep {
		t.Fatal("合法载荷不该关连接")
	}
	if len(sink.saved) != 1 {
		t.Fatalf("快照未落库: %v", sink.saved)
	}
	c.deps.DockerState = nil
	if keep := c.handleDockerState(context.Background(), ok); !keep {
		t.Fatal("未装配时也必须保留连接")
	}
}

// result 帧按 ref 归属落库；frame 帧投递给会话注册表（三期接线）。
func TestDockerResultAndFrameHandlers(t *testing.T) {
	c := newTestConn(t)
	sink := &fakeDockerResult{}
	c.deps.DockerResult = sink
	res := &agentproto.Message{V: agentproto.CurrentVersion, ID: "1", Type: agentproto.TypeAgentDockerResult, TS: 1,
		Data: rawJSON(`{"ref":"1790000000001","ok":true}`)}
	if keep := c.handleDockerResult(context.Background(), res); !keep {
		t.Fatal("结果帧不该关连接")
	}
	if len(sink.got) != 1 || sink.got[0] != "1790000000001" {
		t.Fatalf("结果未落库: %v", sink.got)
	}
	frame := &agentproto.Message{V: agentproto.CurrentVersion, ID: "2", Type: agentproto.TypeAgentDockerFrame, TS: 1,
		Data: rawJSON(`{"session_id":"0123456789abcdef","seq":1,"data":"aGk="}`)}
	c.deps.DockerFrames = nil // 未装配投递面：丢弃但不 panic、不关连接
	if keep := c.handleDockerFrame(context.Background(), frame); !keep {
		t.Fatal("未装配投递面时收到流帧也不该关连接（nil 容忍）")
	}
}

// TestDockerFrameHandlerDelivers：装配后帧必须带着**连接的 deviceID** 投递；
// 投递错误（会话不存在/已结束）不得关连接 —— docker 帧的问题永远不断指标主链路。
func TestDockerFrameHandlerDelivers(t *testing.T) {
	c := newTestConn(t)
	sink := &fakeDockerFrames{}
	c.deps.DockerFrames = sink
	frame := &agentproto.Message{V: agentproto.CurrentVersion, ID: "2", Type: agentproto.TypeAgentDockerFrame, TS: 1,
		Data: rawJSON(`{"session_id":"0123456789abcdef","seq":3,"data":"aGk=","eof":true}`)}
	if keep := c.handleDockerFrame(context.Background(), frame); !keep {
		t.Fatal("投递成功也不该关连接")
	}
	if len(sink.got) != 1 || sink.got[0].Seq != 3 || !sink.got[0].EOF || string(sink.got[0].Data) != "hi" {
		t.Fatalf("帧未原样投递: %+v", sink.got)
	}
	if sink.devs[0] != c.deviceID() {
		t.Fatalf("投递必须带连接的 deviceID: %v vs %d", sink.devs, c.deviceID())
	}
	sink.err = errors.New("session gone")
	if keep := c.handleDockerFrame(context.Background(), frame); !keep {
		t.Fatal("投递失败必须保留连接（docker 载荷问题只丢帧）")
	}
}
