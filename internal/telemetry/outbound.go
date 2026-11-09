// telemetry 出站接线:上游 client 的 Sending 中间件(child client span)
// 与 HTTP 上游出站请求的标准头注入 RoundTripper(spec 0004 出站注入;
// _meta 通道由票 0004-02 在此基础上增补)。
package telemetry

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// SendingMiddleware 返回该上游专属的出站 child client span 中间件:
// 上游名与类型经构造注入(每个 adapter 构造时各取一份);对选中上游的
// 转发 = 网关请求 span 的 child(spec 0004 span 拓扑)。span 属性带
// method、原始 tool 名、上游名、上游类型——与网关请求 span 上的 method/
// 聚合 tool 名/命名空间共同覆盖票面五项属性集。开 span 后向请求 params
// `_meta` 注入 `traceparent`(SEP-414,与该 span 同 child 上下文):stdio
// 上游没有 HTTP 头,这是它唯一的 trace 通道;既有键保留、副本修改不改写
// 原 map。HTTP 上游的 RoundTripper 据同一 ctx 注入标准头(同一 child
// 上下文双通道)。错误语义与入站同源(completeSpan):next 报错
// → Error + exception 事件;IsError → 仅记事件。nil 接线返回 nil。
func (t *Telemetry) SendingMiddleware(upstreamName, upstreamType string) mcp.Middleware {
	if t == nil {
		return nil
	}
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			attrs := []attribute.KeyValue{
				attribute.String(attrMethod, method),
				attribute.String(attrUpstreamName, upstreamName),
				attribute.String(attrUpstreamType, upstreamType),
			}
			if params, ok := req.GetParams().(*mcp.CallToolParams); ok && params != nil {
				attrs = append(attrs, attribute.String(attrToolName, params.Name))
			}
			ctx, span := t.startSpan(ctx, method, trace.SpanKindClient, attrs...)
			if p := paramsOf(req); p != nil {
				p.SetMeta(injectToMeta(ctx, p.GetMeta()))
			}
			result, err := next(ctx, method, req)
			completeSpan(span, result, err)
			return result, err
		}
	}
}

// RoundTripper 返回向出站 HTTP 请求注入标准 traceparent 头的
// RoundTripper:注入的是 ctx 当前 span(即 Sending 中间件刚开的 child
// client span,go-sdk StreamableClientTransport 以调用 ctx 构造 POST
// 请求,两侧同一 ctx)。OTel HTTP 自动埋点的上游无需解析任何 MCP 结构
// 即可接上。base 为 nil 时回落 http.DefaultTransport;请求与响应/错误
// 原样透传。nil 接线返回 nil,装配处据此不注入自定义 HTTPClient。
func (t *Telemetry) RoundTripper(base http.RoundTripper) http.RoundTripper {
	if t == nil {
		return nil
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		injectToHeaders(req.Context(), req.Header)
		return base.RoundTrip(req)
	})
}

// roundTripperFunc 让闭包满足 http.RoundTripper(仅包内装配使用)。
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
