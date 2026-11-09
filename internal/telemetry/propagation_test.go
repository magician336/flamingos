// telemetry 外部测试包的共享真值构造器:真实录制 span 的 SpanContext
// 与其 W3C traceparent 字面量。inbound/outbound 接线测试(本外部测试
// 包)据它们构造合法传播载荷与期望值——真值独立,不以被测代码的算法
// 重算。传播纯函数自身的表驱动测试在 propagation_internal_test.go
// (包内测试:四个函数已收窄为包内可见,不因测试需要而公开;两文件
// 分属 telemetry/telemetry_test 两个编译单元,同名 helper 各持一份、
// 逻辑独立于被测代码,是 internal/external 测试包并存的常规形态)。
package telemetry_test

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// recordedSpanContext 在录制 provider 上开一个立即结束的 span,返回其
// SpanContext——传播断言的独立真值来源(不经被测函数)。
func recordedSpanContext(t *testing.T) trace.SpanContext {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	defer func() { _ = tp.Shutdown(context.Background()) }()
	_, span := tp.Tracer("test").Start(context.Background(), "真值 span")
	got := span.SpanContext()
	span.End()
	return got
}

// traceparentOf 拼出指定 span 上下文的 W3C traceparent 字面量——期望值的
// 独立出处(与被测注入路径无关;合法用例的 span 上下文由真实录制 span 生成)。
func traceparentOf(sc trace.SpanContext) string {
	return "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-01"
}
