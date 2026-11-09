// transport 的全装配收口 e2e(票 0005-03/spec 0005):telemetry+policy+
// audit+origin+ratelimit 五路接线一次装配,钉死票面验收的五个一例——放行
// 往返、deny -40001、Origin 403、桶空 429、审计行落盘;同时钉住链顺序语义:
// 403/429 在 HTTP 最外层被拒,到不了 mcp 层审计(无审计行)也不消耗令牌
// (Origin 在限流外层)。SDK client 不发 Origin 头,经 mcp 会话的调用天然
// 走"无 Origin 放行"路径。
package transport_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/magician336/flamingos/internal/audit"
	"github.com/magician336/flamingos/internal/gateway"
	"github.com/magician336/flamingos/internal/policy"
	"github.com/magician336/flamingos/internal/transport"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fullHarness 五路全装配的进程内网关:与 auditHarness 同形,追加 origin
// 与 ratelimit 两路 HTTP 层接线;burst=4 预算留足 mcp 会话(init+2 调用)
// 后恰好一枚供穿透验证。返回审计文件路径。
func fullHarness(t *testing.T, role string) (*gatewayHarness, string) {
	t.Helper()
	tel, _ := recordedTelemetry(t)
	aud, path := assembledAudit(t)
	handler, err := transport.New(context.Background(), gateway.New(policyTools()),
		transport.WithTelemetry(tel),
		transport.WithPolicy(viewerPolicy("mcp-role")),
		transport.WithAudit(aud),
		transport.WithOrigin([]string{"http://localhost:*"}),
		transport.WithRateLimit(transport.RateLimitConfig{RPS: 1, Burst: 4}),
	)
	if err != nil {
		t.Fatalf("构造 transport 失败: %v", err)
	}
	ts := newTestServer(t, handler)
	ct := &mcp.StreamableClientTransport{
		Endpoint:             ts.URL + "/mcp",
		DisableStandaloneSSE: true,
		HTTPClient:           &http.Client{Transport: roleRT{role: role}},
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "agent", Version: "0.0.1"}, nil).
		Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatalf("连接网关失败: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return &gatewayHarness{ts: ts, cs: cs}, path
}

func TestTransportFullAssembly(t *testing.T) {
	h, auditPath := fullHarness(t, "viewer")

	// ① 放行:带角色头的调用照常往返(消费 2 号令牌;1 号在 initialize)。
	res, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "calc_greet",
		Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("放行调用应成功,实际报错: %v", err)
	}
	if got := textOf(res); got != "greet" {
		t.Errorf("放行往返内容失真,实际 %q", got)
	}

	// ② 拒绝:未覆盖工具收 JSON-RPC -40001(消费 3 号令牌)。
	_, err = h.cs.CallTool(context.Background(), &mcp.CallToolParams{
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

	// ③ Origin 403:不在允许列表的浏览器 Origin 被最外层拒——不进 mcp 层,
	// 不消耗令牌(此时桶内还剩 1 枚)。
	status, body := postWithOrigin(t, h.ts, "http://evil.example.com:3000")
	if status != http.StatusForbidden {
		t.Fatalf("非法 Origin 应被 403,实际状态码 %d", status)
	}
	if !strings.Contains(body, "Origin") {
		t.Errorf("403 响应体应点名 Origin 语义,实际 %q", body)
	}

	// ④ 429:最后 1 枚令牌(允许 Origin 的裸请求)之后,桶空即 429 + Retry-After。
	status, _ = postWithOrigin(t, h.ts, "http://localhost:3000")
	if status == http.StatusTooManyRequests {
		t.Fatal("倒数第二枚令牌应放行(穿透限流层),实际 429")
	}
	status, retryAfter := postNoOrigin(t, h.ts)
	if status != http.StatusTooManyRequests {
		t.Fatalf("桶空应被限流,实际状态码 %d", status)
	}
	if retryAfter != "1" {
		t.Errorf("Retry-After = %q,期望按 1 rps 补足取整的 %q", retryAfter, "1")
	}

	// ⑤ 审计:恰好两行(allow 与 deny 各一),403/429 的 HTTP 层拒绝不产生
	// 审计行——审计在 mcp 层只看 tools/call,链顺序语义由此钉死。
	lines := auditLines(t, auditPath)
	if len(lines) != 2 {
		t.Fatalf("mcp 层审计应恰好两条记录(HTTP 层 403/429 不落盘),实际 %d 条: %+v", len(lines), lines)
	}
	want := []audit.Record{
		{TS: lines[0].TS, Role: "viewer", Tool: "calc_greet", Decision: audit.DecisionAllow, Error: false},
		{TS: lines[1].TS, Role: "viewer", Tool: "fs_echo", Decision: audit.DecisionDeny, Error: true},
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("第 %d 条审计记录 = %+v,期望 %+v", i+1, lines[i], w)
		}
	}
}
