package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/dockerstate"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/service"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 容器 stats 历史端点（GET /docker/hosts/:id/containers/:cid/stats-history）的测试：
// 形状/排序/空态/404/400。鉴权（docker:inspect 挂路由）由 router 包的源码级
// 守卫钉住（路由 perm 是中间件层，handler 裸引擎测不到 —— 与事件流端点同一分工）。

// newStatsHistoryEnv 构造带一台设备（id=7）与若干留存样本的测试环境。
func newStatsHistoryEnv(t *testing.T, perms []string) *dockerTestEnv {
	t.Helper()
	env := newTestDockerHandler(t, perms)
	dev := &entity.Device{Hostname: "h1"}
	dev.ID = 7
	// 设备存在才不 404（与 State 端点同一前置）。
	env.handler.svc = service.NewDockerService(env.store,
		dockerDeviceReader{devs: map[uint64]*entity.Device{7: dev}},
		dockerCfg{}, logger.NewNop()).
		WithStatsHistory(dockerstate.NewStatsHistoryStore(env.redis))
	return env
}

// newStatsHistoryContext 造一个 GET stats-history 的测试上下文。
func newStatsHistoryContext(deviceID, cid string) (*httptest.ResponseRecorder, *gin.Context) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet,
		"/api/v1/docker/hosts/"+deviceID+"/containers/"+cid+"/stats-history", nil)
	c.Params = gin.Params{{Key: "id", Value: deviceID}, {Key: "cid", Value: cid}}
	return w, c
}

// recordStatsFrames 往 env 的留存存储写 frames 帧快照（cpu 按帧递增）。
func recordStatsFrames(t *testing.T, env *dockerTestEnv, frames int) {
	t.Helper()
	ctx := context.Background()
	base := time.UnixMilli(1730000000000)
	sh := dockerstate.NewStatsHistoryStore(env.redis)
	for f := 0; f < frames; f++ {
		at := base.Add(time.Duration(f) * 30 * time.Second)
		st := &agentproto.DockerState{T: at.UnixMilli(), DockerOK: true, Containers: []agentproto.DockerContainer{
			{ID: "cid-aaa", Name: "alpha", State: "running",
				CPUPercent: float64(f) * 5, MemUsageMB: 100 + float64(f), MemLimitMB: 512},
		}}
		if err := sh.Record(ctx, 7, st, at); err != nil {
			t.Fatal(err)
		}
	}
}

// TestStatsHistoryShapeAndOrder 钉住响应形状与排序：samples 升序、字段名驼峰、
// 值与留存逐字一致（映射漏字段/改名都会在这里红 —— 手写映射的已知风险）。
func TestStatsHistoryShapeAndOrder(t *testing.T) {
	env := newStatsHistoryEnv(t, []string{"docker:inspect"})
	recordStatsFrames(t, env, 3)

	w, c := newStatsHistoryContext("7", "cid-aaa")
	env.handler.StatsHistory(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var body struct {
		Code int `json:"code"`
		Data struct {
			Samples []struct {
				T          int64   `json:"t"`
				CPUPercent float64 `json:"cpuPercent"`
				MemUsageMB float64 `json:"memUsageMb"`
				MemLimitMB float64 `json:"memLimitMb"`
			} `json:"samples"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (%s)", err, w.Body.String())
	}
	if body.Code != 0 {
		t.Fatalf("必须走 app.Success 信封, body=%s", w.Body.String())
	}
	if len(body.Data.Samples) != 3 {
		t.Fatalf("应有 3 个样本, got %d (%s)", len(body.Data.Samples), w.Body.String())
	}
	base := time.UnixMilli(1730000000000)
	for f, sm := range body.Data.Samples {
		wantT := base.Add(time.Duration(f) * 30 * time.Second).UnixMilli()
		if sm.T != wantT {
			t.Fatalf("样本 %d 时刻不符（升序 + core 收帧时刻）: got %d want %d", f, sm.T, wantT)
		}
		if sm.CPUPercent != float64(f)*5 || sm.MemUsageMB != 100+float64(f) || sm.MemLimitMB != 512 {
			t.Fatalf("样本 %d 字段值不符: %+v", f, sm)
		}
	}
	// 字段名必须是驼峰这套（t/cpuPercent/memUsageMb/memLimitMb）：蛇形是协议/NDJSON
	// 流的命名，HTTP DTO 是驼峰 —— 两套并存是模块约定（response/docker.go 头注）。
	for _, name := range []string{`"cpuPercent"`, `"memUsageMb"`, `"memLimitMb"`, `"t"`} {
		if !strings.Contains(w.Body.String(), name) {
			t.Fatalf("字段 %s 缺席（DTO 命名漂移）, body=%s", name, w.Body.String())
		}
	}
}

// TestStatsHistoryEmptyIsArrayNotNull 钉住空态：无历史（容器没留存）返回 200 +
// samples 空数组而非 null —— 「没有留存」是正常答案，不是错误，也不该让前端
// 多一层判空。
func TestStatsHistoryEmptyIsArrayNotNull(t *testing.T) {
	env := newStatsHistoryEnv(t, []string{"docker:inspect"})

	w, c := newStatsHistoryContext("7", "never-seen")
	env.handler.StatsHistory(c)
	if w.Code != http.StatusOK {
		t.Fatalf("无历史必须 200, got %d (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"samples":[]`) {
		t.Fatalf("空态必须是空数组而非 null, body=%s", w.Body.String())
	}
}

// TestStatsHistoryUnknownDevice404 钉住 404 话术与 State 端点一致（设备不存在）：
// ID 打错时给结论，而不是一页空曲线让人猜。
func TestStatsHistoryUnknownDevice404(t *testing.T) {
	env := newStatsHistoryEnv(t, []string{"docker:inspect"})

	w, c := newStatsHistoryContext("9", "cid-aaa")
	env.handler.StatsHistory(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("设备不存在必须 404, got %d (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "设备不存在") {
		t.Fatalf("404 话术必须与其他设备端点一致, body=%s", w.Body.String())
	}
}

// TestStatsHistoryBadParams 钉住 400：非十进制设备 id、空容器 id 都不得被悄悄
// 当成默认值处理。
func TestStatsHistoryBadParams(t *testing.T) {
	env := newStatsHistoryEnv(t, []string{"docker:inspect"})

	t.Run("路径 id 非法", func(t *testing.T) {
		w, c := newStatsHistoryContext("abc", "cid-aaa")
		env.handler.StatsHistory(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("非十进制 id 必须 400, got %d (%s)", w.Code, w.Body.String())
		}
	})
	t.Run("空容器 id", func(t *testing.T) {
		w, c := newStatsHistoryContext("7", "")
		env.handler.StatsHistory(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("空 cid 必须 400, got %d (%s)", w.Code, w.Body.String())
		}
	})
}

// TestStatsHistoryUnwiredReturns500 钉住「未装配」语义：读面没注入留存时给出 500
// （装配错误早暴露）—— 好过假装一条空曲线，那会让「容器没有历史」和「留存没接上」
// 在页面上不可区分。
func TestStatsHistoryUnwiredReturns500(t *testing.T) {
	env := newTestDockerHandler(t, nil)
	dev := &entity.Device{Hostname: "h1"}
	dev.ID = 7
	env.handler.svc = service.NewDockerService(env.store,
		dockerDeviceReader{devs: map[uint64]*entity.Device{7: dev}},
		dockerCfg{}, logger.NewNop()) // 无 WithStatsHistory

	w, c := newStatsHistoryContext("7", "cid-aaa")
	env.handler.StatsHistory(c)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("未装配必须 500, got %d (%s)", w.Code, w.Body.String())
	}
}
