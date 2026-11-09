// telemetry 缓存命中属性导出入口的测试(票 0006-03/spec 0006 Story 5):
// MarkCacheHit 向 ctx 当前 span 写命中属性,hit/miss 两态与关闭态经
// SpanRecorder 断言;cmd 装配层把 cache 的 OnHit 回调闭包接到本入口,
// 属性键的单一出处与既有属性键同站(telemetry.go)。
package telemetry_test

import (
	"context"
	"testing"

	"github.com/magician336/flamingos/internal/telemetry"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// attrOf 从已结束 span 的属性集中取指定键的值(缺失返回 found=false)。
func attrOf(span sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
	for _, kv := range span.Attributes() {
		if kv.Key == attribute.Key(key) {
			return kv.Value, true
		}
	}
	return attribute.Value{}, false
}

// newHitHarness 构造接线与录制器,并返回测试自持的 tracer(开被写 span
// 用;MarkCacheHit 只消费 ctx,与 tracer 的 scope 名无关)。provider 关闭
// 挂 t.Cleanup,保证在途 span 落盘后再断言。
func newHitHarness(t *testing.T) (*telemetry.Telemetry, *tracetest.SpanRecorder, trace.Tracer) {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return telemetry.New(tp), sr, tp.Tracer("cache-hit-test")
}

func TestMarkCacheHit(t *testing.T) {
	t.Run("hit 与 miss 两态都写 bool 属性:命中 true、未命中 false", func(t *testing.T) {
		// 定档:两态都写(属性存在性本身标记"该调用经过缓存层",只有
		// true 有属性会让 miss 与"没查缓存"在 Jaeger 上无法分辨)。
		tel, sr, tr := newHitHarness(t)

		for _, tc := range []struct {
			name string
			hit  bool
		}{
			{"命中", true},
			{"未命中", false},
		} {
			ctx, span := tr.Start(context.Background(), "tools/call", trace.WithSpanKind(trace.SpanKindServer))
			tel.MarkCacheHit(ctx, tc.hit)
			span.End()

			got := sr.Ended()[len(sr.Ended())-1]
			if got.Name() != "tools/call" {
				t.Fatalf("%s:录制器应收到刚结束的 span,实际 %q", tc.name, got.Name())
			}
			v, ok := attrOf(got, "flamingos.cache.hit")
			if !ok {
				t.Fatalf("%s:span 应带缓存命中属性,实际缺失", tc.name)
			}
			if v.AsBool() != tc.hit {
				t.Errorf("%s:属性值 = %v,期望 %v", tc.name, v.AsBool(), tc.hit)
			}
		}
	})

	t.Run("写向 ctx 当前 span:不新开 span、不额外结束 span", func(t *testing.T) {
		tel, sr, tr := newHitHarness(t)

		ctx, span := tr.Start(context.Background(), "tools/call", trace.WithSpanKind(trace.SpanKindServer))
		tel.MarkCacheHit(ctx, true)
		span.End()

		if got := len(sr.Ended()); got != 1 {
			t.Fatalf("MarkCacheHit 不应新开或提前结束 span,实际录制 %d 个", got)
		}
	})

	t.Run("关闭态:nil 接线安全返回(零行为)", func(t *testing.T) {
		var tel *telemetry.Telemetry // telemetry.New(nil) 的关闭态哨兵形态
		tel.MarkCacheHit(context.Background(), true)
	})
}
