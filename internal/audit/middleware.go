// 审计中间件:mcp 层 Receiving 形态(与 policy/telemetry 的中间件同构,
// nil 哨兵/构造注入)。挂在 RBAC 判定之外层——审计先下探、待决策产生后
// 落盘,allow 与 deny 两条路径都恰好记一条(spec 0005:RBAC deny 不下探
// next,审计若在其内层将看不到拒绝)。
package audit

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// methodCallTool 是被审计的唯一 JSON-RPC method:initialize/tools/list 等
// 其余方法不产生审计记录。SDK 未导出该字面量常量,与 policy 包各自以本包
// 单一出处对齐 MCP method 名(互不 import 的代价,两处由 e2e 钉住一致)。
const methodCallTool = "tools/call"

// ReceivingMiddleware 返回审计中间件:仅对 tools/call 记录——下探 next
// (判定与 handler)拿到结果与错误后,从 ctx 取角色与决策落盘一条;ctx
// 无决策(RBAC 关闭、全放行形态)按 allow 记录。自身零拦截:结果与错误
// 原样透传,写盘失败不反噬调用(见 Append)。nil 接线返回 nil,装配处据
// 此不安装(关闭态零开销)。
func (r *Recorder) ReceivingMiddleware() mcp.Middleware {
	if r == nil {
		return nil
	}
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != methodCallTool {
				return next(ctx, method, req)
			}
			res, err := next(ctx, method, req)
			r.Append(Record{
				Role:     r.roleOf(ctx),
				Tool:     toolNameOf(req),
				Decision: r.decisionOf(ctx),
				Error:    err != nil,
			})
			return res, err
		}
	}
}

// roleOf 取 ctx 中的角色声明(装配层注入的键);缺失返回空串——正常装配
// 下 transport 的角色提取层已带头名/默认角色兜底写入,审计不做第二次兜底。
func (r *Recorder) roleOf(ctx context.Context) string {
	role, _ := ctx.Value(r.cfg.RoleKey).(string)
	return role
}

// decisionOf 把 ctx 中的决策渲染为 allow/deny:有决策值经注入的翻译闭包
// 判定;无决策(RBAC 关闭态,不存在拒绝)按 allow 记录。
func (r *Recorder) decisionOf(ctx context.Context) string {
	v := ctx.Value(r.cfg.DecisionKey)
	if v == nil {
		return DecisionAllow
	}
	if r.cfg.DecisionAllowed != nil && r.cfg.DecisionAllowed(v) {
		return DecisionAllow
	}
	return DecisionDeny
}

// toolNameOf 取 tools/call 的聚合工具名;params 形状异常时返回空串(记录
// 照落,字段留空可查)——审计只观察,不做判定层那个 fail-close 兜底。
// 服务端 Receiving 链路上 SDK 对 tools/call 恒传 *mcp.CallToolParamsRaw
// (v1.8.0 实测,与 policy 包同一结论)。
func toolNameOf(req mcp.Request) string {
	if params, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok && params != nil {
		return params.Name
	}
	return ""
}
