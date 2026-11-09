// telemetry 包测试:传播纯函数表驱动(spec 0004 Testing Decisions——
// 格式边界不靠 e2e 兜底,在此直测)。真值来源独立:合法用例的 traceparent
// 由真实录制 span 生成,断言不以被测代码的算法重算期望值。
// HTTP 头通道(票 0004-01)与 SEP-414 `_meta` 通道(票 0004-02)在此
// 同构直测:_meta 的 map[string]any 经薄适配接入同一官方 propagator,
// 非法/非字符串值的容错语义由 propagator 保证、由本表驱动锁定。
// 本文件用包内测试——四个传播纯函数只被同包 inbound/outbound 接线
// 消费,包外无生产调用方,不因测试需要而公开(与 otlp_internal_test.go
// 对 parseOTLPEndpoint 的处理同一原则,_internal_test 后缀照其先例);
// 外部测试包共享的真值构造器留在 propagation_test.go,两文件分工互见
// 对方头注。
package telemetry

import (
	"context"
	"net/http"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// headerWith 以 Set 语义构造头(键按 net/http 规则规范化),对应真实
// 网络路径下入站头的键形态。
func headerWith(key, value string) http.Header {
	h := http.Header{}
	h.Set(key, value)
	return h
}

// recordedSpanContext 在录制 provider 上开一个立即结束的 span,返回其
// SpanContext——提取/注入断言的独立真值来源(不经被测函数)。
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

func TestExtractFromHeaders(t *testing.T) {
	// 合法用例的头由真实 span 上下文拼出,值本身即 W3C 格式样例。
	// 头一律经 Set/Add 构造:真实网络路径下入站头由 net/http 规范化键,
	// map 字面量的小写键不经规范化,无法代表线路形状。
	want := recordedSpanContext(t)
	valid := "00-" + want.TraceID().String() + "-" + want.SpanID().String() + "-01"
	multi := headerWith("traceparent", "garbage")
	multi.Add("traceparent", valid)

	tests := []struct {
		name          string
		header        http.Header
		wantExtracted bool
	}{
		{
			name:          "合法 traceparent:提取出同 trace/span 的远程父上下文",
			header:        headerWith("traceparent", valid),
			wantExtracted: true,
		},
		{
			name:          "头缺失:不提取任何上下文",
			header:        http.Header{},
			wantExtracted: false,
		},
		{
			name:          "值非 traceparent 形状:官方 propagator 容错忽略",
			header:        headerWith("traceparent", "not-a-traceparent"),
			wantExtracted: false,
		},
		{
			name:          "trace id 段非法十六进制:忽略",
			header:        headerWith("traceparent", "00-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz-"+want.SpanID().String()+"-01"),
			wantExtracted: false,
		},
		{
			name:          "版本段非法:忽略",
			header:        headerWith("traceparent", "xx-"+want.TraceID().String()+"-"+want.SpanID().String()+"-01"),
			wantExtracted: false,
		},
		{
			name:          "多值头:propagator 只认第一个有效值,此处首值非法即整体忽略",
			header:        multi,
			wantExtracted: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := extractFromHeaders(context.Background(), tt.header)
			got := trace.SpanContextFromContext(ctx)
			if tt.wantExtracted {
				if !got.IsValid() {
					t.Fatalf("应提取出有效 span 上下文,实际无")
				}
				if got.TraceID() != want.TraceID() || got.SpanID() != want.SpanID() {
					t.Errorf("提取出的 trace/span 应与注入侧一致:\n期望 trace=%s span=%s\n实际 trace=%s span=%s",
						want.TraceID(), want.SpanID(), got.TraceID(), got.SpanID())
				}
				if !got.IsRemote() {
					t.Error("提取出的应为远程父上下文(IsRemote=true)")
				}
			} else if got.IsValid() {
				t.Errorf("不应提取出 span 上下文,实际 trace=%s span=%s", got.TraceID(), got.SpanID())
			}
		})
	}
}

func TestInjectToHeaders(t *testing.T) {
	t.Run("ctx 有 span:头收到与该 span 同 trace 同 span id 的 traceparent", func(t *testing.T) {
		sr := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
		defer func() { _ = tp.Shutdown(context.Background()) }()

		ctx, span := tp.Tracer("test").Start(context.Background(), "出站 span")
		defer span.End()
		header := http.Header{}
		injectToHeaders(ctx, header)

		got := header.Get("traceparent")
		if got == "" {
			t.Fatal("应写入 traceparent 头,实际缺失")
		}
		parts := strings.Split(got, "-")
		if len(parts) != 4 {
			t.Fatalf("traceparent 应为四段 W3C 形状,实际 %q", got)
		}
		want := span.SpanContext()
		if parts[1] != want.TraceID().String() || parts[2] != want.SpanID().String() {
			t.Errorf("注入的 trace/span 应与当前 span 一致:\n头 %q\nspan trace=%s span=%s",
				got, want.TraceID(), want.SpanID())
		}
	})

	t.Run("ctx 无 span:不写任何传播头", func(t *testing.T) {
		header := http.Header{"Authorization": []string{"Bearer 既有头不该被动到"}}
		injectToHeaders(context.Background(), header)

		if got := header.Get("traceparent"); got != "" {
			t.Errorf("无 span 时不应写 traceparent,实际 %q", got)
		}
		if got := header.Get("Authorization"); got != "Bearer 既有头不该被动到" {
			t.Errorf("注入不应触碰既有头,实际 Authorization=%q", got)
		}
	})
}

// traceparentOf 拼出指定 span 上下文的 W3C traceparent 字面量——期望值的
// 独立出处(与被测注入路径无关;合法用例的 span 上下文由真实录制 span 生成)。
func traceparentOf(sc trace.SpanContext) string {
	return "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-01"
}

func TestExtractFromMeta(t *testing.T) {
	// 独立真值:两个不同 trace 的 span 上下文,分别扮演 _meta 携带的
	// 上下文与 HTTP 头携带的上下文(双通道并存用例)。
	fromMeta := recordedSpanContext(t)
	fromHeader := recordedSpanContext(t)

	tests := []struct {
		name        string
		meta        map[string]any
		parentCtx   context.Context // 入口 ctx 已携带的上下文(HTTP 头通道提取结果)
		wantTraceID trace.TraceID   // 期望提取后的远程父;零值 = 期望无上下文
	}{
		{
			name:        "合法 traceparent:提取出同 trace/span 的远程父上下文",
			meta:        map[string]any{"traceparent": traceparentOf(fromMeta)},
			wantTraceID: fromMeta.TraceID(),
		},
		{
			name: "_meta 缺失(nil):不提取任何上下文",
			meta: nil,
		},
		{
			name: "_meta 无 traceparent 键:不提取",
			meta: map[string]any{"progressToken": 1},
		},
		{
			name: "值非法:官方 propagator 容错忽略,不炸",
			meta: map[string]any{"traceparent": "not-a-traceparent"},
		},
		{
			name: "值非字符串:按缺失处理,不炸",
			meta: map[string]any{"traceparent": 12345},
		},
		{
			name: "trace id 段非法十六进制:忽略",
			meta: map[string]any{"traceparent": "00-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz-" + fromMeta.SpanID().String() + "-01"},
		},
		{
			name:        "双通道并存:_meta 有效则覆盖 ctx 已有(HTTP 头)的上下文",
			meta:        map[string]any{"traceparent": traceparentOf(fromMeta)},
			parentCtx:   trace.ContextWithRemoteSpanContext(context.Background(), fromHeader),
			wantTraceID: fromMeta.TraceID(),
		},
		{
			name:        "双通道并存:_meta 非法则回退 ctx 已有(HTTP 头)的上下文",
			meta:        map[string]any{"traceparent": "garbage"},
			parentCtx:   trace.ContextWithRemoteSpanContext(context.Background(), fromHeader),
			wantTraceID: fromHeader.TraceID(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := tt.parentCtx
			if parent == nil {
				parent = context.Background() // 表字段零值是 nil ctx,此处兜底
			}
			ctx := extractFromMeta(parent, tt.meta)
			got := trace.SpanContextFromContext(ctx)
			if tt.wantTraceID.IsValid() {
				if !got.IsValid() {
					t.Fatalf("应提取出有效 span 上下文,实际无")
				}
				if got.TraceID() != tt.wantTraceID {
					t.Errorf("提取出的 trace 应为 %s,实际 %s", tt.wantTraceID, got.TraceID())
				}
				if !got.IsRemote() {
					t.Error("提取出的应为远程父上下文(IsRemote=true)")
				}
			} else if got.IsValid() {
				t.Errorf("不应提取出 span 上下文,实际 trace=%s span=%s", got.TraceID(), got.SpanID())
			}
		})
	}
}

func TestInjectToMeta(t *testing.T) {
	t.Run("ctx 有 span:返回的 map 含与该 span 同 trace 同 span id 的 traceparent", func(t *testing.T) {
		sr := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
		defer func() { _ = tp.Shutdown(context.Background()) }()
		ctx, span := tp.Tracer("test").Start(context.Background(), "出站 span")
		defer span.End()

		got := injectToMeta(ctx, nil)
		value, ok := got["traceparent"].(string)
		if !ok {
			t.Fatalf("应写入字符串 traceparent,实际 %T: %v", got["traceparent"], got["traceparent"])
		}
		if value != traceparentOf(span.SpanContext()) {
			t.Errorf("注入的 traceparent 应与当前 span 一致:\n期望 %q\n实际 %q",
				traceparentOf(span.SpanContext()), value)
		}
	})

	t.Run("既有键保留:Agent 自带的其他 _meta 键原样带出", func(t *testing.T) {
		ctx, span := recordingSpan(t)
		defer span.End()

		orig := map[string]any{"progressToken": "token-7", "自定义键": []any{1, 2}}
		got := injectToMeta(ctx, orig)

		if got["progressToken"] != "token-7" || got["自定义键"] == nil {
			t.Errorf("既有 _meta 键应原样保留,实际 %v", got)
		}
		if _, ok := got["traceparent"]; !ok {
			t.Error("应注入 traceparent 键")
		}
	})

	t.Run("副本修改:原 map 不被原地改写(不可变纪律)", func(t *testing.T) {
		ctx, span := recordingSpan(t)
		defer span.End()

		orig := map[string]any{"progressToken": "token-7"}
		got := injectToMeta(ctx, orig)

		if _, exists := orig["traceparent"]; exists {
			t.Error("注入不应写进原 map(原地改写),实际原 map 已含 traceparent")
		}
		if len(orig) != 1 {
			t.Errorf("原 map 长度应保持 1,实际 %d: %v", len(orig), orig)
		}
		if got == nil || len(got) != 2 {
			t.Errorf("返回的副本应含既有键与 traceparent 共 2 键,实际 %v", got)
		}
	})

	t.Run("ctx 无 span:不写任何键", func(t *testing.T) {
		got := injectToMeta(context.Background(), map[string]any{"progressToken": 1})
		if _, exists := got["traceparent"]; exists {
			t.Errorf("无 span 时不应写 traceparent,实际 %v", got)
		}
		if got["progressToken"] != 1 {
			t.Errorf("既有键应保留,实际 %v", got)
		}
	})
}

// recordingSpan 在独立 provider 上开一个进行中的 span,返回其 ctx 与
// span 本体(供注入断言对照;provider 关闭挂 t.Cleanup)。
func recordingSpan(t *testing.T) (context.Context, trace.Span) {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return tp.Tracer("test").Start(context.Background(), "注入源 span")
}
