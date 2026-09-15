package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/app"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
)

// stubDeviceSvc **同时**实现 DeviceServiceInterface 与 DeviceMetricsServiceInterface，
// 因此可以 NewDeviceHandler(stub, stub) 一次注入两个依赖。
//
// 分派证据有两层，缺一不可：
//  1. metricsCalls / resourceCall 是**调用计数器** —— 只断言响应形状会被
//     「先调一次再调一次」的死代码骗过去（后一次的结果覆盖前者）；
//  2. 两个面的响应带各自独有的判别字段（整机无 resource_kind/name，
//     下钻有），断言响应确实出自被计数的那条分支。
type stubDeviceSvc struct {
	lastQuery *request.DeviceMetricsQuery
	err       error

	metricsCalls int
	resourceCall int
}

func (s *stubDeviceSvc) List(context.Context, *request.DeviceQuery) (*app.PageResponse, error) {
	return app.NewPageResponse([]response.DeviceListItem{}, 0, 1, 10), nil
}

func (s *stubDeviceSvc) GetByID(context.Context, uint64) (*response.DeviceResp, error) {
	return &response.DeviceResp{}, nil
}

func (s *stubDeviceSvc) Metrics(_ context.Context, _ uint64, q *request.DeviceMetricsQuery) (*response.DeviceMetricsResp, error) {
	s.lastQuery = q
	s.metricsCalls++
	if s.err != nil {
		return nil, s.err
	}
	return &response.DeviceMetricsResp{RangeSeconds: q.Range, ResolutionSeconds: 30, Source: "redis"}, nil
}

func (s *stubDeviceSvc) ResourceMetrics(_ context.Context, _ uint64, q *request.DeviceMetricsQuery) (*response.DeviceResourceResp, error) {
	s.lastQuery = q
	s.resourceCall++
	if s.err != nil {
		return nil, s.err
	}
	return &response.DeviceResourceResp{
		RangeSeconds: q.Range, ResolutionSeconds: 300, Source: "db",
		ResourceKind: q.Kind, Name: q.Name,
	}, nil
}

func (s *stubDeviceSvc) Resources(context.Context, uint64, string) (*response.DeviceResourcesResp, error) {
	return &response.DeviceResourcesResp{}, nil
}

func (s *stubDeviceSvc) Enable(context.Context, uint64) error  { return s.err }
func (s *stubDeviceSvc) Disable(context.Context, uint64) error { return s.err }
func (s *stubDeviceSvc) Delete(context.Context, uint64) error  { return s.err }

func newGinCtx(method, target string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, nil)
	return c, w
}

func TestDeviceHandlerMetricsBindsQuery(t *testing.T) {
	svc := &stubDeviceSvc{}
	h := NewDeviceHandler(svc, svc)
	c, w := newGinCtx(http.MethodGet, "/devices/1001/metrics?range=86400&metrics=cpu_used_percent,load1")
	c.Params = gin.Params{{Key: "id", Value: "1001"}}

	h.Metrics(c)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d, want 200；body=%s", w.Code, w.Body.String())
	}
	if svc.lastQuery == nil || svc.lastQuery.Range != 86400 {
		t.Fatalf("range 未绑定: %+v", svc.lastQuery)
	}
	if svc.lastQuery.Metrics != "cpu_used_percent,load1" {
		t.Fatalf("metrics 未绑定: %q", svc.lastQuery.Metrics)
	}
}

func TestDeviceHandlerBadIDReturns400(t *testing.T) {
	stub := &stubDeviceSvc{}
	h := NewDeviceHandler(stub, stub)
	c, w := newGinCtx(http.MethodGet, "/devices/not-a-number")
	c.Params = gin.Params{{Key: "id", Value: "not-a-number"}}
	h.GetByID(c)
	if w.Code == http.StatusOK {
		t.Fatalf("非法 id 必须返回错误状态, got %d", w.Code)
	}
	if stub.metricsCalls != 0 || stub.resourceCall != 0 {
		t.Fatal("非法 id 不得触达 service")
	}
}

func TestDeviceHandlerPropagatesAppError(t *testing.T) {
	svc := &stubDeviceSvc{err: apperror.BadRequest("range 越界")}
	h := NewDeviceHandler(svc, svc)
	c, w := newGinCtx(http.MethodGet, "/devices/1001/metrics?range=10")
	c.Params = gin.Params{{Key: "id", Value: "1001"}}
	h.Metrics(c)
	if w.Code == http.StatusOK {
		t.Fatal("service 返回 BadRequest 时 HTTP 状态不应是 200")
	}
	var ae *apperror.AppError
	if !errors.As(apperror.BadRequest("x"), &ae) {
		t.Fatal("apperror 必须是 AppError（守卫：错误类型未被改动）")
	}
}

// TestDeviceHandlerDispatchesTrendToMetrics 覆盖「整机趋势」分支：
// kind/name 都不给 → 必须调 metrics.Metrics，且**不得**调 ResourceMetrics。
func TestDeviceHandlerDispatchesTrendToMetrics(t *testing.T) {
	svc := &stubDeviceSvc{}
	h := NewDeviceHandler(svc, svc)
	c, w := newGinCtx(http.MethodGet, "/devices/1001/metrics?range=86400&metrics=cpu_used_percent")
	c.Params = gin.Params{{Key: "id", Value: "1001"}}

	h.Metrics(c)

	if svc.metricsCalls != 1 {
		t.Fatalf("整机趋势必须恰好调 1 次 metrics.Metrics, got %d", svc.metricsCalls)
	}
	if svc.resourceCall != 0 {
		t.Fatalf("整机趋势不得调 ResourceMetrics（死代码）, got %d", svc.resourceCall)
	}
	// 响应形状：整机趋势响应**没有** resource_kind/name 这两个字段
	body := w.Body.String()
	if !strings.Contains(body, `"source":"redis"`) {
		t.Fatalf("响应必须来自整机趋势分支: %s", body)
	}
	if strings.Contains(body, "resource_kind") || strings.Contains(body, `"name"`) {
		t.Fatalf("整机趋势响应不应带下钻字段（分派反了）: %s", body)
	}
}

// TestDeviceHandlerDispatchesDrillToResourceMetrics 覆盖「资源下钻」分支：
// kind 与 name 同时存在 → 必须调 metrics.ResourceMetrics，且**不得**调 Metrics。
func TestDeviceHandlerDispatchesDrillToResourceMetrics(t *testing.T) {
	svc := &stubDeviceSvc{}
	h := NewDeviceHandler(svc, svc)
	c, w := newGinCtx(http.MethodGet, "/devices/1001/metrics?range=86400&kind=disk&name=/data")
	c.Params = gin.Params{{Key: "id", Value: "1001"}}

	h.Metrics(c)

	if svc.resourceCall != 1 {
		t.Fatalf("kind+name 必须恰好调 1 次 metrics.ResourceMetrics, got %d", svc.resourceCall)
	}
	if svc.metricsCalls != 0 {
		t.Fatalf("下钻不得再调 metrics.Metrics（死代码）, got %d", svc.metricsCalls)
	}
	if svc.lastQuery == nil || svc.lastQuery.Kind != "disk" || svc.lastQuery.Name != "/data" {
		t.Fatalf("下钻参数未透传: %+v", svc.lastQuery)
	}
	// 响应形状：下钻响应带 resource_kind/name（整机趋势响应没有这两个字段）
	body := w.Body.String()
	if !strings.Contains(body, `"resource_kind":"disk"`) || !strings.Contains(body, `"name":"/data"`) {
		t.Fatalf("kind+name 应分派到 ResourceMetrics: %s", body)
	}
	if strings.Contains(body, `"source":"redis"`) {
		t.Fatalf("下钻响应不应出自整机趋势分支（分派反了）: %s", body)
	}
}

func TestDeviceHandlerDeleteUsesParam(t *testing.T) {
	svc := &stubDeviceSvc{}
	h := NewDeviceHandler(svc, svc)
	c, w := newGinCtx(http.MethodDelete, "/devices/1001")
	c.Params = gin.Params{{Key: "id", Value: "1001"}}
	h.Delete(c)
	if w.Code != http.StatusOK {
		t.Fatalf("Delete 状态 = %d, want 200", w.Code)
	}
}
