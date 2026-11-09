// Package telemetry 承担网关全部可观测知识(spec 0004):TracerProvider
// 的装配形态、W3C trace 上下文的 HTTP 头与 SEP-414 `_meta` 双通道传播、
// 入站网关请求 span 与出站上游 client span 的中间件、HTTP 上游的出站
// 头注入 RoundTripper。
//
// 依赖纪律(架构 §1/§2,spec 0004 Story 24):本包只依赖 OTel 与
// go-sdk 的协议类型/中间件函数类型,绝不 import transport/gateway/
// upstream——telemetry 是被装配进上述包的叶子能力,全部知识收在此处,
// 其余包只留装配行(架构 §8-1 的 W2 diff 纪律)。
package telemetry

import (
	"context"
	"maps"
	"net/http"
	"reflect"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/propagation"
)

// propagator 是全包共用的 W3C TraceContext 传播器。HTTP 头通道(票
// 0004-01)与 `_meta` 通道(票 0004-02,SEP-414)共用同一官方实现:
// 键名(traceparent)、W3C 值格式、非法 traceId/版本段/大小写等边界
// 情形的解析与生成都交给久经考验的实现,两通道零手写解析(spec 0004
// Implementation Decisions "W3C 格式复用")。
var propagator = propagation.TraceContext{}

// extractFromHeaders 从入站 HTTP 头提取远程 trace 上下文并挂入 ctx:
// 头缺失或值非法时按官方 propagator 容错语义返回不含 span 上下文的 ctx,
// 调用方随后开 span 即自开根。纯函数:不修改 header。
// 包内可见:仅同包 inbound/outbound 接线消费,不经导出面公开。
func extractFromHeaders(ctx context.Context, header http.Header) context.Context {
	return propagator.Extract(ctx, propagation.HeaderCarrier(header))
}

// injectToHeaders 把 ctx 中当前 span 的 trace 上下文写入出站 HTTP 头
// (标准 traceparent);ctx 无有效 span 时不写任何键、不触碰既有键。
// 纯函数:仅按 propagator 语义写头。
func injectToHeaders(ctx context.Context, header http.Header) {
	propagator.Inject(ctx, propagation.HeaderCarrier(header))
}

// extractFromMeta 从请求 `_meta`(SEP-414 协议原生载体,map[string]any)
// 提取远程 trace 上下文并挂入 ctx:_meta 缺失、无 traceparent 键或值
// 非法/非字符串时按官方 propagator 容错语义原样返回 ctx——与
// extractFromHeaders 同一 propagator,同一容错语义。提取出的有效上下文
// 覆盖 ctx 已有者,入站优先级(`_meta` > HTTP 头)由此编排在唯一调用
// 序(HTTP 头先注入 ctx,此处后提取)中单一出处地成立。
func extractFromMeta(ctx context.Context, meta map[string]any) context.Context {
	if len(meta) == 0 {
		return ctx
	}
	return propagator.Extract(ctx, metaCarrier{meta: meta})
}

// injectToMeta 把 ctx 中当前 span 的 trace 上下文写入 `_meta` 的**副本**
// 并返回:既有键原样带出,原 map 不被原地改写(不可变纪律,spec 0004
// Story 25——不污染调用链上共享的对象);ctx 无有效 span 时按 propagator
// 语义不写任何键,副本与原 map 内容一致。stdio 上游没有 HTTP 头,`_meta`
// 是它唯一的 trace 通道,也是本函数的存在理由。
func injectToMeta(ctx context.Context, meta map[string]any) map[string]any {
	fresh := make(map[string]any, len(meta)+1)
	maps.Copy(fresh, meta)
	propagator.Inject(ctx, metaCarrier{meta: fresh})
	return fresh
}

// metaCarrier 把 `_meta` 的 map[string]any 适配成官方 propagator 的
// TextMapCarrier(SEP-414:键名与 W3C 值格式由 propagator 决定,本类型
// 只做 map 透传)。Get 对非字符串值返回空串——propagator 视同键缺失,
// 容错不炸请求。
type metaCarrier struct {
	meta map[string]any
}

func (c metaCarrier) Get(key string) string {
	v, _ := c.meta[key].(string)
	return v
}

func (c metaCarrier) Set(key, value string) {
	c.meta[key] = value
}

func (c metaCarrier) Keys() []string {
	keys := make([]string, 0, len(c.meta))
	for k := range c.meta {
		keys = append(keys, k)
	}
	return keys
}

// paramsMeta 是"带 `_meta` 的请求参数"的最小读写接口:SDK 的全部请求
// params 类型(CallToolParams、ListToolsParams 等)都内嵌 Meta 而实现
// 它。不逐类型枚举,tools/call 之外的方法(initialize、tools/list…)
// 的 `_meta` 同样被传播覆盖(spec 0004 Story 12/22)。
type paramsMeta interface {
	GetMeta() map[string]any
	SetMeta(map[string]any)
}

// paramsOf 安全取 req 的带 `_meta` params:不支持 `_meta` 的参数或
// GetParams() 返回 nil / typed nil(SDK 分发对缺 params 的请求保留
// 零值指针,nil 接收者上调方法会 panic)时返回 nil。
func paramsOf(req mcp.Request) paramsMeta {
	p, ok := req.GetParams().(paramsMeta)
	if !ok {
		return nil
	}
	if v := reflect.ValueOf(p); v.Kind() == reflect.Pointer && v.IsNil() {
		return nil
	}
	return p
}
