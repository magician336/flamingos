// transport 的策略接线测试(票 0005-01/spec 0005):经构造参数注入 policy
// 后,HTTP 角色头提取与 mcp 层 RBAC 判定在真实 handler 上生效——带角色头的
// allow 调用往返成功、deny 调用收 JSON-RPC -40001、被拒流量在 span 上标注
// 错误(SpanRecorder,同时钉死 RBAC 挂在 trace Receiving 之内);不注入时
// 关闭态零行为回归。Agent 侧用带 mcp-role 头的自定义 RoundTripper 模拟
// 声明角色的客户端(与 tracing_test.go 的 agentTraceRT 同款形态)。
package transport_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/magician336/flamingos/internal/gateway"
	"github.com/magician336/flamingos/internal/policy"
	"github.com/magician336/flamingos/internal/telemetry"
	"github.com/magician336/flamingos/internal/transport"
	"github.com/magician336/flamingos/internal/upstream"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// roleRT 为每个出站请求注入声明的角色头——模拟在 HTTP 头自报角色的 Agent;
// role 为空串时不注入任何头(未带头的最简客户端)。
type roleRT struct {
	role   string
	header string // 角色头名;空串 = 缺省头名 mcp-role
}

func (r roleRT) RoundTrip(req *http.Request) (*http.Response, error) {
	header := r.header
	if header == "" {
		header = "mcp-role"
	}
	if r.role != "" {
		req.Header.Set(header, r.role)
	}
	return http.DefaultTransport.RoundTrip(req)
}

// policyHarness 是带策略接线的进程内网关:与 tracedHarness 同形,transport.New
// 同时注入 telemetry 与 policy;带 SpanRecorder 以断言被拒流量的 span 错误。
// role/header 决定 Agent 侧注入的角色头(模拟声明角色的客户端)。
func policyHarness(t *testing.T, clients map[string]upstream.Client, tel *telemetry.Telemetry, pol *policy.Policy, role, header string) *gatewayHarness {
	t.Helper()
	handler, err := transport.New(context.Background(), gateway.New(clients),
		transport.WithTelemetry(tel), transport.WithPolicy(pol))
	if err != nil {
		t.Fatalf("构造 transport 失败: %v", err)
	}
	ts := newTestServer(t, handler)
	ct := &mcp.StreamableClientTransport{
		Endpoint:             ts.URL + "/mcp",
		DisableStandaloneSSE: true,
		HTTPClient:           &http.Client{Transport: roleRT{role: role, header: header}},
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "0.0.1"}, nil).
		Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatalf("连接网关失败: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return &gatewayHarness{ts: ts, cs: cs}
}

// recordedTelemetry 返回注入用的 telemetry 接线与配套 SpanRecorder。
func recordedTelemetry(t *testing.T) (*telemetry.Telemetry, *tracetest.SpanRecorder) {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return telemetry.New(tp), sr
}

// policyTools 是双上游假客户端:calc.greet(回显原始名)与 fs.echo。
func policyTools() map[string]upstream.Client {
	return map[string]upstream.Client{
		"calc": &fakeClient{tools: []*mcp.Tool{
			{Name: "greet", InputSchema: map[string]any{"type": "object"}},
		}},
		"fs": &fakeClient{tools: []*mcp.Tool{
			{Name: "echo", InputSchema: map[string]any{"type": "object"}},
		}},
	}
}

// viewerPolicy 是贯穿测试的规则表:viewer 放行 calc_ 前缀,其余 fail-close。
func viewerPolicy(header string) *policy.Policy {
	return policy.New(policy.Config{
		RoleHeader:  header,
		DefaultRole: "viewer",
		Roles:       map[string]policy.RoleRules{"viewer": {Allow: []string{"calc_*"}}},
	})
}

func TestTransportPolicy(t *testing.T) {
	t.Run("allow:带角色头的调用照常往返成功,span 无错误标注", func(t *testing.T) {
		tel, sr := recordedTelemetry(t)
		h := policyHarness(t, policyTools(), tel, viewerPolicy("mcp-role"), "viewer", "")

		res, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "calc_greet",
			Arguments: json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatalf("allow 角色的调用应照常成功,实际报错: %v", err)
		}
		if got := textOf(res); got != "greet" {
			t.Errorf("往返内容失真,实际 %q", got)
		}

		span := spanFinder(t, sr)("tools/call")
		if status := span.Status(); status.Code == codes.Error {
			t.Errorf("放行调用的 span 不应标注错误,实际 %v(%s)", status.Code, status.Description)
		}
	})

	t.Run("deny:未覆盖工具收 JSON-RPC -40001,中文点名角色与工具,span 标注错误", func(t *testing.T) {
		tel, sr := recordedTelemetry(t)
		h := policyHarness(t, policyTools(), tel, viewerPolicy("mcp-role"), "viewer", "")

		_, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "fs_echo",
			Arguments: json.RawMessage(`{}`),
		})
		if err == nil {
			t.Fatal("期望 deny 报错,实际放行")
		}
		var wire *jsonrpc.Error
		if !errors.As(err, &wire) {
			t.Fatalf("拒绝应为 JSON-RPC 结构化错误,实际 %T: %v", err, err)
		}
		if wire.Code != policy.CodePolicyDenied {
			t.Errorf("错误码 = %d,期望 %d", wire.Code, policy.CodePolicyDenied)
		}
		for _, want := range []string{"viewer", "fs_echo"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("拒绝信息应点名 %q,实际 %q", want, err.Error())
			}
		}

		// span 错误可见,同时钉死装配顺序语义:RBAC 判定中间件在 trace
		// Receiving 之后安装(SDK 先装先执行=外层),拒绝错误才会落进
		// 网关请求 span——若 RBAC 在 trace 之外,此 span 根本不会产生。
		span := spanFinder(t, sr)("tools/call")
		if status := span.Status(); status.Code != codes.Error {
			t.Errorf("被拒调用的 span 应标注 Error,实际 %v(%s)", status.Code, status.Description)
		}
		for _, ev := range span.Events() {
			if ev.Name == "exception" {
				return
			}
		}
		t.Errorf("被拒调用的 span 应记录 exception 事件,实际 %v", span.Events())
	})

	t.Run("未带头:按默认角色判定(fail-close 点名默认角色)", func(t *testing.T) {
		tel, _ := recordedTelemetry(t)
		h := policyHarness(t, policyTools(), tel, viewerPolicy("mcp-role"), "", "")

		_, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "fs_echo",
			Arguments: json.RawMessage(`{}`),
		})
		if err == nil {
			t.Fatal("未带头走默认角色,未覆盖工具应被拒")
		}
		if !strings.Contains(err.Error(), "viewer") {
			t.Errorf("拒绝信息应按默认角色 viewer 点名,实际 %q", err.Error())
		}
	})

	t.Run("头名可配:transport 按 policy 声明的头名提取,缺省头名不生效", func(t *testing.T) {
		tel, _ := recordedTelemetry(t)
		h := policyHarness(t, policyTools(), tel, viewerPolicy("x-agent-role"), "viewer", "x-agent-role")

		if _, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "calc_greet",
			Arguments: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatalf("按配置头名声明的角色应放行,实际报错: %v", err)
		}

		// 同一网关:把角色写进缺省头名 mcp-role——头名已改为 x-agent-role,
		// 该声明不被读取,按默认角色 viewer 判定 fs_echo 仍被拒。
		h2 := policyHarness(t, policyTools(), tel, viewerPolicy("x-agent-role"), "viewer", "mcp-role")
		if _, err := h2.cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "fs_echo",
			Arguments: json.RawMessage(`{}`),
		}); err == nil {
			t.Fatal("缺省头名的声明在头名改配后不应生效,fs_echo 应被拒")
		}
	})

	t.Run("仅拦 tools/call:未知角色也能 initialize 与 tools/list,调用才拒", func(t *testing.T) {
		tel, _ := recordedTelemetry(t)
		// ghost 未声明任何规则:连接本身即完成 initialize(不判),清单照常。
		h := policyHarness(t, policyTools(), tel, viewerPolicy("mcp-role"), "ghost", "")

		list, err := h.cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatalf("tools/list 不判放行,应照常成功,实际报错: %v", err)
		}
		if len(list.Tools) != 2 {
			t.Errorf("聚合清单应含 2 个工具,实际 %v", toolNames(list.Tools))
		}

		if _, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "calc_greet",
			Arguments: json.RawMessage(`{}`),
		}); err == nil {
			t.Fatal("未知角色的 tools/call 应 fail-close 拒绝")
		}
	})

	t.Run("关闭态:不注入 policy 时角色头被忽略,行为照旧", func(t *testing.T) {
		tel, sr := recordedTelemetry(t)
		// 不传 WithPolicy:默认关闭态。声明角色照常声明,网关全放行。
		h := policyHarness(t, policyTools(), tel, nil, "ghost", "")

		if _, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "fs_echo",
			Arguments: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatalf("关闭态调用应照常成功,实际报错: %v", err)
		}
		if len(sr.Ended()) == 0 {
			t.Error("关闭策略不应影响可观测接线:调用仍应有网关请求 span")
		}
	})
}
