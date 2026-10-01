package dockerevents

import (
	"context"
	"errors"

	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstream"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// FrameSink 是流数据帧的投递面（agenthub.DockerFrameDeliverer 在本包的自述形态；
// 消费方自己声明接口，wireup 把 FrameRouter 装到 agenthub 的 DockerFrames 上）。
type FrameSink interface {
	DeliverDockerFrame(ctx context.Context, deviceID uint64, f *agentproto.DockerFrame) error
}

// FrameRouter 把 agent.docker.frame 按归属做**二段路由**：先给用户流会话注册表
// （dockerstream.Registry，命中即止）；它不认识（ErrNotFound）时，再交给常驻事件
// 管理器。
//
// 为什么不做成注册表的一部分：常驻订阅与用户会话的根本属性不同 —— 没有发起用户、
// 不占用户槽位、不被用户的 idle/offline sweep 清退、没有排他接入。合进同一张表会
// 让这些「不同」变成注册表里的一堆分支条件（既有三类流的账目被它们稀释）；二段
// 路由把共享压缩到一条判断 ——「用户注册表不认识」—— 而且**其它错误永不落到第二段**：
// 归属不符是伪造告警、非法帧是被拒帧，都不该换个主人再投一次。
type FrameRouter struct {
	Primary  FrameSink
	Fallback FrameSink
}

// DeliverDockerFrame 实现两段投递（wireup 把它装为 agenthub 的 DockerFrames）。
func (r FrameRouter) DeliverDockerFrame(ctx context.Context, deviceID uint64, f *agentproto.DockerFrame) error {
	if r.Primary == nil {
		if r.Fallback == nil {
			return dockerstream.ErrNotFound
		}
		return r.Fallback.DeliverDockerFrame(ctx, deviceID, f)
	}
	err := r.Primary.DeliverDockerFrame(ctx, deviceID, f)
	if err == nil || !errors.Is(err, dockerstream.ErrNotFound) || r.Fallback == nil {
		return err
	}
	return r.Fallback.DeliverDockerFrame(ctx, deviceID, f)
}
