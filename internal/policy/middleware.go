// RBAC 判定中间件:mcp 层 Receiving 形态(与 telemetry.ReceivingMiddleware
// 同构,nil 哨兵/构造注入),以及角色与决策的 ctx 传递(专用 key 类型)。
package policy

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// methodCallTool 是被判定拦截的唯一 JSON-RPC method:initialize/tools/list
// 等其余方法不判放行(spec 0005——tools/list 也不做未授权过滤,已知边界)。
// SDK 未导出该字面量常量,此处以本包单一出处对齐 MCP method 名。
const methodCallTool = "tools/call"

// CodePolicyDenied 是策略拒绝的应用自定义 JSON-RPC 错误码(spec 0005:
// MCP 规范建议新码分配在 -32768..-32000 之外,-40001 满足)。
const CodePolicyDenied = -40001

// ctx 键(spec 0005 Story 18:决策结果经 ctx 传递,三方同口径):
// 包内专用类型防碰撞,经 RoleContextKey/DecisionContextKey 以 any 注入给
// 互不 import 的消费方(票 02:audit 只认 ctx 键,由 cmd 装配注入)。
type ctxKey int

const (
	roleKey     ctxKey = iota + 1 // 角色声明字符串(transport 提取层写入)
	decisionKey                   // Decision 判定结果(判定中间件写入)
)

// RoleContextKey 返回角色声明的 ctx 键(不透明值,供装配层注入消费方)。
func RoleContextKey() any { return roleKey }

// DecisionContextKey 返回判定决策的 ctx 键(不透明值,供装配层注入消费方)。
func DecisionContextKey() any { return decisionKey }

// WithRole 把声明的角色放入 ctx:transport 的角色提取中间件(HTTP 头 →
// ctx)是唯一生产者;头名与默认角色的兜底口径见 Policy.RoleHeader/
// DefaultRole。
func WithRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, roleKey, role)
}

// RoleFromContext 取出 ctx 中的角色声明;缺失时返回空串,由消费方按默认
// 角色兜底。
func RoleFromContext(ctx context.Context) string {
	role, _ := ctx.Value(roleKey).(string)
	return role
}

// WithDecision 把判定决策放入 ctx:判定中间件是唯一生产者,审计与追踪以
// 同一决策为口径(allow/deny 的记录不另起判定)。
func WithDecision(ctx context.Context, d Decision) context.Context {
	return context.WithValue(ctx, decisionKey, d)
}

// DecisionFromContext 取出 ctx 中的判定决策;ok 为 false 表示本次调用没有
// 决策(非 tools/call,或判定中间件未安装)。
func DecisionFromContext(ctx context.Context) (Decision, bool) {
	d, ok := ctx.Value(decisionKey).(Decision)
	return d, ok
}

// ReceivingMiddleware 返回 RBAC 判定中间件:仅对 tools/call 判定——角色取
// ctx 声明(缺失落默认角色),按 deny 优先、fail-close 判定;放行把决策放
// 入 ctx 后照常下探;拒绝返回 JSON-RPC 错误 CodePolicyDenied,中文信息点名
// 角色与工具名,不下探 next(spec 0005 拒绝语义)。判定胶水经 decide 与
// DecideContext(transport 决策桥的接缝)共用同一口径。nil 接线返回 nil,
// 装配处据此不安装(关闭态零开销)。
func (p *Policy) ReceivingMiddleware() mcp.Middleware {
	if p == nil {
		return nil
	}
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != methodCallTool {
				return next(ctx, method, req)
			}
			ctx, d, role, tool := p.decide(ctx, req)
			if !d.Allowed {
				return nil, &jsonrpc.Error{
					Code:    CodePolicyDenied,
					Message: fmt.Sprintf("策略拒绝:角色 %q 无权调用工具 %q", role, tool),
				}
			}
			return next(ctx, method, req)
		}
	}
}

// DecideContext 对一次 tools/call 请求按唯一判定口径预写决策进 ctx(默认
// 角色兜底 → 取聚合工具名 → Decide 纯函数 → WithDecision),返回新 ctx。
// 供 transport 的决策桥使用:SDK 中间件链上 ctx 只向下传递,挂在判定外层
// 的审计看不到 deny 路径的写入,桥在判定之外层以本方法预写、审计与判定
// 同输入必同结果(spec 0005 Story 18 三方同口径)。
func (p *Policy) DecideContext(ctx context.Context, req mcp.Request) context.Context {
	nctx, _, _, _ := p.decide(ctx, req)
	return nctx
}

// decide 是判定胶水的单一出处(与 ReceivingMiddleware/DecideContext 共用):
// ctx 角色声明按默认角色兜底 → 取 tools/call 聚合工具名 → Decide 纯函数
// 判定 → 决策写入 ctx,一并带回决策与所用角色、工具名(拒绝错误信息据此
// 点名)。
func (p *Policy) decide(ctx context.Context, req mcp.Request) (context.Context, Decision, string, string) {
	role := RoleFromContext(ctx)
	if role == "" {
		role = p.defaultRole
	}
	tool := toolNameOf(req)
	d := p.Decide(role, tool)
	return WithDecision(ctx, d), d, role, tool
}

// toolNameOf 取 tools/call 的聚合工具名;params 形状异常时返回空串,交由
// fail-close 判定兜底。服务端 Receiving 链路上 SDK 对 tools/call 恒传
// *mcp.CallToolParamsRaw(v1.8.0 实测,探针核对;裸参数按约定由 tool
// handler 自行解码——判定只需要名字,不解码参数)。
func toolNameOf(req mcp.Request) string {
	if params, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok && params != nil {
		return params.Name
	}
	return ""
}
