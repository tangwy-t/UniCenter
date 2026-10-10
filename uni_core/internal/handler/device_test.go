package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/app"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
)

// stubDeviceSvc **同时**实现 DeviceServiceInterface、DeviceMetricsServiceInterface
// 与 DeviceOverviewServiceInterface，因此可以 NewDeviceHandler(stub, stub, stub)
// 一次注入三个依赖。
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

	// 总览面：overviewQuery 记录**实际传入**的查询参数（断言 handler 没有
	// 丢弃或改写 query），overviewCalls 计数（与上面同一个理由：防死代码）。
	overviewQuery *request.DeviceOverviewQuery
	overviewCalls int

	// 列表面：与总览同款 —— 记录实际传入的 DeviceQuery，断言 handler 把
	// 新增的 IP/版本/排序参数原样绑定进 service（丢一个，前端筛选就静默失效）。
	listQuery *request.DeviceQuery
	listCalls int
	listItems []response.DeviceListItem
	listErr   error
	listPage  int
	listSize  int
	listTotal int64

	// 分组/标签写面：记录实际传入的 id 与指针参数 —— 断言 handler 把请求体
	// 原样绑定（尤其是「未提供的字段必须是 nil」，否则部分更新会变成清空）。
	updateID    uint64
	updateGroup *string
	updateTags  *string
	updateCalls int
}

func (s *stubDeviceSvc) List(_ context.Context, q *request.DeviceQuery) (*app.PageResponse, error) {
	s.listQuery = q
	s.listCalls++
	if s.listErr != nil {
		return nil, s.listErr
	}
	return app.NewPageResponse(s.listItems, s.listTotal, s.listPage, s.listSize), nil
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

func (s *stubDeviceSvc) Overview(_ context.Context, q *request.DeviceOverviewQuery) (*response.DeviceOverviewResp, error) {
	s.overviewQuery = q
	s.overviewCalls++
	if s.err != nil {
		return nil, s.err
	}
	// 判别字段：Source/RangeSeconds 直接回显 query，让「handler 是否原样透传」
	// 可被断言；Devices 置一台，让响应形状不空。
	return &response.DeviceOverviewResp{
		RangeSeconds: q.Range, ResolutionSeconds: 30, Source: "redis",
		DeviceTotal: 1,
	}, nil
}

func (s *stubDeviceSvc) Resources(context.Context, uint64, string) (*response.DeviceResourcesResp, error) {
	return &response.DeviceResourcesResp{}, nil
}

func (s *stubDeviceSvc) Enable(context.Context, uint64) error  { return s.err }
func (s *stubDeviceSvc) Disable(context.Context, uint64) error { return s.err }
func (s *stubDeviceSvc) Delete(context.Context, uint64) error  { return s.err }

func (s *stubDeviceSvc) UpdateGroup(_ context.Context, id uint64, group, tags *string) error {
	s.updateID = id
	s.updateGroup = group
	s.updateTags = tags
	s.updateCalls++
	return s.err
}

func newGinCtx(method, target string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, nil)
	return c, w
}

// newGinCtxBody 与 newGinCtx 同款，但带 JSON 请求体（PATCH 端点需要）。
func newGinCtxBody(method, target, body string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, w
}

// TestDeviceHandlerListBindsQuery 钉住列表端点把全部查询参数绑定进 service ——
// 新增的 primaryIp / agentVersion / group / sortBy / sortOrder 丢一个，前端的筛选与
// 服务端排序就静默失效（页面照常出列表，只是条件被无视）。
func TestDeviceHandlerListBindsQuery(t *testing.T) {
	svc := &stubDeviceSvc{}
	h := NewDeviceHandler(svc, svc, svc)
	c, w := newGinCtx(http.MethodGet,
		"/devices?page=2&pageSize=20&hostname=web&status=1&online=true"+
			"&primaryIp=10.1.1.&agentVersion=0.6.0&group=prod&sortBy=agentVersion&sortOrder=asc")

	h.List(c)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d, want 200；body=%s", w.Code, w.Body.String())
	}
	if svc.listCalls != 1 {
		t.Fatalf("List 调用数 = %d, want 1", svc.listCalls)
	}
	q := svc.listQuery
	if q == nil {
		t.Fatal("handler 未把 DeviceQuery 传给 service")
	}
	if q.PrimaryIp != "10.1.1." {
		t.Fatalf("primaryIp 未绑定: %q", q.PrimaryIp)
	}
	if q.AgentVersion != "0.6.0" {
		t.Fatalf("agentVersion 未绑定: %q", q.AgentVersion)
	}
	if q.Group != "prod" {
		t.Fatalf("group 未绑定: %q", q.Group)
	}
	if q.SortBy != "agentVersion" {
		t.Fatalf("sortBy 未绑定: %q", q.SortBy)
	}
	if q.SortOrder != "asc" {
		t.Fatalf("sortOrder 未绑定: %q", q.SortOrder)
	}
	// 既有参数不回归
	if q.Hostname != "web" || q.Status == nil || *q.Status != 1 || q.Online == nil || !*q.Online {
		t.Fatalf("既有参数绑定回归: %+v", q)
	}
	if q.Page != 2 || q.PageSize != 20 {
		t.Fatalf("分页参数未绑定: page=%d pageSize=%d", q.Page, q.PageSize)
	}
}

func TestDeviceHandlerMetricsBindsQuery(t *testing.T) {
	svc := &stubDeviceSvc{}
	h := NewDeviceHandler(svc, svc, svc)
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
	h := NewDeviceHandler(stub, stub, stub)
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
	h := NewDeviceHandler(svc, svc, svc)
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
	h := NewDeviceHandler(svc, svc, svc)
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
	h := NewDeviceHandler(svc, svc, svc)
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
	h := NewDeviceHandler(svc, svc, svc)
	c, w := newGinCtx(http.MethodDelete, "/devices/1001")
	c.Params = gin.Params{{Key: "id", Value: "1001"}}
	h.Delete(c)
	if w.Code != http.StatusOK {
		t.Fatalf("Delete 状态 = %d, want 200", w.Code)
	}
}

// TestDeviceHandlerBadIDUsesSharedParamHelper：C2 —— 路径参数解析必须走
// app.Uint64Param（既有工具，11 个 handler、50 处使用），不得自造解析。
//
// 断言方式：错误响应里必须出现该工具的固定话术（“无效的参数 id: <原值>”）。
// 自造解析器（旧的 deviceID()）给的是「设备 ID 非法」，与本仓库其它 50 处
// 的失败信息不一致 —— 客户端无法按同一套话术定位问题。
func TestDeviceHandlerBadIDUsesSharedParamHelper(t *testing.T) {
	stub := &stubDeviceSvc{}
	h := NewDeviceHandler(stub, stub, stub)
	for name, raw := range map[string]string{"非数字": "not-a-number", "超出 uint64": "99999999999999999999999"} {
		c, w := newGinCtx(http.MethodDelete, "/devices/"+raw)
		c.Params = gin.Params{{Key: "id", Value: raw}}
		h.Delete(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s：HTTP 状态 = %d, want 400", name, w.Code)
		}
		if !strings.Contains(w.Body.String(), "无效的参数 id") {
			t.Fatalf("%s：必须复用 app.Uint64Param 的失败话术（证明没有自造解析器）, body=%s",
				name, w.Body.String())
		}
	}
}

// TestDeviceHandlerHasSwagAnnotations：C1 —— DeviceHandler 的每一个 handler 方法
// 都必须有 swag 注解块（既有 13 个 handler 文件逐方法都有）。
//
// 为什么值得守卫：注解缺失不会让任何编译/测试失败，只会让 docs/swagger.json 里
// `/devices*` 路径数为 0 —— 前端与外部集成方按文档拿不到接口，且没人会发现。
// 这里直接扫描源文件：每个 `func (h *DeviceHandler) X(` 上方的注释块必须含
// @Summary/@Tags/@Param 或 @Success/@Router（合并端点允许两个 @Success）。
func TestDeviceHandlerHasSwagAnnotations(t *testing.T) {
	src, err := os.ReadFile("device.go")
	if err != nil {
		t.Fatalf("读取 device.go: %v", err)
	}
	lines := strings.Split(string(src), "\n")

	methodRe := regexp.MustCompile(`^func \(h \*DeviceHandler\) ([A-Za-z0-9_]+)\(`)
	routers := map[string]bool{}
	checked := 0
	for i, line := range lines {
		m := methodRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		method := m[1]
		checked++
		// 收集紧邻上方的注释块（跳过空行与额外的普通注释行直到非注释行）
		block := make([]string, 0, 24)
		for j := i - 1; j >= 0; j-- {
			trimmed := strings.TrimSpace(lines[j])
			if strings.HasPrefix(trimmed, "//") {
				block = append([]string{trimmed}, block...)
				continue
			}
			break
		}
		joined := strings.Join(block, "\n")
		for _, must := range []string{"@Summary", "@Tags", "@Success", "@Router", "@Security"} {
			if !strings.Contains(joined, must) {
				t.Fatalf("%s 缺少 swag 注解 %s（docs/swagger.json 里就不会有该路径）:\n%s",
					method, must, joined)
			}
		}
		r := regexp.MustCompile(`@Router\s+(\S+)\s+\[(\w+)\]`).FindStringSubmatch(joined)
		if r == nil {
			t.Fatalf("%s 的 @Router 行格式不符（既有格式：`@Router /x/{id} [get]`）:\n%s", method, joined)
		}
		if !strings.HasPrefix(r[1], "/devices") {
			t.Fatalf("%s 的 @Router 路径 = %q, want 以 /devices 开头（既有文件不带 /api/v1 前缀）", method, r[1])
		}
		if !strings.EqualFold(r[2], "") {
			routers[r[1]+" "+strings.ToLower(r[2])] = true
		}
	}
	if checked != 9 {
		t.Fatalf("扫描到 %d 个 DeviceHandler 方法，want 9（方法增删时本守卫必须同步）", checked)
	}
	want := map[string]bool{
		"/devices get": true,
		// 总览是与 "/devices/{id}" 并列的**静态兄弟路由**（见 handler 注释）。
		// 它在 want 里显式登记，故「总览被误改成 /devices 的 query 变体」或
		// 「注解被误删」都会让本守卫红灯。
		"/devices/overview get":       true,
		"/devices/{id} get":           true,
		"/devices/{id} delete":        true,
		"/devices/{id} patch":         true,
		"/devices/{id}/metrics get":   true,
		"/devices/{id}/resources get": true,
		"/devices/{id}/enable post":   true,
		"/devices/{id}/disable post":  true,
	}
	for k := range want {
		if !routers[k] {
			t.Fatalf("缺 swag 路由注解 %q（实测 %v）", k, routers)
		}
	}
	if len(routers) != len(want) {
		t.Fatalf("swag 路由注解 = %v（%d 条）, want %d 条", routers, len(routers), len(want))
	}
}

// TestDeviceHandlerUpdateGroupBindsBody 钉住 PATCH /devices/:id 的请求体绑定：
// 出现的字段必须原样进 service。
func TestDeviceHandlerUpdateGroupBindsBody(t *testing.T) {
	svc := &stubDeviceSvc{}
	h := NewDeviceHandler(svc, svc, svc)

	c, w := newGinCtxBody(http.MethodPatch, "/devices/1001", `{"group":"prod","tags":"web,cn"}`)
	c.Params = gin.Params{{Key: "id", Value: "1001"}}
	h.UpdateGroup(c)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d, want 200；body=%s", w.Code, w.Body.String())
	}
	if svc.updateCalls != 1 || svc.updateID != 1001 {
		t.Fatalf("UpdateGroup 调用数/id = %d/%d, want 1/1001", svc.updateCalls, svc.updateID)
	}
	if svc.updateGroup == nil || *svc.updateGroup != "prod" {
		t.Fatalf("group 未绑定: %v", svc.updateGroup)
	}
	if svc.updateTags == nil || *svc.updateTags != "web,cn" {
		t.Fatalf("tags 未绑定: %v", svc.updateTags)
	}
}

// TestDeviceHandlerUpdateGroupPartialBodyLeavesAbsentFieldNil 钉住**部分更新**的
// 绑定语义：未提供的字段必须是 nil（= 不动），空串必须是非 nil 的空串指针（= 清空）。
//
// 若把未提供绑成了空串指针，PATCH 就会静默清空另一个字段 —— 这正是部分更新最容易
// 出错的边界，故单独钉一条。
func TestDeviceHandlerUpdateGroupPartialBodyLeavesAbsentFieldNil(t *testing.T) {
	svc := &stubDeviceSvc{}
	h := NewDeviceHandler(svc, svc, svc)

	c, w := newGinCtxBody(http.MethodPatch, "/devices/1001", `{"group":"prod"}`)
	c.Params = gin.Params{{Key: "id", Value: "1001"}}
	h.UpdateGroup(c)
	if w.Code != http.StatusOK {
		t.Fatalf("HTTP 状态 = %d, want 200；body=%s", w.Code, w.Body.String())
	}
	if svc.updateGroup == nil || *svc.updateGroup != "prod" {
		t.Fatalf("group 未绑定: %v", svc.updateGroup)
	}
	if svc.updateTags != nil {
		t.Fatalf("未提供的 tags 必须保持 nil（否则部分更新会误清空）: %v", *svc.updateTags)
	}

	// 空串 = 清空：必须绑成**非 nil 的空串指针**，而不是 nil。
	c2, _ := newGinCtxBody(http.MethodPatch, "/devices/1001", `{"group":""}`)
	c2.Params = gin.Params{{Key: "id", Value: "1001"}}
	h.UpdateGroup(c2)
	if svc.updateGroup == nil || *svc.updateGroup != "" {
		t.Fatalf("空串必须绑成非 nil 空串指针（=清空），got %v", svc.updateGroup)
	}
}

// TestDeviceHandlerUpdateGroupPropagatesErrorAndBadInput 覆盖错误传播与三类非法输入：
// service 的 404 必须原样透出；非法 JSON / 非法 id 必须 400 且**不触达 service**。
func TestDeviceHandlerUpdateGroupPropagatesErrorAndBadInput(t *testing.T) {
	// service 返回 404 → 不得 200
	svc := &stubDeviceSvc{err: apperror.NotFound("设备不存在")}
	h := NewDeviceHandler(svc, svc, svc)
	c, w := newGinCtxBody(http.MethodPatch, "/devices/9999", `{"group":"prod"}`)
	c.Params = gin.Params{{Key: "id", Value: "9999"}}
	h.UpdateGroup(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("service 返回 NotFound 时 HTTP 状态 = %d, want 404", w.Code)
	}

	// 非法 JSON → 400，且不得触达 service
	svc2 := &stubDeviceSvc{}
	h2 := NewDeviceHandler(svc2, svc2, svc2)
	c2, w2 := newGinCtxBody(http.MethodPatch, "/devices/1001", `{not-json}`)
	c2.Params = gin.Params{{Key: "id", Value: "1001"}}
	h2.UpdateGroup(c2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("非法请求体 HTTP 状态 = %d, want 400", w2.Code)
	}
	if svc2.updateCalls != 0 {
		t.Fatal("非法请求体不得触达 service")
	}

	// 非法 id → 400，且不得触达 service
	svc3 := &stubDeviceSvc{}
	h3 := NewDeviceHandler(svc3, svc3, svc3)
	c3, w3 := newGinCtxBody(http.MethodPatch, "/devices/xx", `{"group":"prod"}`)
	c3.Params = gin.Params{{Key: "id", Value: "xx"}}
	h3.UpdateGroup(c3)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("非法 id HTTP 状态 = %d, want 400", w3.Code)
	}
	if svc3.updateCalls != 0 {
		t.Fatal("非法 id 不得触达 service")
	}
}
