// telemetry 包的装配中心:TracerProvider 的三形态入口——注入形态(测试
// 经 New 直传)、关闭态(nil 哨兵)、OTLP 导出形态(otlp.go,票 0004-03)。
// span 属性键与事件名的单一出处也在此(spec 0004 Story 26:命名与键的
// 调整不散改)。
package telemetry

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// scopeName 是 telemetry 开全部 span 的 instrumentation scope 名,
// Jaeger 中据此识别 span 来源。
const scopeName = "github.com/magician336/flamingos"

// Provider 是装配层持有的 TracerProvider 形态:产出 Tracer 之外,还要
// 能在停机路径完成导出(flush)——BatchSpanProcessor 的 Shutdown 会先
// 把在途 span 全部导出再关闭。OTLP 形态(NewOTLPTracerProvider)与
// 测试注入形态(*sdktrace.TracerProvider,含 SpanRecorder)天然满足;
// 关闭态不持 provider,装配处持 nil(spec 0004 Story 23:三形态单一构造处)。
type Provider interface {
	trace.TracerProvider
	Shutdown(ctx context.Context) error
}

// Telemetry 持有 Tracer 并产出各处装配件(入站/出站中间件、RoundTripper)。
// 零值不可用:一律经 New 构造。nil *Telemetry 是关闭态哨兵——全部产件
// 方法对 nil 接收者返回 nil,装配处据"非 nil 才安装"跳过全部挂点,
// 关闭态零中间件、零包装(spec 0004 Story 13:零开销)。
type Telemetry struct {
	tracer trace.Tracer
}

// New 以 TracerProvider 构造接线:tp 为 nil 时返回 nil(关闭态哨兵,
// 装配处不装任何挂点);测试注入含 SpanRecorder 的 provider,OTLP
// provider 经 NewOTLPTracerProvider 产出——三形态收敛到这一个入口。
func New(tp trace.TracerProvider) *Telemetry {
	if tp == nil {
		return nil
	}
	return &Telemetry{tracer: tp.Tracer(scopeName)}
}

// startSpan 按 spec 0004 Story 26 的 span 命名规则开 span:span 名恒为
// JSON-RPC method 名——入站 server span(Receiving 中间件)与出站
// client span(Sending 中间件)共用此单一出处,命名规则调整不散改两处
// (与下方属性键/事件名的单一出处纪律一致)。span kind 与属性由调用方
// 给出;接收者不为 nil 设防——两个调用方都是产件方法返回的非 nil 接线
// 闭包。
func (t *Telemetry) startSpan(ctx context.Context, method string, kind trace.SpanKind, attrs ...attribute.KeyValue) (context.Context, trace.Span) {
	return t.tracer.Start(ctx, method,
		trace.WithSpanKind(kind), trace.WithAttributes(attrs...))
}

// 属性与事件键(spec 0004:GenAI semconv 可用键优先,不足处 flamingos.*
// 私有前缀;otel 模块 semconv 包未携带 gen_ai 键,此处以字面键名对齐
// GenAI semconv 的 gen_ai.tool.name,不为此新增依赖)。
const (
	attrMethod = "flamingos.method" // JSON-RPC method 名(initialize/tools/list/tools/call…)

	// attrToolName 是 GenAI semconv 的 tool 名键:入站侧为聚合名
	// (命名空间_原始名),出站侧为上游原始名。
	attrToolName = "gen_ai.tool.name"

	// attrNamespace 是命名空间;cmd 装配以命名空间为上游映射键,
	// 命名空间与上游名恒同值(架构 §5)。
	attrNamespace = "flamingos.namespace"

	attrUpstreamName = "flamingos.upstream.name" // 上游名(出站 client span)
	attrUpstreamType = "flamingos.upstream.type" // 上游类型:http|stdio(出站 client span)

	// attrCacheHit 是缓存命中属性键(spec 0006 Story 5,票 03):值恒为
	// bool——true=缓存命中,false=查过缓存但未命中。两态都写:属性存在性
	// 本身标记"该调用经过缓存层",只写 true 会让 miss 与"没查缓存"在
	// Jaeger 上无法分辨。
	attrCacheHit = "flamingos.cache.hit"

	// eventToolIsError 标记"上游正常应答但业务失败"(IsError 应答)。
	eventToolIsError = "flamingos.tool.is_error"
)

// MarkCacheHit 向 ctx 当前 span 写缓存命中属性(spec 0006 Story 5,票 03):
// 网关请求 span 由 telemetry 的 Receiving 中间件在链最外层开出,内层代码
// (cache 装饰器的 OnHit 回调,经 cmd 装配闭包接到本入口)可据 ctx 写属性
// ——命中请求在 Jaeger 上可分辨"快是因为缓存"。hit 语义与值域见 attrCacheHit
// 注释;ctx 无 recording span 时为 no-op。nil 接线返回(关闭态零行为)。
func (t *Telemetry) MarkCacheHit(ctx context.Context, hit bool) {
	if t == nil {
		return
	}
	trace.SpanFromContext(ctx).SetAttributes(attribute.Bool(attrCacheHit, hit))
}

// TraceIDFromContext 返回 ctx 当前 span 的 trace_id(32 字符 hex,W3C
// traceparent 同款形态;spec 0008 票 01):录制器经 cmd 装配闭包注入本访问
// 器,录制条目冗余 trace_id 与 Jaeger 排查对查。ctx 无 recording span(可
// 观测关闭态)或 span 上下文无效时返回空串不报错——只读观察,不新开 span
// 也不依赖 Telemetry 实例,关闭态天然留空。
func TraceIDFromContext(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return ""
	}
	return sc.TraceID().String()
}
