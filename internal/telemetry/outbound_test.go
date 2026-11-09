// telemetry 出站接线测试:上游 Sending 中间件(child client span)与
// HTTP 出站 RoundTripper(标准头注入)。span 语义经 SpanRecorder 断言;
// RoundTripper 对构造请求注入头,断言与 ctx 当前 span 同 trace。
package telemetry_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/magician336/flamingos/internal/telemetry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// driveSending 以桩 next 与手造 ClientRequest 驱动 Sending 中间件一轮,
// 返回中间件对外透传的 (result, err);stub 入参 ctx 经 captured 传出,
// 供断言"next 的 ctx 含刚开的 child span"。
func driveSending(t *testing.T, tel *telemetry.Telemetry, ctx context.Context, stub mcp.MethodHandler, captured *context.Context) (mcp.Result, error) {
	t.Helper()
	req := &mcp.ClientRequest[*mcp.CallToolParams]{Params: &mcp.CallToolParams{Name: "add"}}
	wrapped := tel.SendingMiddleware("calc", "http")(stub)
	return wrapped(ctx, "tools/call", req)
}

// parentCtx 返回携带独立真值 span 上下文的 ctx:模拟"上游调用发生在
// 调用方的 span 之内"。真值来自独立 provider,断言只关心 id 一致性,
// 与被测 provider 无关(该真值 span 不进入被测录制器)。
func parentCtx(t *testing.T) (context.Context, trace.SpanContext) {
	t.Helper()
	sc := recordedSpanContext(t)
	return trace.ContextWithRemoteSpanContext(context.Background(), sc), sc
}

func TestSendingMiddleware(t *testing.T) {
	t.Run("开 child client span:kind=client、属性带 method/原始 tool 名/上游名/上游类型,next ctx 含该 span", func(t *testing.T) {
		tel, sr := newTelemetry(t)
		ctx, parent := parentCtx(t)

		var captured context.Context
		wantResult := &mcp.CallToolResult{}
		res, err := driveSending(t, tel, ctx, stubHandler(wantResult, nil, &captured), &captured)

		if err != nil || res != wantResult {
			t.Fatalf("中间件应原样透传 result/err,实际 res=%v err=%v", res, err)
		}
		span := endedSingle(t, sr)
		if span.SpanKind() != trace.SpanKindClient {
			t.Errorf("span kind 应为 client,实际 %v", span.SpanKind())
		}
		if span.Parent().SpanID() != parent.SpanID() {
			t.Errorf("client span 父应为调用方 span(%s),实际 %s",
				parent.SpanID(), span.Parent().SpanID())
		}
		if span.SpanContext().TraceID() != parent.TraceID() {
			t.Errorf("client span 应接续调用方 trace %s,实际 %s",
				parent.TraceID(), span.SpanContext().TraceID())
		}
		attrs := attrsOf(span)
		for key, want := range map[attribute.Key]string{
			"flamingos.method":        "tools/call",
			"gen_ai.tool.name":        "add",
			"flamingos.upstream.name": "calc",
			"flamingos.upstream.type": "http",
		} {
			if got := attrs[key]; got != want {
				t.Errorf("属性 %s = %q,期望 %q", key, got, want)
			}
		}
		got := trace.SpanContextFromContext(captured)
		if !got.IsValid() || got.SpanID() != span.SpanContext().SpanID() {
			t.Errorf("next 的 ctx 应含刚开的 client span(%s),实际 %s",
				span.SpanContext().SpanID(), got.SpanID())
		}
	})

	t.Run("next 报错:span 标 Error 并记 exception 事件,err 原样上抛", func(t *testing.T) {
		tel, sr := newTelemetry(t)
		wantErr := errors.New("上游 \"calc\" 传输失败: 超时")
		var captured context.Context
		_, err := driveSending(t, tel, context.Background(), stubHandler(nil, wantErr, &captured), &captured)

		if !errors.Is(err, wantErr) {
			t.Fatalf("err 应原样上抛,实际 %v", err)
		}
		span := endedSingle(t, sr)
		if span.Status().Code != codes.Error {
			t.Errorf("出站传输失败应标 span Error,实际 %v", span.Status().Code)
		}
		if !hasEvent(span, "exception") {
			t.Errorf("应记 exception 事件,实际事件 %v", span.Events())
		}
	})

	t.Run("业务失败(IsError):仅记事件不改错误状态", func(t *testing.T) {
		tel, sr := newTelemetry(t)
		wantResult := &mcp.CallToolResult{IsError: true}
		var captured context.Context
		res, err := driveSending(t, tel, context.Background(), stubHandler(wantResult, nil, &captured), &captured)

		if err != nil || res != wantResult {
			t.Fatalf("IsError 应答应原样透传,实际 res=%v err=%v", res, err)
		}
		span := endedSingle(t, sr)
		if span.Status().Code == codes.Error {
			t.Error("业务失败不应改 span 错误状态(协议层是成功应答)")
		}
		if !hasEvent(span, "flamingos.tool.is_error") {
			t.Errorf("业务失败应记事件,实际事件 %v", span.Events())
		}
	})

	t.Run("nil 接线:出站装配件返回 nil,调用方据此跳过挂点", func(t *testing.T) {
		var tel *telemetry.Telemetry // 关闭态哨兵
		if tel.SendingMiddleware("calc", "http") != nil {
			t.Error("nil 接线的 SendingMiddleware 应返回 nil")
		}
	})
}

// driveSendingReq 以自定义 req 驱动 Sending 中间件一轮,捕获中间件传给
// next 的 req 对象本身——出站 `_meta` 注入写在该对象上,黑盒可观察。
func driveSendingReq(t *testing.T, tel *telemetry.Telemetry, req mcp.Request, captured *mcp.Request) (mcp.Result, error) {
	t.Helper()
	wrapped := tel.SendingMiddleware("calc", "stdio")(func(_ context.Context, _ string, r mcp.Request) (mcp.Result, error) {
		*captured = r
		return &mcp.CallToolResult{}, nil
	})
	return wrapped(context.Background(), "tools/call", req)
}

// metaOfReq 读 req params 的 `_meta`(仅测试观察用;params 无 _meta 时 nil)。
func metaOfReq(t *testing.T, req mcp.Request) map[string]any {
	t.Helper()
	p, ok := req.GetParams().(*mcp.CallToolParams)
	if !ok || p == nil {
		t.Fatalf("req params 应为 *CallToolParams,实际 %T", req.GetParams())
	}
	return p.GetMeta()
}

// TestSendingMetaInjection 是票 0004-02 的出站 `_meta` 注入断言:向全部
// 上游的出站请求 params 注入 `_meta.traceparent`——stdio 上游没有 HTTP
// 头,这是它唯一的 trace 通道;既有键保留、原 map 不被原地改写。
func TestSendingMetaInjection(t *testing.T) {
	t.Run("注入 _meta.traceparent:与刚开的 client span 同 trace 同 span id", func(t *testing.T) {
		tel, sr := newTelemetry(t)
		req := &mcp.ClientRequest[*mcp.CallToolParams]{Params: &mcp.CallToolParams{Name: "add"}}

		var captured mcp.Request
		if _, err := driveSendingReq(t, tel, req, &captured); err != nil {
			t.Fatalf("调用失败: %v", err)
		}
		span := endedSingle(t, sr)
		meta := metaOfReq(t, captured)
		got, ok := meta["traceparent"].(string)
		if !ok {
			t.Fatalf("注入的 _meta.traceparent 应为字符串,实际 %T: %v", meta["traceparent"], meta["traceparent"])
		}
		if want := traceparentOf(span.SpanContext()); got != want {
			t.Errorf("注入值应与 client span 一致:\n期望 %q\n实际 %q", want, got)
		}
	})

	t.Run("既有键保留、副本修改:原 _map 不被原地改写,Agent 自带键原样带出", func(t *testing.T) {
		tel, _ := newTelemetry(t)
		orig := mcp.Meta{"progressToken": "token-9"}
		req := &mcp.ClientRequest[*mcp.CallToolParams]{
			Params: &mcp.CallToolParams{Name: "add", Meta: orig},
		}

		var captured mcp.Request
		if _, err := driveSendingReq(t, tel, req, &captured); err != nil {
			t.Fatalf("调用失败: %v", err)
		}
		if _, exists := orig["traceparent"]; exists {
			t.Error("注入不应写进原 map(原地改写),实际原 map 已含 traceparent")
		}
		meta := metaOfReq(t, captured)
		if meta["progressToken"] != "token-9" {
			t.Errorf("既有 _meta 键应原样带出,实际 %v", meta)
		}
		if _, ok := meta["traceparent"].(string); !ok {
			t.Errorf("应注入 traceparent,实际 %v", meta)
		}
	})

	t.Run("params 为 nil(缺 params 的请求):不注入、不 panic,span 照常", func(t *testing.T) {
		tel, sr := newTelemetry(t)
		req := &mcp.ClientRequest[*mcp.CallToolParams]{} // Params 保持零值(typed nil)

		var captured mcp.Request
		if _, err := driveSendingReq(t, tel, req, &captured); err != nil {
			t.Fatalf("调用失败: %v", err)
		}
		if ended := sr.Ended(); len(ended) != 1 {
			t.Fatalf("应恰有 1 个已结束 span,实际 %d 个", len(ended))
		}
	})
}

func TestRoundTripper(t *testing.T) {
	t.Run("注入出站头:base 收到的请求带与 ctx 当前 span 同 trace 的 traceparent,响应原样透传", func(t *testing.T) {
		tel, _ := newTelemetry(t)
		ctx, span := parentCtx(t)

		var captured *http.Request
		wantResp := &http.Response{StatusCode: http.StatusAccepted}
		base := captureTransport{capture: &captured, resp: wantResp}
		rt := tel.RoundTripper(base)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://上游/mcp", nil)
		if err != nil {
			t.Fatalf("构造请求失败: %v", err)
		}
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip 失败: %v", err)
		}
		if resp != wantResp {
			t.Errorf("响应应原样透传,实际 %v", resp)
		}
		gotHeader := captured.Header.Get("traceparent")
		parts := strings.Split(gotHeader, "-")
		if len(parts) != 4 {
			t.Fatalf("base 收到的 traceparent 应为四段 W3C 形状,实际 %q", gotHeader)
		}
		if parts[1] != span.TraceID().String() || parts[2] != span.SpanID().String() {
			t.Errorf("注入头的 trace/span 应与 ctx 当前 span 一致:\n头 %q\nspan trace=%s span=%s",
				gotHeader, span.TraceID(), span.SpanID())
		}
	})

	t.Run("ctx 无 span:不写头,请求照常送达", func(t *testing.T) {
		tel, _ := newTelemetry(t)
		var captured *http.Request
		base := captureTransport{capture: &captured, resp: &http.Response{StatusCode: http.StatusOK}}
		rt := tel.RoundTripper(base)

		req, err := http.NewRequest(http.MethodPost, "http://上游/mcp", nil)
		if err != nil {
			t.Fatalf("构造请求失败: %v", err)
		}
		if _, err := rt.RoundTrip(req); err != nil {
			t.Fatalf("RoundTrip 失败: %v", err)
		}
		if got := captured.Header.Get("traceparent"); got != "" {
			t.Errorf("无 span 时不应写 traceparent,实际 %q", got)
		}
	})

	t.Run("base 缺省:回落 DefaultTransport 完成本地往返", func(t *testing.T) {
		tel, _ := newTelemetry(t)
		var gotHeader string
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotHeader = r.Header.Get("traceparent")
			w.WriteHeader(http.StatusOK)
		}))
		defer ts.Close()

		rt := tel.RoundTripper(nil) // base 缺省 → http.DefaultTransport
		ctx, span := parentCtx(t)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL, nil)
		if err != nil {
			t.Fatalf("构造请求失败: %v", err)
		}
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip 失败: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("状态码 %d,期望 200", resp.StatusCode)
		}
		if parts := strings.Split(gotHeader, "-"); len(parts) != 4 ||
			parts[1] != span.TraceID().String() || parts[2] != span.SpanID().String() {
			t.Errorf("服务端应收到的 traceparent 与 client span 一致(trace=%s span=%s),实际 %q",
				span.TraceID(), span.SpanID(), gotHeader)
		}
	})

	t.Run("nil 接线:返回 nil,装配处据此不注入 HTTPClient", func(t *testing.T) {
		var tel *telemetry.Telemetry // 关闭态哨兵
		if tel.RoundTripper(nil) != nil {
			t.Error("nil 接线的 RoundTripper 应返回 nil")
		}
	})
}

// captureTransport 捕获途经请求并返回预置响应的桩 RoundTripper。
type captureTransport struct {
	capture **http.Request
	resp    *http.Response
}

func (c captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	*c.capture = req.Clone(req.Context())
	return c.resp, nil
}
