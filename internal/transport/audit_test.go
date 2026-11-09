// transport 的审计接线测试(票 0005-02/spec 0005):审计中间件挂在 mcp 层
// Receiving 链上 trace 之内、RBAC 判定之外——先下探待决策产生,allow 与
// deny 都落一条记录(deny 能被记录本身就钉死了 audit 在 policy 外层的装配
// 顺序:RBAC 拒绝不下探 next,审计若在其内层将看不到拒绝)。ctx 键与决策
// 翻译按 cmd 装配的同款注入(policy 访问器 + 类型断言闭包),同时验证该
// 接缝的真实可用。不注入 WithAudit / 注入 nil 哨兵均为关闭态零挂点。
package transport_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/magician336/flamingos/internal/audit"
	"github.com/magician336/flamingos/internal/gateway"
	"github.com/magician336/flamingos/internal/policy"
	"github.com/magician336/flamingos/internal/telemetry"
	"github.com/magician336/flamingos/internal/transport"
	"github.com/magician336/flamingos/internal/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// auditLines 读回审计文件全部记录(JSON-lines 逐行反序列化)。
func auditLines(t *testing.T, path string) []audit.Record {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读审计文件 %s 失败: %v", path, err)
	}
	var lines []audit.Record
	for i, line := range splitLines(string(raw)) {
		var rec audit.Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("审计第 %d 行不是合法 JSON: %v(%q)", i+1, err, line)
		}
		lines = append(lines, rec)
	}
	return lines
}

// splitLines 按 JSON-lines 行边界拆分(容忍缺尾换行的文件)。
func splitLines(text string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			lines = append(lines, text[start:i])
			start = i + 1
		}
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	return lines
}

// assembledAudit 按cmd 装配的同款注入构造审计器:t.TempDir 下的审计文件
// 路径 + policy 的两个 ctx 键 + policy.Decision 类型断言闭包。
func assembledAudit(t *testing.T) (*audit.Recorder, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	rec, err := audit.New(audit.Config{
		Path:        path,
		RoleKey:     policy.RoleContextKey(),
		DecisionKey: policy.DecisionContextKey(),
		DecisionAllowed: func(v any) bool {
			d, ok := v.(policy.Decision)
			return ok && d.Allowed
		},
	})
	if err != nil {
		t.Fatalf("构造审计器失败: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	return rec, path
}

// auditHarness 是带审计接线的进程内网关:与 policyHarness 同形,transport.New
// 注入 telemetry/policy/audit 三路;role 决定 Agent 侧注入的角色头。
func auditHarness(t *testing.T, clients map[string]upstream.Client, tel *telemetry.Telemetry,
	pol *policy.Policy, aud *audit.Recorder, role string) *gatewayHarness {
	t.Helper()
	handler, err := transport.New(context.Background(), gateway.New(clients),
		transport.WithTelemetry(tel), transport.WithPolicy(pol), transport.WithAudit(aud))
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
	return &gatewayHarness{ts: ts, cs: cs}
}

func TestTransportAudit(t *testing.T) {
	t.Run("allow 与 deny 各一条:字段、顺序与判定同口径,deny 可见钉死外层装配", func(t *testing.T) {
		tel, _ := recordedTelemetry(t)
		aud, path := assembledAudit(t)
		h := auditHarness(t, policyTools(), tel, viewerPolicy("mcp-role"), aud, "viewer")

		// 第一调:calc_greet 放行(viewer 允许 calc_*),照常往返。
		if _, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "calc_greet", Arguments: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatalf("放行调用应成功,实际报错: %v", err)
		}
		// 第二调:fs_echo 被拒(未覆盖工具 fail-close),错误照收。
		if _, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "fs_echo", Arguments: json.RawMessage(`{}`),
		}); err == nil {
			t.Fatal("被拒调用应收错误,实际放行")
		}

		lines := auditLines(t, path)
		if len(lines) != 2 {
			t.Fatalf("allow/deny 两类调用应各产生一条记录,实际 %d 条: %+v", len(lines), lines)
		}
		want := []audit.Record{
			{TS: lines[0].TS, Role: "viewer", Tool: "calc_greet", Decision: audit.DecisionAllow, Error: false},
			{TS: lines[1].TS, Role: "viewer", Tool: "fs_echo", Decision: audit.DecisionDeny, Error: true},
		}
		for i, w := range want {
			if lines[i] != w {
				t.Errorf("第 %d 条记录 = %+v,期望 %+v", i+1, lines[i], w)
			}
		}
	})

	t.Run("nil 哨兵:WithAudit(nil) 关闭态零挂点,调用照常", func(t *testing.T) {
		tel, _ := recordedTelemetry(t)
		h := auditHarness(t, policyTools(), tel, viewerPolicy("mcp-role"), nil, "viewer")

		if _, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "calc_greet", Arguments: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatalf("关闭审计不应影响调用,实际报错: %v", err)
		}
	})
}
