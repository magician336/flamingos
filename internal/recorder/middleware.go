// 录制中间件:mcp 层 Receiving 形态(与 telemetry/policy/audit 的中间件
// 同构,nil 哨兵/构造注入)。挂 Receiving 链末位(RBAC 判定内层)——判定
// 放行才下探 next,deny 路径不下探故不入录制(spec 0008:基准表征工具处理
// 行为而非策略配置);只处理 tools/call,其余方法原样下探零改动。自身零
// 拦截:结果与错误原样透传,写盘失败不反噬调用(见 Append)。
package recorder

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// methodCallTool 是被录制的唯一 JSON-RPC method:initialize/tools/list 等
// 其余方法不产生录制记录。SDK 未导出该字面量常量,与 transport/policy/
// audit 各以本包单一出处对齐 MCP method 名(互不 import 的代价,一致性
// 由 e2e 钉住)。
const methodCallTool = "tools/call"

// ReceivingMiddleware 返回录制中间件:仅对 tools/call 记录——下探 next
// (转发 handler)拿到结果、错误与耗时后落盘一条;trace_id 经装配注入的
// 只读访问器取自 ctx(链最外层的网关请求 span 已在其中),角色取 ctx 键
// (缺失留空,不做第二次兜底——与 audit 同一口径)。nil 接线返回 nil,
// 装配处据此不安装(关闭态零挂点)。
func (r *Recorder) ReceivingMiddleware() mcp.Middleware {
	if r == nil {
		return nil
	}
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != methodCallTool {
				return next(ctx, method, req)
			}
			start := time.Now()
			res, err := next(ctx, method, req)
			r.appendCall(ctx, req, res, err, time.Since(start))
			return res, err
		}
	}
}

// appendCall 把一次实际执行的 tools/call 翻译为录制记录后落盘:三态 kind
// 判定(传输错误文案与 IsError 分开记录)与字段提取全部在此单一出处。
// params 形状异常时 tool/args 留空可查、记录照落(沿 audit 的观察者纪律,
// 不做判定层那个 fail-close 兜底)。
func (r *Recorder) appendCall(ctx context.Context, req mcp.Request, res mcp.Result, err error, latency time.Duration) {
	rec := Record{
		Method:    methodCallTool,
		LatencyMS: latency.Milliseconds(),
	}
	if params, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok && params != nil {
		rec.Tool = params.Name
		if len(params.Arguments) > 0 {
			rec.Args = append(json.RawMessage(nil), params.Arguments...) // 副本移交,原始字节不重编码
		}
	}
	switch {
	case err != nil:
		rec.Kind = KindTransportError
		rec.Error = err.Error()
	default:
		rec.Kind = KindSuccess
		if ctr, ok := res.(*mcp.CallToolResult); ok && ctr != nil && ctr.IsError {
			rec.Kind = KindBusinessFailure
		}
		if res != nil {
			if encoded, merr := json.Marshal(res); merr == nil {
				rec.Result = encoded
			} else {
				log.Printf("警告: 录制结果序列化失败(工具 %q): %v", rec.Tool, merr)
			}
		}
	}
	if r.cfg.TraceIDOf != nil {
		rec.TraceID = r.cfg.TraceIDOf(ctx)
	}
	rec.Role, _ = ctx.Value(r.cfg.RoleKey).(string)
	r.Append(rec)
}
