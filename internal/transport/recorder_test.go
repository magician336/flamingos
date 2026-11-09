// transport 的录制接线测试(spec 0008 票 01):录制中间件挂在 mcp 层
// Receiving 链末位(RBAC 判定内层)——deny 路径不下探 next 故不入录制
// (被拒调用不出现在录制文件里,本身就钉死了录制在判定内层的装配顺序:
// 若挂在判定外层,deny 也会落记录)。注入形态与 cmd 装配同款(policy 的
// ctx 键 + telemetry 只读访问器闭包),trace_id 与 SpanRecorder 的网关请求
// span 交叉核对,钉死"链最外层开的 span 在录制层可见"。三态 kind 各一例,
// 参数字节经真实 SDK 往返逐字节保留。不注入 WithRecorder / 注入 nil 哨兵
// 均为关闭态零挂点。
package transport_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/magician336/flamingos/internal/gateway"
	"github.com/magician336/flamingos/internal/policy"
	"github.com/magician336/flamingos/internal/recorder"
	"github.com/magician336/flamingos/internal/telemetry"
	"github.com/magician336/flamingos/internal/transport"
	"github.com/magician336/flamingos/internal/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// recorderLines 读回录制文件全部记录(JSON-lines 逐行反序列化)。
func recorderLines(t *testing.T, path string) []recorder.Record {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读录制文件 %s 失败: %v", path, err)
	}
	return recorderRecords(t, raw)
}

// recorderRecords 从已读原始字节解析记录(JSON-lines 逐行反序列化):
// 供需要对原始字节本身断言的用例读一次复用,不重复读盘。
func recorderRecords(t *testing.T, raw []byte) []recorder.Record {
	t.Helper()
	var lines []recorder.Record
	for i, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		var rec recorder.Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("录制第 %d 行不是合法 JSON: %v(%q)", i+1, err, line)
		}
		lines = append(lines, rec)
	}
	return lines
}

// assembledRecorder 按 cmd 装配的同款注入构造录制器:t.TempDir 下的录制
// 文件路径 + policy 的角色 ctx 键 + telemetry 的只读 trace_id 访问器。
func assembledRecorder(t *testing.T) (*recorder.Recorder, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "recording.jsonl")
	rec, err := recorder.New(recorder.Config{
		Path:      path,
		RoleKey:   policy.RoleContextKey(),
		TraceIDOf: telemetry.TraceIDFromContext,
	})
	if err != nil {
		t.Fatalf("构造录制器失败: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	return rec, path
}

// recorderHarness 是带录制接线的进程内网关:与 auditHarness 同形,
// transport.New 注入 telemetry/policy/recorder 三路;role 决定 Agent 侧
// 注入的角色头。
func recorderHarness(t *testing.T, clients map[string]upstream.Client, tel *telemetry.Telemetry,
	pol *policy.Policy, rec *recorder.Recorder, role string) *gatewayHarness {
	t.Helper()
	handler, err := transport.New(context.Background(), gateway.New(clients),
		transport.WithTelemetry(tel), transport.WithPolicy(pol), transport.WithRecorder(rec))
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

// objectTool 是带合法 object schema 的工具规格(快照注册必需)。
func objectTool(name string) *mcp.Tool {
	return &mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}
}

// fakeIsErrorClient 是恒回业务失败应答的假上游:IsError=true 且协议层成功
// (err 为 nil),对应 kind=business_failure 形态。
type fakeIsErrorClient struct{}

func (fakeIsErrorClient) ListTools(context.Context) ([]*mcp.Tool, error) {
	return []*mcp.Tool{objectTool("fail")}, nil
}

func (fakeIsErrorClient) CallTool(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "上游报业务失败"}},
		IsError: true,
	}, nil
}

func (fakeIsErrorClient) Close() error { return nil }

// fakeDeadClient 是恒报传输错误的假上游:err 形态经 gateway 包装点名上游,
// 对应 kind=transport_error 形态。
type fakeDeadClient struct{}

func (fakeDeadClient) ListTools(context.Context) ([]*mcp.Tool, error) {
	return []*mcp.Tool{objectTool("gone")}, nil
}

func (fakeDeadClient) CallTool(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error) {
	return nil, errors.New("连接被拒")
}

func (fakeDeadClient) Close() error { return nil }

// recorderTools 是成功形态夹具:calc.greet 回显原始名(fakeClient 复用)。
func recorderTools() map[string]upstream.Client {
	return map[string]upstream.Client{
		"calc": &fakeClient{tools: []*mcp.Tool{objectTool("greet")}},
		"fs":   &fakeClient{tools: []*mcp.Tool{objectTool("echo")}},
	}
}

// recorderAllowAllPolicy 是全放行规则表:序列例三种上游各自触发一种 kind。
func recorderAllowAllPolicy() *policy.Policy {
	return policy.New(policy.Config{
		RoleHeader:  "mcp-role",
		DefaultRole: "viewer",
		Roles:       map[string]policy.RoleRules{"viewer": {Allow: []string{"*"}}},
	})
}

func TestTransportRecorder(t *testing.T) {
	t.Run("正常调用逐字段成立:12 字段齐备,trace_id 与网关请求 span 同值", func(t *testing.T) {
		tel, sr := recordedTelemetry(t)
		rec, path := assembledRecorder(t)
		h := recorderHarness(t, recorderTools(), tel, viewerPolicy("mcp-role"), rec, "viewer")

		if _, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "calc_greet",
			Arguments: json.RawMessage(`{"msg":"hi"}`),
		}); err != nil {
			t.Fatalf("放行调用应成功,实际报错: %v", err)
		}

		lines := recorderLines(t, path)
		if len(lines) != 1 {
			t.Fatalf("一次调用应恰落一条记录,实际 %d 条: %+v", len(lines), lines)
		}
		got := lines[0]
		if got.V != recorder.FormatVersion {
			t.Errorf("v = %q,期望格式版本 %q", got.V, recorder.FormatVersion)
		}
		if got.Seq != 1 {
			t.Errorf("seq = %d,期望首条为 1", got.Seq)
		}
		if _, err := time.Parse(time.RFC3339Nano, got.Time); err != nil {
			t.Errorf("time = %q,应为 RFC3339Nano UTC 时刻: %v", got.Time, err)
		}
		if got.Method != "tools/call" {
			t.Errorf("method = %q,期望恒为 tools/call", got.Method)
		}
		if got.Tool != "calc_greet" {
			t.Errorf("tool = %q,期望聚合名 calc_greet", got.Tool)
		}
		if string(got.Args) != `{"msg":"hi"}` {
			t.Errorf("args = %s,期望参数原始字节经真实 SDK 往返逐字节保留", got.Args)
		}
		if got.Kind != recorder.KindSuccess {
			t.Errorf("kind = %q,期望 %q", got.Kind, recorder.KindSuccess)
		}
		if got.Error != "" {
			t.Errorf("error = %q,成功形态应为空串", got.Error)
		}
		if got.LatencyMS < 0 {
			t.Errorf("latency_ms = %d,不应为负", got.LatencyMS)
		}
		if got.Role != "viewer" {
			t.Errorf("role = %q,期望经角色提取层落 viewer", got.Role)
		}
		if !strings.Contains(string(got.Result), "greet") {
			t.Errorf("result 应含上游回显内容,实际 %s", got.Result)
		}
		// trace_id 交叉核对:链最外层开的网关请求 span 与录制条目同值——
		// 只读访问器闭包注入接缝在真实装配路径上咬合。
		span := spanFinder(t, sr)("tools/call")
		if want := span.SpanContext().TraceID().String(); got.TraceID != want {
			t.Errorf("trace_id = %q,期望与网关请求 span 同值 %q", got.TraceID, want)
		}
	})

	t.Run("业务失败:IsError 应答落 business_failure,result 记录、error 留空", func(t *testing.T) {
		tel, _ := recordedTelemetry(t)
		rec, path := assembledRecorder(t)
		h := recorderHarness(t, map[string]upstream.Client{"biz": fakeIsErrorClient{}},
			tel, recorderAllowAllPolicy(), rec, "viewer")

		res, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "biz_fail", Arguments: json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatalf("业务失败在协议层是成功应答,不应报错: %v", err)
		}
		if !res.IsError {
			t.Fatal("夹具应答应为 IsError")
		}

		lines := recorderLines(t, path)
		if len(lines) != 1 {
			t.Fatalf("应恰落一条记录,实际 %d 条", len(lines))
		}
		got := lines[0]
		if got.Kind != recorder.KindBusinessFailure {
			t.Errorf("kind = %q,期望 %q", got.Kind, recorder.KindBusinessFailure)
		}
		if got.Error != "" {
			t.Errorf("error = %q,业务失败形态应留空(IsError 语义在 result 内)", got.Error)
		}
		if !strings.Contains(string(got.Result), "上游报业务失败") {
			t.Errorf("result 应含业务失败应答内容,实际 %s", got.Result)
		}
	})

	t.Run("传输错误:err 落 transport_error,文案点名上游,result 为 null", func(t *testing.T) {
		tel, _ := recordedTelemetry(t)
		rec, path := assembledRecorder(t)
		h := recorderHarness(t, map[string]upstream.Client{"calc": fakeDeadClient{}},
			tel, recorderAllowAllPolicy(), rec, "viewer")

		if _, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "calc_gone", Arguments: json.RawMessage(`{}`),
		}); err == nil {
			t.Fatal("死上游调用应收错误,实际成功")
		}

		// 读一次复用:原始字节既做逐字段断言又做序列化形态断言,不重复读盘。
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读录制文件失败: %v", err)
		}
		lines := recorderRecords(t, raw)
		if len(lines) != 1 {
			t.Fatalf("应恰落一条记录,实际 %d 条", len(lines))
		}
		got := lines[0]
		if got.Kind != recorder.KindTransportError {
			t.Errorf("kind = %q,期望 %q", got.Kind, recorder.KindTransportError)
		}
		if !strings.Contains(got.Error, "calc") {
			t.Errorf("error 应点名上游(gateway 错误契约透传),实际 %q", got.Error)
		}
		if !strings.Contains(string(raw), `"result":null`) {
			t.Errorf("传输错误形态 result 应序列化为 null,实际行:\n%s", raw)
		}
	})

	t.Run("deny 不入录制:被拒调用不出现在录制文件(判定内层装配钉死)", func(t *testing.T) {
		tel, _ := recordedTelemetry(t)
		rec, path := assembledRecorder(t)
		h := recorderHarness(t, recorderTools(), tel, viewerPolicy("mcp-role"), rec, "viewer")

		if _, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "calc_greet", Arguments: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatalf("放行调用应成功,实际报错: %v", err)
		}
		if _, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "fs_echo", Arguments: json.RawMessage(`{}`),
		}); err == nil {
			t.Fatal("被拒调用应收错误,实际放行")
		}

		lines := recorderLines(t, path)
		if len(lines) != 1 {
			t.Fatalf("deny 不入录制:仅放行调用应落记录,实际 %d 条: %+v", len(lines), lines)
		}
		if lines[0].Tool != "calc_greet" {
			t.Errorf("唯一记录应为放行的 calc_greet,实际 %q", lines[0].Tool)
		}
	})

	t.Run("seq 严格递增且与文件行序一致:三形态依序各一例", func(t *testing.T) {
		tel, _ := recordedTelemetry(t)
		rec, path := assembledRecorder(t)
		h := recorderHarness(t, map[string]upstream.Client{
			"calc": &fakeClient{tools: []*mcp.Tool{objectTool("greet")}},
			"biz":  fakeIsErrorClient{},
			"dead": fakeDeadClient{},
		}, tel, recorderAllowAllPolicy(), rec, "viewer")

		calls := []struct {
			tool string
		}{{"calc_greet"}, {"biz_fail"}, {"dead_gone"}}
		for _, c := range calls {
			if _, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
				Name: c.tool, Arguments: json.RawMessage(`{}`),
			}); err != nil && c.tool != "dead_gone" {
				t.Fatalf("调用 %s 不应报错(dead 除外),实际: %v", c.tool, err)
			}
		}

		lines := recorderLines(t, path)
		if len(lines) != 3 {
			t.Fatalf("三次调用应恰落三条记录,实际 %d 条: %+v", len(lines), lines)
		}
		wantKinds := []string{recorder.KindSuccess, recorder.KindBusinessFailure, recorder.KindTransportError}
		for i, line := range lines {
			if line.Seq != int64(i+1) {
				t.Errorf("第 %d 行 seq = %d,期望与文件行序一致( %d )", i+1, line.Seq, i+1)
			}
			if line.Kind != wantKinds[i] {
				t.Errorf("第 %d 行 kind = %q,期望 %q", i+1, line.Kind, wantKinds[i])
			}
		}
	})

	t.Run("nil 哨兵:WithRecorder(nil) 关闭态零挂点,调用照常", func(t *testing.T) {
		tel, _ := recordedTelemetry(t)
		h := recorderHarness(t, recorderTools(), tel, viewerPolicy("mcp-role"), nil, "viewer")

		if _, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "calc_greet", Arguments: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatalf("关闭录制不应影响调用,实际报错: %v", err)
		}
	})
}
