package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/UniCenter/uni_core/internal/middleware"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/app"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/service"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 构建上下文上传端点（v1.3·core ② 后端）────────────────────────────────
//
// 权限（docker:manage 静态 perm）在**路由层**（router 源码守卫钉住挂载形态），
// 处理器层测试只管剩下的契约：Content-Type 判形、未装配 500、成功 200 的
// {filename} 信封形状、以及 service 的错误结论句透明转达。中转本身的流式/
// 分片/哈希由 service 层的账本替身测试覆盖（docker_build_context_test.go），
// 这里用真实 service + 假通道 —— 两层各守各的边界。

// fakeUploadChannel 是 handler 测试用的通道替身：只吃帧不记账（service 层
// 已记），离线可拨。
type fakeUploadChannel struct {
	offline bool
}

func (f *fakeUploadChannel) SendBinaryToDevice(_ context.Context, _ uint64, _ []byte) error {
	if f.offline {
		return errOffline
	}
	return nil
}

func (f *fakeUploadChannel) SendToDevice(_ uint64, _ *agentproto.Message) error {
	if f.offline {
		return errOffline
	}
	return nil
}

// errOffline 立一「离线」哨兵：handler 测试不依赖 agenthub 的具体错误。
type offlineErr struct{}

func (offlineErr) Error() string { return "device offline" }

var errOffline = offlineErr{}

func newBuildCtxHandlerFunc(ch *fakeUploadChannel, online bool) gin.HandlerFunc {
	svc := service.NewDockerBuildContextService(ch, logger.NewNop())
	svc.WithOnline(func(uint64) bool { return online })
	// 固定会话号：文件名由它推导，断言不靠运气。
	//（idGen 由 service 包内部持有，这里经固定会话号只断言响应形状。）
	hdl := NewDockerHandler(nil, nil, nil, nil, nil).WithBuildContext(svc)
	return hdl.BuildContextUpload
}

// uploadContext 造一条对着端点的 gin 上下文（带 JWT claims 的 uid —— 顺带
// 钉住「处理器不依赖 claims 之外的会话状态」这一事实）。
func uploadContext(w *httptest.ResponseRecorder, ct string, body []byte, contentLen int64) (*gin.Context, context.CancelFunc) {
	c, _ := gin.CreateTestContext(w)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/docker/hosts/7/build-context",
		bytes.NewReader(body)).WithContext(ctx)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.ContentLength = contentLen
	c.Request = req
	c.Params = gin.Params{{Key: "id", Value: "7"}}
	c.Set(middleware.CtxClaims, testClaims(42))
	return c, cancel
}

// wantGzip 一段过魔数校验的 body（handler 层不关心 tar 内容）。
func wantGzip(n int) []byte {
	b := make([]byte, n)
	b[0], b[1] = 0x1f, 0x8b
	return b
}

func TestBuildContextUploadSuccessRespondsFilename(t *testing.T) {
	ch := &fakeUploadChannel{}
	w := httptest.NewRecorder()
	c, cancel := uploadContext(w, "application/octet-stream", wantGzip(1024), 1024)
	defer cancel()

	newBuildCtxHandlerFunc(ch, true)(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200（body: %s）", w.Code, w.Body.String())
	}
	var resp app.Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("响应不是 app.Response 信封: %v", err)
	}
	data, err := json.Marshal(resp.Data)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		FileName string `json:"filename"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("载荷不是 {filename}: %v", err)
	}
	if !agentproto.IsDockerBuildContextFilename(payload.FileName) ||
		!strings.HasPrefix(payload.FileName, "build-ctx-") {
		t.Fatalf("filename = %q，必须是推导名形态（build-ctx-<会话号>.tar.gz）", payload.FileName)
	}
}

func TestBuildContextUploadUnassembled(t *testing.T) {
	w := httptest.NewRecorder()
	c, cancel := uploadContext(w, "application/octet-stream", wantGzip(16), 16)
	defer cancel()

	// 未装配：NewDockerHandler 之后不 WithBuildContext。
	NewDockerHandler(nil, nil, nil, nil, nil).BuildContextUpload(c)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("未装配应 500: %d", w.Code)
	}
}

func TestBuildContextUploadContentTypeGuard(t *testing.T) {
	ch := &fakeUploadChannel{}
	for _, ct := range []string{"", "application/json", "multipart/form-data; boundary=x", "text/plain"} {
		w := httptest.NewRecorder()
		c, cancel := uploadContext(w, ct, wantGzip(16), 16)
		newBuildCtxHandlerFunc(ch, true)(c)
		cancel()
		if w.Code != http.StatusBadRequest {
			t.Fatalf("Content-Type %q: status = %d, want 400（拿错端点要明说）", ct, w.Code)
		}
	}
}

func TestBuildContextUploadOffline(t *testing.T) {
	// 预检离线的结论句必须透传（service 层已定 503 语义，handler 不改写）。
	ch := &fakeUploadChannel{}
	w := httptest.NewRecorder()
	c, cancel := uploadContext(w, "application/octet-stream", wantGzip(64), 64)
	defer cancel()

	newBuildCtxHandlerFunc(ch, false)(c)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("离线应 503: %d（body: %s）", w.Code, w.Body.String())
	}
}

func TestBuildContextUploadBadContent(t *testing.T) {
	ch := &fakeUploadChannel{}

	t.Run("非 gzip 魔数 → 400", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, cancel := uploadContext(w, "application/octet-stream", []byte("plain-text-tar"), 14)
		defer cancel()
		newBuildCtxHandlerFunc(ch, true)(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
	})

	t.Run("Content-Length 超上限 → 400（即拒）", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, cancel := uploadContext(w, "application/octet-stream", wantGzip(64),
			agentproto.MaxDockerBuildContextBytes+1)
		defer cancel()
		newBuildCtxHandlerFunc(ch, true)(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
	})

	t.Run("空 body → 400", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, cancel := uploadContext(w, "application/octet-stream", nil, 0)
		defer cancel()
		newBuildCtxHandlerFunc(ch, true)(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", w.Code)
		}
	})
}
