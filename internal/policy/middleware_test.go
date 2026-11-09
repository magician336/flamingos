// policy 判定中间件测试:接缝在 mcp.Middleware 本身——手工装配 next 桩,
// 以 &mcp.ServerRequest 驱动真实中间件链内行为(SDK 机器类型不跨出本包的
// 纪律对测试同样适用:测试只见 Request/Result 接口与 jsonrpc.Error)。
package policy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// middlewareHarness 以给定 Policy 构造判定中间件并返回套上桩的 handler:
// 桩记录调用次数与收到的 ctx,allow 路径返回哨兵 result 供透传断言。
type middlewareHarness struct {
	p       *Policy
	calls   int
	gotCtx  context.Context
	gotMeth string
}

func (h *middlewareHarness) handle(ctx context.Context, method string, _ mcp.Request) (mcp.Result, error) {
	h.calls++
	h.gotCtx = ctx
	h.gotMeth = method
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "透传"}}}, nil
}

func newMiddleware(p *Policy) (*middlewareHarness, mcp.MethodHandler) {
	h := &middlewareHarness{p: p}
	return h, p.ReceivingMiddleware()(h.handle)
}

// toolReq 构造 tools/call 的 ServerRequest(判定的真实入参形状:服务端
// Receiving 链路上 SDK 对 tools/call 传 *mcp.CallToolParamsRaw,v1.8.0
// 实测;裸参数由 tool handler 自行解码,判定只需要名字)。
func toolReq(tool string) mcp.Request {
	return &mcp.ServerRequest[*mcp.CallToolParamsRaw]{Params: &mcp.CallToolParamsRaw{Name: tool}}
}

func TestReceivingMiddlewareNilSentinel(t *testing.T) {
	var p *Policy
	if mw := p.ReceivingMiddleware(); mw != nil {
		t.Fatal("nil 哨兵应返回 nil 中间件(关闭态零挂点),实际非 nil")
	}
}

func TestReceivingMiddlewareDeny(t *testing.T) {
	p := New(Config{
		DefaultRole: "viewer",
		Roles:       map[string]RoleRules{"viewer": {Allow: []string{"calc_*"}}},
	})
	h, handler := newMiddleware(p)

	_, err := handler(WithRole(context.Background(), "viewer"), "tools/call", toolReq("fs_cat"))
	if err == nil {
		t.Fatal("期望 deny 报错,实际放行")
	}
	var wire *jsonrpc.Error
	if !errors.As(err, &wire) {
		t.Fatalf("拒绝应为 JSON-RPC 结构化错误,实际 %T: %v", err, err)
	}
	if wire.Code != CodePolicyDenied {
		t.Errorf("错误码 = %d,期望 %d", wire.Code, CodePolicyDenied)
	}
	for _, want := range []string{"策略拒绝", "viewer", "fs_cat"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息应点名 %q(中文),实际 %q", want, err.Error())
		}
	}
	if h.calls != 0 {
		t.Errorf("deny 路径不应下探 next,实际下探 %d 次", h.calls)
	}
}

func TestReceivingMiddlewareAllow(t *testing.T) {
	p := New(Config{
		DefaultRole: "viewer",
		Roles:       map[string]RoleRules{"viewer": {Allow: []string{"calc_*"}}},
	})
	h, handler := newMiddleware(p)

	res, err := handler(WithRole(context.Background(), "viewer"), "tools/call", toolReq("calc_add"))
	if err != nil {
		t.Fatalf("期望放行,实际报错: %v", err)
	}
	if h.calls != 1 {
		t.Fatalf("allow 路径应恰好下探 next 一次,实际 %d 次", h.calls)
	}
	if res == nil {
		t.Fatal("next 的结果应原样透传,实际为 nil")
	}

	// 决策经 ctx 传递(专用 key 类型):next 收到的 ctx 必须携带同一次判定。
	d, ok := DecisionFromContext(h.gotCtx)
	if !ok {
		t.Fatal("next 的 ctx 应携带决策,实际缺失")
	}
	want := Decision{Role: "viewer", Tool: "calc_add", Allowed: true}
	if d != want {
		t.Errorf("ctx 决策 = %+v,期望 %+v", d, want)
	}
}

func TestReceivingMiddlewareDefaultRole(t *testing.T) {
	p := New(Config{
		DefaultRole: "viewer",
		Roles:       map[string]RoleRules{"viewer": {Allow: []string{"calc_*"}}},
	})
	h, handler := newMiddleware(p)

	// ctx 无角色(等价于 transport 未提取到任何声明的兜底缺失):按默认角色判定。
	_, err := handler(context.Background(), "tools/call", toolReq("fs_cat"))
	if err == nil {
		t.Fatal("默认角色未覆盖的工具应 fail-close 拒绝")
	}
	if !strings.Contains(err.Error(), "viewer") {
		t.Errorf("拒绝信息应按默认角色 viewer 点名,实际 %q", err.Error())
	}
	if h.calls != 0 {
		t.Errorf("deny 路径不应下探 next,实际下探 %d 次", h.calls)
	}
}

func TestReceivingMiddlewareOnlyToolsCall(t *testing.T) {
	p := New(Config{
		DefaultRole: "ghost", // 无任何规则的角色:若误判 tools/list 也必拒
		Roles:       map[string]RoleRules{},
	})
	h, handler := newMiddleware(p)

	res, err := handler(context.Background(), "tools/list", &mcp.ServerRequest[*mcp.ListToolsParams]{})
	if err != nil {
		t.Fatalf("tools/list 不判放行,应原样放行,实际报错: %v", err)
	}
	if res == nil {
		t.Fatal("tools/list 的结果应原样透传")
	}
	if _, ok := DecisionFromContext(h.gotCtx); ok {
		t.Error("非 tools/call 不应产生决策")
	}
}

func TestContextKeysAreDistinctAndStable(t *testing.T) {
	// 键为包内专用类型、经 any 注入给互不 import 的消费方(票 02):
	// 两次取值必须一致(comparable 且稳定),角色键与决策键必须互异。
	if RoleContextKey() != RoleContextKey() {
		t.Fatal("角色键两次取值应一致(可作 ctx.Value 的稳定键)")
	}
	if DecisionContextKey() != DecisionContextKey() {
		t.Fatal("决策键两次取值应一致(可作 ctx.Value 的稳定键)")
	}
	if RoleContextKey() == DecisionContextKey() {
		t.Fatal("角色键与决策键必须互异")
	}
}
