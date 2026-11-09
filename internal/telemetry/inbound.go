// telemetry 入站接线:HTTP 头提取包装(transport 层 net/http 中间件形态)
// 与网关请求 span 的 server Receiving 中间件(spec 0004 入站提取)。
package telemetry

import (
	"context"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// HTTPMiddleware 返回入站 HTTP 头提取包装:把入站请求 traceparent 头
// 中的远程 trace 上下文注入请求 ctx(OTel HTTP 自动埋点的 Agent 无需
// 了解 MCP 即可接上);头缺失或值非法按 propagator 容错语义不注入,
// 后续 Receiving 中间件开 span 即自开根。nil 接线返回 nil,装配处据此
// 不包装(关闭态零开销)。
func (t *Telemetry) HTTPMiddleware() func(http.Handler) http.Handler {
	if t == nil {
		return nil
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(extractFromHeaders(r.Context(), r.Header)))
		})
	}
}

// ReceivingMiddleware 返回网关请求 span 的 server Receiving 中间件:
// 覆盖全部 JSON-RPC method——以 method 名开 SpanKindServer;tools/call
// 的 params 恒为 *mcp.CallToolParams,据此补聚合 tool 名与命名空间属性
// (按第一个下划线拆分,与 gateway 路由同规则,架构 §4.2 R2)。
// 开 span 前先从 params `_meta.traceparent`(SEP-414 协议原生载体)
// 提取远程父——有效则覆盖 ctx 已有的 HTTP 头提取结果,入站优先级
// (`_meta` > 头)在这一处编排中单一出处地成立(Story 3/26);_meta
// 缺失或非法时沿用头结果或自开根。出段补全错误语义:next 报错(含
// 点名上游的"不可用")→ span Error + exception 事件;业务失败
// (IsError 应答)→ 仅记事件不改错误状态——应答在协议层是成功的
// (spec 0004 Story 9/10)。result/err 原样透传。
// nil 接线返回 nil,装配处据此不安装。
func (t *Telemetry) ReceivingMiddleware() mcp.Middleware {
	if t == nil {
		return nil
	}
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			attrs := []attribute.KeyValue{attribute.String(attrMethod, method)}
			if params, ok := req.GetParams().(*mcp.CallToolParams); ok && params != nil {
				attrs = append(attrs, attribute.String(attrToolName, params.Name))
				if ns, _, found := strings.Cut(params.Name, "_"); found {
					attrs = append(attrs, attribute.String(attrNamespace, ns))
				}
			}
			if p := paramsOf(req); p != nil {
				ctx = extractFromMeta(ctx, p.GetMeta())
			}
			ctx, span := t.startSpan(ctx, method, trace.SpanKindServer, attrs...)
			result, err := next(ctx, method, req)
			completeSpan(span, result, err)
			return result, err
		}
	}
}

// completeSpan 在请求处理返回后补全并结束 span(出段补全,入站与出站
// 中间件共用):next 报错属传输/协议层失败(上游不可用即此族),标
// Error 并记 exception 事件;IsError 应答属"协议层成功应答、业务语义
// 失败",仅记事件、不动错误状态(Story 9/10 的区分在此单一出处)。
func completeSpan(span trace.Span, result mcp.Result, err error) {
	defer span.End()
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
		return
	}
	if res, ok := result.(*mcp.CallToolResult); ok && res != nil && res.IsError {
		span.AddEvent(eventToolIsError)
	}
}
