// transport 的可观测接线测试(spec 0004 票 01):经构造参数注入
// telemetry 后,入站 HTTP 头提取与网关请求 span 在真实 handler 上生效;
// 不注入时关闭态零 span、既有行为不变。Agent 侧用带 traceparent 头的
// 自定义 RoundTripper 模拟 OTel 自动埋点的客户端(票面 e2e 断言形态)。
package transport_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/magician336/flamingos/internal/gateway"
	"github.com/magician336/flamingos/internal/telemetry"
	"github.com/magician336/flamingos/internal/transport"
	"github.com/magician336/flamingos/internal/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// tracedHarness 是带可观测接线的进程内网关:与 gatewayHarness 同形,
// 但 transport.New 经构造参数注入 telemetry(spec 0004:transport 入站
// 接线 = 一个装配参数,默认不传即关闭态)。agentSC 有效时,Agent 侧
// 注入对应 traceparent 头(模拟 OTel 自动埋点);无效时不注入(无
// trace 上下文的 Agent)。
func tracedHarness(t *testing.T, clients map[string]upstream.Client, tel *telemetry.Telemetry, agentSC trace.SpanContext) *gatewayHarness {
	t.Helper()
	handler, err := transport.New(context.Background(), gateway.New(clients), transport.WithTelemetry(tel))
	if err != nil {
		t.Fatalf("构造 transport 失败: %v", err)
	}
	ts := newTestServer(t, handler)
	ct := &mcp.StreamableClientTransport{
		Endpoint:             ts.URL + "/mcp",
		DisableStandaloneSSE: true,
	}
	if agentSC.IsValid() {
		ct.HTTPClient = &http.Client{Transport: agentTraceRT{sc: agentSC}}
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "0.0.1"}, nil).
		Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatalf("连接网关失败: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return &gatewayHarness{ts: ts, cs: cs}
}

// newTestServer 起 httptest 服务并挂 t.Cleanup(供 tracedHarness 复用
// gatewayHarness 结构而不改动既有夹具)。
func newTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

// agentTraceRT 为每个出站请求注入预置的 traceparent 头——模拟已被
// OTel 埋点的 Agent,是断言"接续而非新根"的独立真值来源。
type agentTraceRT struct {
	sc trace.SpanContext
}

func (a agentTraceRT) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("traceparent", "00-"+a.sc.TraceID().String()+"-"+a.sc.SpanID().String()+"-01")
	return http.DefaultTransport.RoundTrip(req)
}

// spanFinder 在录制器中按 span 名找唯一 span。
func spanFinder(t *testing.T, sr *tracetest.SpanRecorder) func(name string) sdktrace.ReadOnlySpan {
	t.Helper()
	return func(name string) sdktrace.ReadOnlySpan {
		for _, span := range sr.Ended() {
			if span.Name() == name {
				return span
			}
		}
		t.Fatalf("录制器中找不到名为 %q 的 span,已有 %v", name, spanNames(sr))
		return nil
	}
}

// spanNames 列出录制器中全部已结束 span 的名字,供失败信息展示。
func spanNames(sr *tracetest.SpanRecorder) []string {
	names := make([]string, 0, len(sr.Ended()))
	for _, span := range sr.Ended() {
		names = append(names, span.Name())
	}
	return names
}

func TestTransportTracing(t *testing.T) {
	fakeTools := map[string]upstream.Client{
		"calc": &fakeClient{tools: []*mcp.Tool{
			{Name: "greet", InputSchema: map[string]any{"type": "object"}},
		}},
	}

	t.Run("开启接线:Agent 带 traceparent 的调用产生接续其 trace 的网关请求 span", func(t *testing.T) {
		sr := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
		t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
		agent := recordedAgentSpan(t)

		h := tracedHarness(t, fakeTools, telemetry.New(tp), agent)
		if _, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "calc_greet",
			Arguments: json.RawMessage(`{"msg":"你好"}`),
		}); err != nil {
			t.Fatalf("经网关调用失败: %v", err)
		}

		span := spanFinder(t, sr)("tools/call")
		if span.SpanKind() != trace.SpanKindServer {
			t.Errorf("网关请求 span kind 应为 server,实际 %v", span.SpanKind())
		}
		if span.SpanContext().TraceID() != agent.TraceID() {
			t.Errorf("网关 span 应接续 Agent 的 trace %s(接续而非新根),实际 %s",
				agent.TraceID(), span.SpanContext().TraceID())
		}
		if span.Parent().SpanID() != agent.SpanID() {
			t.Errorf("网关 span 父应为 Agent 注入的 %s,实际 %s", agent.SpanID(), span.Parent().SpanID())
		}
	})

	t.Run("关闭态:不接 telemetry 时零 span 且行为照旧", func(t *testing.T) {
		sr := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
		t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

		// 不传 WithTelemetry:默认关闭态。provider 只用于观察"零 span"。
		h := tracedHarness(t, fakeTools, nil, trace.SpanContext{})
		if _, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "calc_greet",
			Arguments: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatalf("关闭态调用应照常成功,实际报错: %v", err)
		}
		if got := len(sr.Ended()); got != 0 {
			t.Errorf("关闭态应零 span,实际 %d 个:%v", got, spanNames(sr))
		}
	})
}

// recordedAgentSpan 返回独立真值 span 上下文,模拟 Agent 侧已存在的
// trace(Agent 自己的 provider 不进入被测录制器)。
func recordedAgentSpan(t *testing.T) trace.SpanContext {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	defer func() { _ = tp.Shutdown(context.Background()) }()
	_, span := tp.Tracer("agent").Start(context.Background(), "Agent 请求")
	sc := span.SpanContext()
	span.End()
	return sc
}

// ---- 全链路 e2e(票 0004-01 核心验收):真实 HTTP adapter + 真实 gateway +
// 真实 transport + 假上游观察面,SpanRecorder 断言 Agent → 网关 → 上游
// trace 贯通(spec 0004 Testing Decisions,Jaeger 不进 CI)----

// headerCapture 是假上游的观察面:httptest 包装捕获每个入站请求的
// traceparent 头(黑盒观察,不依赖上游内部结构)。
type headerCapture struct {
	mu     sync.Mutex
	values []string // 全部 POST 携带的 traceparent 值(含空串,按序)
}

// wrap 把观察面包装到假上游 handler 外层。
func (h *headerCapture) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.values = append(h.values, r.Header.Get("traceparent"))
		h.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

// all 返回已捕获 traceparent 值的副本。
func (h *headerCapture) all() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.values...)
}

// newObservedUpstream 起一个带观察面的进程内假上游(go-sdk server +
// Stateless Streamable HTTP),注册 echo tool;观察面捕获收到的
// traceparent 头。
func newObservedUpstream(t *testing.T) (*httptest.Server, *headerCapture) {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-upstream", Version: "0.0.1"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "echo",
		Description: "回显原始参数字节",
		InputSchema: map[string]any{"type": "object"},
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(req.Params.Arguments)}}}, nil
	})
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	))
	capture := &headerCapture{}
	ts := httptest.NewServer(capture.wrap(mux))
	t.Cleanup(ts.Close)
	return ts, capture
}

// spansInTrace 返回录制器中属于指定 trace 的全部已结束 span。
func spansInTrace(sr *tracetest.SpanRecorder, traceID trace.TraceID) []sdktrace.ReadOnlySpan {
	var found []sdktrace.ReadOnlySpan
	for _, span := range sr.Ended() {
		if span.SpanContext().TraceID() == traceID {
			found = append(found, span)
		}
	}
	return found
}

// findSpan 在给定 span 集中按名字与 kind 找唯一匹配,找不到即失败。
func findSpan(t *testing.T, spans []sdktrace.ReadOnlySpan, name string, kind trace.SpanKind) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range spans {
		if span.Name() == name && span.SpanKind() == kind {
			return span
		}
	}
	t.Fatalf("spans 中找不到 name=%q kind=%v", name, kind)
	return nil
}

// TestTracingFullLink 是票面 e2e 断言集:同一 telemetry 接线贯穿网关与
// 上游 adapter,Agent 侧注入标准 traceparent 头后,一次 tools/call 的
// trace 应贯通 Agent → 网关(server span)→ 上游(client span + 假上游
// 观察面收到的头);Agent 不带上下文时网关自开根。
func TestTracingFullLink(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tel := telemetry.New(tp)

	fakeUp, capture := newObservedUpstream(t)
	calcClient, err := upstream.NewHTTP(context.Background(), "calc", fakeUp.URL+"/mcp",
		integrationTimeout, upstream.WithTelemetry(tel))
	if err != nil {
		t.Fatalf("构造真实上游客户端失败: %v", err)
	}
	t.Cleanup(func() { _ = calcClient.Close() })

	agent := recordedAgentSpan(t)
	h := tracedHarness(t, map[string]upstream.Client{"calc": calcClient}, tel, agent)

	call := func(t *testing.T) {
		t.Helper()
		res, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "calc_echo",
			Arguments: json.RawMessage(`{"msg":"全链路"}`),
		})
		if err != nil {
			t.Fatalf("经网关调用失败: %v", err)
		}
		if got := textOf(res); got != `{"msg":"全链路"}` {
			t.Fatalf("往返内容失真,实际 %q", got)
		}
	}

	t.Run("贯通:server/client span 同 trace 父子相连,且与 Agent 注入的同 trace(接续而非新根)", func(t *testing.T) {
		call(t)

		spans := spansInTrace(sr, agent.TraceID())
		server := findSpan(t, spans, "tools/call", trace.SpanKindServer)
		client := findSpan(t, spans, "tools/call", trace.SpanKindClient)
		if client.Parent().SpanID() != server.SpanContext().SpanID() {
			t.Errorf("client span 父应为网关 server span(%s),实际 %s",
				server.SpanContext().SpanID(), client.Parent().SpanID())
		}
		if server.Parent().SpanID() != agent.SpanID() {
			t.Errorf("server span 父应为 Agent 注入的 %s,实际 %s", agent.SpanID(), server.Parent().SpanID())
		}
	})

	t.Run("观察面:假上游收到的 traceparent 头与 client span 同 trace 同 span", func(t *testing.T) {
		call(t)

		client := findSpan(t, spansInTrace(sr, agent.TraceID()), "tools/call", trace.SpanKindClient)
		want := "00-" + client.SpanContext().TraceID().String() + "-" + client.SpanContext().SpanID().String() + "-01"
		for _, got := range capture.all() {
			if got == want {
				return
			}
		}
		t.Errorf("假上游应收到的 traceparent 与 client span 一致:\n期望 %q\n实际捕获 %v", want, capture.all())
	})

	t.Run("无上下文:Agent 不带 traceparent 时网关自开根 span", func(t *testing.T) {
		// 同一网关上另起一个不带头的 Agent 会话:无任何 trace 上下文。
		bare, err := mcp.NewClient(&mcp.Implementation{Name: "bare-agent", Version: "0.0.1"}, nil).
			Connect(context.Background(), &mcp.StreamableClientTransport{
				Endpoint:             h.ts.URL + "/mcp",
				DisableStandaloneSSE: true,
			}, nil)
		if err != nil {
			t.Fatalf("裸 Agent 连接网关失败: %v", err)
		}
		defer func() { _ = bare.Close() }()

		before := len(sr.Ended())
		if _, err := bare.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "calc_echo",
			Arguments: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatalf("裸 Agent 调用应照常成功,实际报错: %v", err)
		}

		// 新增的 tools/call server span(裸调用产生)应无父且不在 Agent 的 trace 上。
		for _, span := range sr.Ended()[before:] {
			if span.Name() != "tools/call" || span.SpanKind() != trace.SpanKindServer {
				continue
			}
			if span.Parent().IsValid() {
				t.Errorf("无上下文时网关应自开根,实际父 trace=%s span=%s",
					span.Parent().TraceID(), span.Parent().SpanID())
			}
			if span.SpanContext().TraceID() == agent.TraceID() {
				t.Error("自开根 span 不应落在 Agent 的 trace 上")
			}
			return // 断言首个新增 server span 即可
		}
		t.Fatalf("裸调用后应新增 tools/call server span,实际新增 %v", spanNames(sr)[before:])
	})
}
