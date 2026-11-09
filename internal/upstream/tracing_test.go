// upstream 的可观测出站接线测试(spec 0004 票 01):构造选项注入
// telemetry 后,该上游的出站调用产生 child client span(Sending 中间件,
// 上游名/类型经构造注入);不注入时关闭态零 span、既有行为不变。
// HTTP 与 stdio 两个 adapter 各验一遍(adapter 是选项的唯一消费者)。
package upstream

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/magician336/flamingos/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// newRecordingTelemetry 构造带独立录制 provider 的可观测接线;provider
// 关闭挂 t.Cleanup,保证在途 span 落盘后再断言。
func newRecordingTelemetry(t *testing.T) (*telemetry.Telemetry, *tracetest.SpanRecorder) {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return telemetry.New(tp), sr
}

// clientSpanByName 在录制器中按名字找唯一 client span,找不到即失败。
func clientSpanByName(t *testing.T, sr *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	var found []sdktrace.ReadOnlySpan
	for _, span := range sr.Ended() {
		if span.Name() == name && span.SpanKind() == trace.SpanKindClient {
			found = append(found, span)
		}
	}
	if len(found) != 1 {
		t.Fatalf("应恰有 1 个名为 %q 的 client span,实际 %d 个(全部:%d 个 span)",
			name, len(found), len(sr.Ended()))
	}
	return found[0]
}

// attrsOf 把 span 属性集索引为 map,断言与顺序无关。
func attrsOf(span sdktrace.ReadOnlySpan) map[string]string {
	m := make(map[string]string, len(span.Attributes()))
	for _, kv := range span.Attributes() {
		m[string(kv.Key)] = kv.Value.AsString()
	}
	return m
}

// spanNamesOf 列出录制器全部已结束 span 的名字,供失败信息展示。
func spanNamesOf(sr *tracetest.SpanRecorder) []string {
	names := make([]string, 0, len(sr.Ended()))
	for _, span := range sr.Ended() {
		names = append(names, span.Name())
	}
	return names
}

func TestHTTPOutboundTracing(t *testing.T) {
	t.Run("开启选项:调用产生带上游名/类型/tool 名的 client span,握手期方法同样覆盖", func(t *testing.T) {
		url := newFakeUpstream(t)
		tel, sr := newRecordingTelemetry(t)
		client, err := NewHTTP(context.Background(), "calc", url, testTimeout, WithTelemetry(tel))
		if err != nil {
			t.Fatalf("构造客户端失败: %v", err)
		}
		defer func() { _ = client.Close() }()

		if _, err := client.CallTool(context.Background(), "echo", json.RawMessage(`{"msg":"你好"}`)); err != nil {
			t.Fatalf("调用应照常成功,实际报错: %v", err)
		}

		span := clientSpanByName(t, sr, "tools/call")
		attrs := attrsOf(span)
		for key, want := range map[string]string{
			"flamingos.method":        "tools/call",
			"gen_ai.tool.name":        "echo",
			"flamingos.upstream.name": "calc",
			"flamingos.upstream.type": "http",
		} {
			if got := attrs[key]; got != want {
				t.Errorf("属性 %s = %q,期望 %q", key, got, want)
			}
		}
		// Sending 中间件挂在整个会话上:构造时的握手同样被覆盖。握手方法名
		// 取决于 SDK 版本协商(HTTP adapter 未 pin 版本,v1.8.0 走 SEP-2575
		// 的 server/discover;stdio adapter pin 旧版走 initialize),两者
		// 皆可,存在其一即为覆盖证据。
		handshakeCovered := false
		for _, span := range sr.Ended() {
			if span.SpanKind() == trace.SpanKindClient &&
				(span.Name() == "server/discover" || span.Name() == "initialize") {
				handshakeCovered = true
			}
		}
		if !handshakeCovered {
			t.Errorf("握手期方法应产生 client span(server/discover 或 initialize),实际 %v", spanNamesOf(sr))
		}
	})

	t.Run("关闭态:不传选项时零 span 且往返照旧", func(t *testing.T) {
		url := newFakeUpstream(t)
		_, sr := newRecordingTelemetry(t) // provider 只用于观察"零 span"
		client, err := NewHTTP(context.Background(), "calc", url, testTimeout)
		if err != nil {
			t.Fatalf("构造客户端失败: %v", err)
		}
		defer func() { _ = client.Close() }()

		args := json.RawMessage(`{"msg":"你好,flamingos","n":1}`)
		res, err := client.CallTool(context.Background(), "echo", args)
		if err != nil {
			t.Fatalf("关闭态调用应照常成功,实际报错: %v", err)
		}
		if got := textOf(res); got != string(args) {
			t.Errorf("关闭态应字节级回显,实际 %q", got)
		}
		if got := len(sr.Ended()); got != 0 {
			t.Errorf("关闭态应零 span,实际 %d 个", got)
		}
	})
}

func TestStdioOutboundTracing(t *testing.T) {
	t.Run("开启选项:stdio 调用产生类型为 stdio 的 client span", func(t *testing.T) {
		tel, sr := newRecordingTelemetry(t)
		client, err := NewStdio(context.Background(), "fs", stdioServerBin, nil, testTimeout, WithTelemetry(tel))
		if err != nil {
			t.Fatalf("构造 stdio 客户端失败: %v", err)
		}
		defer func() { _ = client.Close() }()

		if _, err := client.CallTool(context.Background(), "echo", json.RawMessage(`{}`)); err != nil {
			t.Fatalf("调用应照常成功,实际报错: %v", err)
		}

		span := clientSpanByName(t, sr, "tools/call")
		attrs := attrsOf(span)
		for key, want := range map[string]string{
			"flamingos.upstream.name": "fs",
			"flamingos.upstream.type": "stdio",
		} {
			if got := attrs[key]; got != want {
				t.Errorf("属性 %s = %q,期望 %q", key, got, want)
			}
		}
	})

	t.Run("关闭态:不传选项时 stdio 零 span 且往返照旧", func(t *testing.T) {
		_, sr := newRecordingTelemetry(t)
		client, err := NewStdio(context.Background(), "fs", stdioServerBin, nil, testTimeout)
		if err != nil {
			t.Fatalf("构造 stdio 客户端失败: %v", err)
		}
		defer func() { _ = client.Close() }()

		if _, err := client.CallTool(context.Background(), "echo", json.RawMessage(`{"alive":true}`)); err != nil {
			t.Fatalf("关闭态调用应照常成功,实际报错: %v", err)
		}
		if got := len(sr.Ended()); got != 0 {
			t.Errorf("关闭态应零 span,实际 %d 个", got)
		}
	})
}
