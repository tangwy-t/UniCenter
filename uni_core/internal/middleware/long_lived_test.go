package middleware

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// 这些用例都跑**真 HTTP 栈**（httptest + 显式配 WriteTimeout），因为被测的就是
// net/http 的连接级行为：middleware 单测里搓一个 ResponseWriter 替身只能证明
// 「我们调了 SetWriteDeadline」，证明不了「30s 的误杀真的没了、卡死的对端真的被
// 收住了」。grace 一律压到毫秒级，让用例秒级跑完。

// newDeadlineServer 起一个配了 WriteTimeout 的真实服务器：handler 由调用方给。
func newDeadlineServer(t *testing.T, writeTimeout time.Duration, register func(r *gin.Engine, grace time.Duration)) (*httptest.Server, time.Duration) {
	t.Helper()
	grace := writeTimeout // 测试里 grace 与 WriteTimeout 取同值：就是生产口径（同一把尺）
	engine := gin.New()
	register(engine, grace)
	srv := httptest.NewUnstartedServer(engine)
	srv.Config.WriteTimeout = writeTimeout
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, grace
}

// TestLongLivedRemovesAbsoluteWriteDeadline：**修的就是它** —— handler 耗时超过
// WriteTimeout 之后再写响应，必须照样送达（对照组不挂中间件：写失败、客户端拿不到）。
//
// 这正对应 QA 实测的线上形态：拉取进度流在 30.0s 被写失败掐断。
func TestLongLivedRemovesAbsoluteWriteDeadline(t *testing.T) {
	const wait = 300 * time.Millisecond
	srv, _ := newDeadlineServer(t, 100*time.Millisecond, func(r *gin.Engine, grace time.Duration) {
		r.GET("/long", LongLived(nil, grace), func(c *gin.Context) {
			time.Sleep(wait) // 超过 WriteTimeout 的绝对窗口
			c.String(http.StatusOK, "ok")
		})
		r.GET("/control", func(c *gin.Context) { // 对照组：不挂中间件
			time.Sleep(wait)
			c.String(http.StatusOK, "ok")
		})
	})

	// 挂了中间件：响应送达。
	resp, err := http.Get(srv.URL + "/long")
	if err != nil {
		t.Fatalf("长活端点取响应失败（写截止没被接管）: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("长活端点响应不完整: status=%d body=%q", resp.StatusCode, body)
	}

	// 对照组（不挂）：客户端拿不到完整响应 —— 这就是 P0 的原始形态，钉住它，
	// 免得有人「顺手」把中间件从路由上摘掉还以为只是少一层包装。
	resp2, err2 := http.Get(srv.URL + "/control")
	if err2 == nil {
		defer resp2.Body.Close()
		b2, _ := io.ReadAll(resp2.Body)
		if resp2.StatusCode == http.StatusOK && string(b2) == "ok" {
			t.Fatal("对照组居然完整送达：这台的 WriteTimeout 没生效，本用例的前提不成立")
		}
	}
}

// TestLongLivedToleratesIdleGapBetweenWrites：两帧之间**空闲超过 grace** 的流不许
// 被掐（这正是「绝对截止 → 滚动截止」的差别，也是长拉取/长构建里必需的性质：
// RUN 一步几分钟没输出是常态）。第二次写前会重新续期，故必须送达。
func TestLongLivedToleratesIdleGapBetweenWrites(t *testing.T) {
	srv, _ := newDeadlineServer(t, 100*time.Millisecond, func(r *gin.Engine, grace time.Duration) {
		r.GET("/stream", LongLived(nil, grace), func(c *gin.Context) {
			c.Header("Content-Type", "application/x-ndjson")
			c.Status(http.StatusOK)
			c.Writer.Flush()
			_, _ = c.Writer.WriteString(`{"seq":1}` + "\n")
			c.Writer.Flush()
			time.Sleep(3 * grace) // 空闲远超 grace：滚动截止不该把这条流判死
			_, _ = c.Writer.WriteString(`{"seq":2,"eof":true}` + "\n")
			c.Writer.Flush()
		})
	})

	resp, err := http.Get(srv.URL + "/stream")
	if err != nil {
		t.Fatalf("取流失败: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读流中断（空闲期间被杀）: %v", err)
	}
	for _, want := range []string{`{"seq":1}`, `{"seq":2,"eof":true}`} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("缺帧 %s，实际: %q", want, body)
		}
	}
}

// TestLongLivedStillCutsStuckWriter：写了「滚动截止」不等于放开了写 —— 对端
// 不读（TCP 收窗关死）时写必须在 grace 内失败，否则这条路由就成了「一个卡死
// 的浏览器永久占住一个 goroutine」的放大器。
func TestLongLivedStillCutsStuckWriter(t *testing.T) {
	const grace = 200 * time.Millisecond
	engine := gin.New()
	writeErr := make(chan error, 1)
	engine.GET("/stream", LongLived(nil, grace), func(c *gin.Context) {
		c.Status(http.StatusOK)
		chunk := bytes.Repeat([]byte("x"), 32<<10)
		var err error
		for i := 0; i < 8192; i++ { // 256MB：远超任何内核缓冲，必然把对端窗口打满
			if _, err = c.Writer.Write(chunk); err != nil {
				break
			}
			c.Writer.Flush()
		}
		writeErr <- err
	})
	srv := httptest.NewUnstartedServer(engine)
	srv.Config.WriteTimeout = 60 * time.Second // 本用例只钉「滚动 grace」这一层
	srv.Start()
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("连接测试服务器失败: %v", err)
	}
	defer conn.Close()
	// 发出请求后**一个字节都不读**：模拟卡死的对端。
	if _, err := fmt.Fprintf(conn, "GET /stream HTTP/1.1\r\nHost: test\r\n\r\n"); err != nil {
		t.Fatalf("写请求失败: %v", err)
	}

	select {
	case err := <-writeErr:
		if err == nil {
			t.Fatal("对端一个字节不读，服务端写却成功了：滚动 grace 没收住（写会永久阻塞）")
		}
		var ne net.Error
		if !errors.As(err, &ne) {
			t.Fatalf("期望超时类错误（deadline），实际: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("写没有在 grace 内失败：滚动截止没生效")
	}
}

// TestLongLivedTakesOverThroughWriterWrapper：中间件栈上先有人把 c.Writer 包了
// 一层（操作日志的 bodyCaptureWriter 就是这么干的，且它内嵌的是 gin.ResponseWriter
// **接口**）—— 这种情况下也必须能一路穿到 net/http 原始 writer。少一个 Unwrap
// 转发，长活接管就静默失效。
func TestLongLivedTakesOverThroughWriterWrapper(t *testing.T) {
	const wait = 300 * time.Millisecond
	srv, _ := newDeadlineServer(t, 100*time.Millisecond, func(r *gin.Engine, grace time.Duration) {
		r.GET("/long", func(c *gin.Context) {
			// 与 OperationLogMiddleware 同款：把 writer 包一层再交给下游。
			c.Writer = &bodyCaptureWriter{ResponseWriter: c.Writer, body: &bytes.Buffer{}, statusCode: http.StatusOK}
			LongLived(nil, grace)(c)
		}, func(c *gin.Context) {
			time.Sleep(wait)
			c.String(http.StatusOK, "ok")
		})
	})

	resp, err := http.Get(srv.URL + "/long")
	if err != nil {
		t.Fatalf("包装链上接管失败（响应被 30s 级绝对截止截断）: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("响应不完整: status=%d body=%q", resp.StatusCode, body)
	}
}

// TestLongLivedClearsReadDeadlineForLongBody：长活**请求体**（构建上下文上传那类）
// 不该按「首字节起 ReadTimeout 内必须传完」计 —— 只要还在传就不许掐。
func TestLongLivedClearsReadDeadlineForLongBody(t *testing.T) {
	const stall = 300 * time.Millisecond
	srv, _ := newDeadlineServer(t, 100*time.Millisecond, func(r *gin.Engine, grace time.Duration) {
		r.POST("/upload", LongLived(nil, grace), func(c *gin.Context) {
			// 先读一块，再停一会儿（模拟慢链路），然后读完。
			buf := make([]byte, 4)
			if _, err := io.ReadFull(c.Request.Body, buf); err != nil {
				c.String(http.StatusBadRequest, "read1: %v", err)
				return
			}
			time.Sleep(stall) // 超过 ReadTimeout 的绝对窗口
			rest, err := io.ReadAll(c.Request.Body)
			if err != nil {
				c.String(http.StatusBadRequest, "read2: %v", err)
				return
			}
			c.String(http.StatusOK, string(buf)+string(rest))
		})
	})

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/upload", strings.NewReader("hello-world"))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("上传失败（读截止没被接管）: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "hello-world" {
		t.Fatalf("长活请求体被掐: status=%d body=%q", resp.StatusCode, body)
	}
}

// TestRawResponseWriterUnwraps 钉住穿透链本身：gin 的 responseWriter 与
// bodyCaptureWriter 两层包装之后，必须还能拿到能设 deadline 的原始 writer。
func TestRawResponseWriterUnwraps(t *testing.T) {
	engine := gin.New()
	var gotErr error
	engine.GET("/x", func(c *gin.Context) {
		wrapped := &bodyCaptureWriter{ResponseWriter: c.Writer, body: &bytes.Buffer{}, statusCode: http.StatusOK}
		gotErr = http.NewResponseController(rawResponseWriter(wrapped)).SetWriteDeadline(time.Time{})
		c.Status(http.StatusOK)
	})
	srv := httptest.NewServer(engine)
	defer srv.Close()
	if _, err := http.Get(srv.URL + "/x"); err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if gotErr != nil {
		t.Fatalf("两层包装之后仍设不上 deadline（Unwrap 链断了）: %v", gotErr)
	}
}
