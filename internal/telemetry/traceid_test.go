// trace_id 只读访问器测试(spec 0008 票 01):录制器经 cmd 装配闭包注入本
// 访问器,录制条目冗余 trace_id 与 Jaeger 对查——ctx 有 recording span 时
// 取其 TraceID(hex 形态,与 span.SpanContext() 同值交叉核对),关闭态/
// 无效 span 返回空串不报错(只读观察,不新开 span)。
package telemetry_test

import (
	"context"
	"testing"

	"github.com/magician336/flamingos/internal/telemetry"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func TestTraceIDFromContext(t *testing.T) {
	t.Run("ctx 有 recording span:取其 TraceID,与 SpanContext 同值", func(t *testing.T) {
		sr := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
		t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

		tracer := tp.Tracer("traceid-test")
		ctx, span := tracer.Start(context.Background(), "tools/call")
		defer span.End()

		got := telemetry.TraceIDFromContext(ctx)
		want := span.SpanContext().TraceID().String()
		if got != want {
			t.Errorf("TraceIDFromContext = %q,期望当前 span 的 %q", got, want)
		}
		if len(got) != 32 {
			t.Errorf("trace_id 应为 32 字符 hex(W3C traceparent 同款形态),实际 %q", got)
		}
	})

	t.Run("ctx 无 span(可观测关闭态):返回空串不报错", func(t *testing.T) {
		if got := telemetry.TraceIDFromContext(context.Background()); got != "" {
			t.Errorf("无 span 应返回空串,实际 %q", got)
		}
	})

	t.Run("ctx 只有无效 span 上下文:返回空串", func(t *testing.T) {
		ctx := trace.ContextWithSpanContext(context.Background(), trace.SpanContext{})
		if got := telemetry.TraceIDFromContext(ctx); got != "" {
			t.Errorf("无效 span 上下文应返回空串,实际 %q", got)
		}
	})
}
