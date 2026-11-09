// telemetry 入站接线测试:HTTP 头提取包装与网关请求 span 的 Receiving
// 中间件。span 语义(属性集、错误标注、事件、根/接续)经 SpanRecorder
// 断言(spec 0004 Testing Decisions);请求对象用 SDK 导出的
// ServerRequest 字面构造,不伪造 SDK 内部状态。
package telemetry_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/magician336/flamingos/internal/telemetry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// newTelemetry 构造带独立录制 provider 的接线与录制器;provider 关闭挂
// t.Cleanup,保证在途 span 落盘后再断言。
func newTelemetry(t *testing.T) (*telemetry.Telemetry, *tracetest.SpanRecorder) {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return telemetry.New(tp), sr
}

// driveReceiving 以桩 next 与手造 ServerRequest 驱动 Receiving 中间件一轮,
// 返回中间件对外透传的 (result, err) 供"不吞不改"断言。
func driveReceiving(t *testing.T, tel *telemetry.Telemetry, ctx context.Context, method string, req mcp.Request, stub mcp.MethodHandler) (mcp.Result, error) {
	t.Helper()
	wrapped := tel.ReceivingMiddleware()(stub)
	return wrapped(ctx, method, req)
}

// stubHandler 返回按预置值应答并捕获入参 ctx 的桩 next;captured 为 nil
// 时不捕获。
func stubHandler(result mcp.Result, err error, captured *context.Context) mcp.MethodHandler {
	return func(ctx context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
		if captured != nil {
			*captured = ctx
		}
		return result, err
	}
}

// attrsOf 把 span 属性集索引为 map,断言与顺序无关。
func attrsOf(span sdktrace.ReadOnlySpan) map[attribute.Key]string {
	m := make(map[attribute.Key]string, len(span.Attributes()))
	for _, kv := range span.Attributes() {
		m[kv.Key] = kv.Value.AsString()
	}
	return m
}

// hasEvent 判断 span 是否携带指定名字的事件。
func hasEvent(span sdktrace.ReadOnlySpan, name string) bool {
	for _, e := range span.Events() {
		if e.Name == name {
			return true
		}
	}
	return false
}

// endedSingle 断言录制器恰有一个已结束 span 并返回它。
func endedSingle(t *testing.T, sr *tracetest.SpanRecorder) sdktrace.ReadOnlySpan {
	t.Helper()
	ended := sr.Ended()
	if len(ended) != 1 {
		t.Fatalf("应恰有 1 个已结束 span,实际 %d 个", len(ended))
	}
	return ended[0]
}

func TestNew(t *testing.T) {
	t.Run("tp 为 nil:返回 nil,即关闭态哨兵(装配处不装任何挂点)", func(t *testing.T) {
		if got := telemetry.New(nil); got != nil {
			t.Errorf("New(nil) 应返回 nil,实际 %v", got)
		}
	})

	t.Run("nil 接线:入站装配件返回 nil,调用方据此跳过挂点", func(t *testing.T) {
		var tel *telemetry.Telemetry // 关闭态哨兵
		if tel.HTTPMiddleware() != nil {
			t.Error("nil 接线的 HTTPMiddleware 应返回 nil")
		}
		if tel.ReceivingMiddleware() != nil {
			t.Error("nil 接线的 ReceivingMiddleware 应返回 nil")
		}
	})
}

func TestHTTPMiddleware(t *testing.T) {
	t.Run("提取入站 traceparent:下游 handler 的请求 ctx 含同 trace 的远程父", func(t *testing.T) {
		tel, _ := newTelemetry(t)
		want := recordedSpanContext(t)
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("traceparent", "00-"+want.TraceID().String()+"-"+want.SpanID().String()+"-01")

		var captured context.Context
		wrapped := tel.HTTPMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			captured = r.Context()
		}))
		wrapped.ServeHTTP(httptest.NewRecorder(), req)

		got := trace.SpanContextFromContext(captured)
		if !got.IsValid() || got.TraceID() != want.TraceID() || got.SpanID() != want.SpanID() {
			t.Errorf("下游 ctx 应含远程父(trace=%s span=%s),实际 trace=%s span=%s valid=%v",
				want.TraceID(), want.SpanID(), got.TraceID(), got.SpanID(), got.IsValid())
		}
		if !got.IsRemote() {
			t.Error("提取出的 span 上下文应为远程父(IsRemote=true)")
		}
	})

	t.Run("无头照常:ctx 不含 span 上下文,请求原样送达下游", func(t *testing.T) {
		tel, _ := newTelemetry(t)

		var captured context.Context
		rec := httptest.NewRecorder()
		wrapped := tel.HTTPMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			captured = r.Context()
			w.WriteHeader(http.StatusTeapot) // 独立字面量:证明响应原样透传
		}))
		wrapped.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))

		if got := trace.SpanContextFromContext(captured); got.IsValid() {
			t.Errorf("无 traceparent 头时 ctx 不应含 span 上下文,实际 %s", got.TraceID())
		}
		if rec.Code != http.StatusTeapot {
			t.Errorf("包装应原样透传下游响应,状态码 %d,期望 %d", rec.Code, http.StatusTeapot)
		}
	})
}

func TestReceivingMiddleware(t *testing.T) {
	toolsCallReq := func(name string) mcp.Request {
		return &mcp.ServerRequest[*mcp.CallToolParams]{Params: &mcp.CallToolParams{Name: name}}
	}

	t.Run("开网关请求 span:span 名为 method、kind=server、属性带 method/聚合 tool 名/命名空间,结果不吞不改", func(t *testing.T) {
		tel, sr := newTelemetry(t)
		wantResult := &mcp.CallToolResult{}
		var captured context.Context
		res, err := driveReceiving(t, tel, context.Background(), "tools/call",
			toolsCallReq("calc_add"), stubHandler(wantResult, nil, &captured))

		if err != nil || res != wantResult {
			t.Fatalf("中间件应原样透传 result/err,实际 res=%v err=%v", res, err)
		}
		span := endedSingle(t, sr)
		if span.Name() != "tools/call" {
			t.Errorf("span 名应为 method,实际 %q", span.Name())
		}
		if span.SpanKind() != trace.SpanKindServer {
			t.Errorf("span kind 应为 server,实际 %v", span.SpanKind())
		}
		attrs := attrsOf(span)
		for key, want := range map[attribute.Key]string{
			"flamingos.method":    "tools/call",
			"gen_ai.tool.name":    "calc_add",
			"flamingos.namespace": "calc",
		} {
			if got := attrs[key]; got != want {
				t.Errorf("属性 %s = %q,期望 %q", key, got, want)
			}
		}
	})

	t.Run("span ctx 传给下游:next 收到的 ctx 含刚开的 span", func(t *testing.T) {
		tel, sr := newTelemetry(t)
		var captured context.Context
		_, _ = driveReceiving(t, tel, context.Background(), "tools/call",
			toolsCallReq("calc_add"), stubHandler(&mcp.CallToolResult{}, nil, &captured))

		span := endedSingle(t, sr)
		got := trace.SpanContextFromContext(captured)
		if !got.IsValid() || got.SpanID() != span.SpanContext().SpanID() {
			t.Errorf("next 的 ctx 应含网关请求 span(%s),实际 %s", span.SpanContext().SpanID(), got.SpanID())
		}
	})

	t.Run("无上下文自开根:入站无 traceparent 时 span 无父", func(t *testing.T) {
		tel, sr := newTelemetry(t)
		_, _ = driveReceiving(t, tel, context.Background(), "tools/call",
			toolsCallReq("calc_add"), stubHandler(&mcp.CallToolResult{}, nil, nil))

		span := endedSingle(t, sr)
		if span.Parent().IsValid() {
			t.Errorf("无上下文时网关应自开根 span,实际父 trace=%s span=%s",
				span.Parent().TraceID(), span.Parent().SpanID())
		}
	})

	t.Run("接续远程父:ctx 已含提取出的远程父时 span 挂为 child 且同 trace", func(t *testing.T) {
		tel, sr := newTelemetry(t)
		parent := recordedSpanContext(t)
		ctx := trace.ContextWithRemoteSpanContext(context.Background(), parent)

		_, _ = driveReceiving(t, tel, ctx, "tools/call",
			toolsCallReq("calc_add"), stubHandler(&mcp.CallToolResult{}, nil, nil))

		span := endedSingle(t, sr)
		if span.Parent().SpanID() != parent.SpanID() {
			t.Errorf("span 父应为 Agent 注入的 %s,实际 %s", parent.SpanID(), span.Parent().SpanID())
		}
		if span.SpanContext().TraceID() != parent.TraceID() {
			t.Errorf("span 应接续 Agent 的 trace %s,实际 %s", parent.TraceID(), span.SpanContext().TraceID())
		}
	})

	t.Run("上游不可用(next 报错):span 标 Error 并记 exception 事件,err 原样上抛", func(t *testing.T) {
		tel, sr := newTelemetry(t)
		wantErr := errors.New("上游 \"calc\" 不可用: 连接拒绝")
		var captured context.Context
		_, err := driveReceiving(t, tel, context.Background(), "tools/call",
			toolsCallReq("calc_add"), stubHandler(nil, wantErr, &captured))

		if !errors.Is(err, wantErr) {
			t.Fatalf("err 应原样上抛,实际 %v", err)
		}
		span := endedSingle(t, sr)
		if span.Status().Code != codes.Error {
			t.Errorf("上游不可用应标 span Error,实际 %v", span.Status().Code)
		}
		if !hasEvent(span, "exception") {
			t.Errorf("应记 exception 事件,实际事件 %v", span.Events())
		}
	})

	t.Run("业务失败(IsError):仅记事件不改错误状态", func(t *testing.T) {
		tel, sr := newTelemetry(t)
		wantResult := &mcp.CallToolResult{IsError: true}

		res, err := driveReceiving(t, tel, context.Background(), "tools/call",
			toolsCallReq("calc_fail"), stubHandler(wantResult, nil, nil))

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

	t.Run("非 tools/call 方法同样覆盖:tools/list 有 method 属性、无 tool 属性", func(t *testing.T) {
		tel, sr := newTelemetry(t)
		_, _ = driveReceiving(t, tel, context.Background(), "tools/list",
			&mcp.ServerRequest[*mcp.ListToolsParams]{Params: &mcp.ListToolsParams{}},
			stubHandler(&mcp.ListToolsResult{}, nil, nil))

		span := endedSingle(t, sr)
		if span.Name() != "tools/list" {
			t.Errorf("span 名应为 method,实际 %q", span.Name())
		}
		attrs := attrsOf(span)
		if got := attrs["flamingos.method"]; got != "tools/list" {
			t.Errorf("属性 flamingos.method = %q,期望 tools/list", got)
		}
		for _, key := range []attribute.Key{"gen_ai.tool.name", "flamingos.namespace"} {
			if _, ok := attrs[key]; ok {
				t.Errorf("tools/list 不应带 %s 属性,实际 %v", key, attrs)
			}
		}
	})
}

// TestReceivingMetaExtraction 是票 0004-02 的入站 `_meta` 提取断言:
// params `_meta.traceparent`(SEP-414 协议原生载体)是优先通道,与 HTTP
// 头通道并存时以其为准,非法则回退头;请求 params 原样透传不吞不改。
func TestReceivingMetaExtraction(t *testing.T) {
	toolsCallReqWithMeta := func(name string, meta mcp.Meta) mcp.Request {
		return &mcp.ServerRequest[*mcp.CallToolParams]{
			Params: &mcp.CallToolParams{Name: name, Meta: meta},
		}
	}
	fromMeta := recordedSpanContext(t)   // _meta 携带的上下文
	fromHeader := recordedSpanContext(t) // HTTP 头提取的上下文(另一条 trace)

	t.Run("_meta 携带上下文:span 接续其 trace 而非新根", func(t *testing.T) {
		tel, sr := newTelemetry(t)
		req := toolsCallReqWithMeta("calc_add", mcp.Meta{"traceparent": traceparentOf(fromMeta)})

		_, _ = driveReceiving(t, tel, context.Background(), "tools/call",
			req, stubHandler(&mcp.CallToolResult{}, nil, nil))

		span := endedSingle(t, sr)
		if span.Parent().SpanID() != fromMeta.SpanID() {
			t.Errorf("span 父应为 _meta 携带的 %s,实际 %s", fromMeta.SpanID(), span.Parent().SpanID())
		}
		if span.SpanContext().TraceID() != fromMeta.TraceID() {
			t.Errorf("span 应接续 _meta 携带的 trace %s,实际 %s", fromMeta.TraceID(), span.SpanContext().TraceID())
		}
	})

	t.Run("双通道并存 _meta 优先:ctx 已含头提取的父、_meta 另携上下文时,span 接续 _meta 的", func(t *testing.T) {
		tel, sr := newTelemetry(t)
		ctx := trace.ContextWithRemoteSpanContext(context.Background(), fromHeader)
		req := toolsCallReqWithMeta("calc_add", mcp.Meta{"traceparent": traceparentOf(fromMeta)})

		_, _ = driveReceiving(t, tel, ctx, "tools/call",
			req, stubHandler(&mcp.CallToolResult{}, nil, nil))

		span := endedSingle(t, sr)
		if span.Parent().SpanID() != fromMeta.SpanID() {
			t.Errorf("_meta 应优先于 HTTP 头:span 父应为 _meta 的 %s,实际 %s(头的 %s)",
				fromMeta.SpanID(), span.Parent().SpanID(), fromHeader.SpanID())
		}
	})

	t.Run("_meta 非法回退头:_meta 值非字符串时 span 接续头提取的父", func(t *testing.T) {
		tel, sr := newTelemetry(t)
		ctx := trace.ContextWithRemoteSpanContext(context.Background(), fromHeader)
		req := toolsCallReqWithMeta("calc_add", mcp.Meta{"traceparent": 12345, "progressToken": 1})

		_, _ = driveReceiving(t, tel, ctx, "tools/call",
			req, stubHandler(&mcp.CallToolResult{}, nil, nil))

		span := endedSingle(t, sr)
		if span.Parent().SpanID() != fromHeader.SpanID() {
			t.Errorf("非法 _meta 应回退 HTTP 头:span 父应为头的 %s,实际 %s",
				fromHeader.SpanID(), span.Parent().SpanID())
		}
	})

	t.Run("非 tools/call 方法同样提取:tools/list 的 _meta 上下文同样接续", func(t *testing.T) {
		tel, sr := newTelemetry(t)
		req := &mcp.ServerRequest[*mcp.ListToolsParams]{
			Params: &mcp.ListToolsParams{Meta: mcp.Meta{"traceparent": traceparentOf(fromMeta)}},
		}

		_, _ = driveReceiving(t, tel, context.Background(), "tools/list",
			req, stubHandler(&mcp.ListToolsResult{}, nil, nil))

		span := endedSingle(t, sr)
		if span.SpanContext().TraceID() != fromMeta.TraceID() {
			t.Errorf("tools/list 的 span 应接续 _meta 的 trace %s,实际 %s(接续不应只限 tools/call)",
				fromMeta.TraceID(), span.SpanContext().TraceID())
		}
	})

	t.Run("请求原样透传:params 的 _meta 不被提取路径改写", func(t *testing.T) {
		tel, _ := newTelemetry(t)
		meta := mcp.Meta{"traceparent": traceparentOf(fromMeta), "progressToken": 1}
		req := toolsCallReqWithMeta("calc_add", meta)

		_, _ = driveReceiving(t, tel, context.Background(), "tools/call",
			req, stubHandler(&mcp.CallToolResult{}, nil, nil))

		if len(meta) != 2 || meta["progressToken"] != 1 {
			t.Errorf("入站提取只读,params._meta 不应被改写,实际 %v", meta)
		}
		if got := meta["traceparent"]; got != traceparentOf(fromMeta) {
			t.Errorf("既有 traceparent 键应原样保留,实际 %v", got)
		}
	})
}
