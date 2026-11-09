// audit 审计中间件测试:接缝在 mcp.Middleware 本身——手工装配 next 桩,
// 以 &mcp.ServerRequest 驱动真实中间件内行为(与 policy 判定中间件测试
// 同构;机器类型不跨出业务包的纪律对测试同样适用)。角色与决策键由构造
// 注入,测试以自持桩键驱动真实取值路径,证明本包不依赖任何业务包。
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 桩 ctx 键与决策值类型:与 policy 包的专用 key 类型同构(装配层经 any
// 注入,audit 不认识 policy.Decision 的具体形状)。
type stubKey int

const (
	stubRoleKey     stubKey = iota + 1
	stubDecisionKey         // 决策值桩:bool,true=放行
)

// withStubRole/withStubDecision 把角色/决策放进 ctx,模拟 transport 提取层
// 与 policy 判定层的写入。
func withStubRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, stubRoleKey, role)
}

func withStubDecision(ctx context.Context, allowed bool) context.Context {
	return context.WithValue(ctx, stubDecisionKey, allowed)
}

// stubNext 是中间件的 next 桩:记录下探次数,allow 返回哨兵结果,deny
// 返回 JSON-RPC 错误(模拟 policy 拒绝路径——被拒时 next 不再下探)。
type stubNext struct {
	calls int
}

func (s *stubNext) handle(_ context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
	s.calls++
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "透传"}}}, nil
}

func (s *stubNext) handleDenied(_ context.Context, _ string, _ mcp.Request) (mcp.Result, error) {
	s.calls++
	return nil, &jsonrpc.Error{Code: -40001, Message: "策略拒绝(桩)"}
}

// newAuditHandler 构造注入桩键的写盘器与套上桩的中间件 handler。
func newAuditHandler(t *testing.T, next mcp.MethodHandler) (*Recorder, mcp.MethodHandler) {
	t.Helper()
	rec, err := New(Config{
		Path:            filepath.Join(t.TempDir(), "audit.jsonl"),
		RoleKey:         stubRoleKey,
		DecisionKey:     stubDecisionKey,
		DecisionAllowed: func(v any) bool { allowed, _ := v.(bool); return allowed },
	})
	if err != nil {
		t.Fatalf("构造写盘器失败: %v", err)
	}
	return rec, rec.ReceivingMiddleware()(next)
}

// toolReq 构造 tools/call 的 ServerRequest(服务端 Receiving 链路上 SDK 对
// tools/call 恒传 *mcp.CallToolParamsRaw,与 policy 包实测结论一致)。
func toolReq(tool string) mcp.Request {
	return &mcp.ServerRequest[*mcp.CallToolParamsRaw]{Params: &mcp.CallToolParamsRaw{Name: tool}}
}

// linesOf 关闭写盘器并读回全部审计行(关闭保证测试看到完整落盘)。
func linesOf(t *testing.T, rec *Recorder) []Record {
	t.Helper()
	if err := rec.Close(); err != nil {
		t.Fatalf("关闭写盘器失败: %v", err)
	}
	raw, err := os.ReadFile(rec.f.Name())
	if err != nil {
		t.Fatalf("读审计文件失败: %v", err)
	}
	text := strings.TrimSuffix(string(raw), "\n")
	if text == "" {
		return nil
	}
	var lines []Record
	for i, line := range strings.Split(text, "\n") {
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("第 %d 行不是合法 JSON 对象: %v(%q)", i+1, err, line)
		}
		lines = append(lines, rec)
	}
	return lines
}

func TestReceivingMiddlewareNilSentinel(t *testing.T) {
	var rec *Recorder
	if mw := rec.ReceivingMiddleware(); mw != nil {
		t.Fatal("nil 哨兵应返回 nil 中间件(关闭态零挂点),实际非 nil")
	}
}

func TestReceivingMiddlewareAllow(t *testing.T) {
	next := &stubNext{}
	rec, handler := newAuditHandler(t, next.handle)
	ctx := withStubDecision(withStubRole(context.Background(), "viewer"), true)

	res, err := handler(ctx, "tools/call", toolReq("calc_greet"))
	if err != nil {
		t.Fatalf("审计中间件不应拦截放行调用,实际报错: %v", err)
	}
	if res == nil {
		t.Fatal("next 的结果应原样透传,实际为 nil")
	}
	if next.calls != 1 {
		t.Errorf("审计中间件只观察不拦截,next 应被调用 1 次,实际 %d 次", next.calls)
	}

	lines := linesOf(t, rec)
	if len(lines) != 1 {
		t.Fatalf("应恰一条审计记录,实际 %d 条", len(lines))
	}
	want := Record{TS: lines[0].TS, Role: "viewer", Tool: "calc_greet", Decision: DecisionAllow, Error: false}
	if lines[0] != want {
		t.Errorf("审计记录 = %+v,期望 %+v", lines[0], want)
	}
}

func TestReceivingMiddlewareDeny(t *testing.T) {
	next := &stubNext{}
	rec, handler := newAuditHandler(t, next.handleDenied)
	ctx := withStubDecision(withStubRole(context.Background(), "viewer"), false)

	_, err := handler(ctx, "tools/call", toolReq("fs_rm"))
	if err == nil {
		t.Fatal("policy 拒绝的错误应原样透传,实际被吞")
	}
	var wire *jsonrpc.Error
	if !errors.As(err, &wire) || wire.Code != -40001 {
		t.Errorf("拒绝错误应原样透传为 JSON-RPC -40001,实际 %T: %v", err, err)
	}
	if next.calls != 1 {
		t.Errorf("审计中间件只观察不拦截,next 应被调用 1 次,实际 %d 次", next.calls)
	}

	lines := linesOf(t, rec)
	if len(lines) != 1 {
		t.Fatalf("deny 调用应恰一条审计记录,实际 %d 条", len(lines))
	}
	want := Record{TS: lines[0].TS, Role: "viewer", Tool: "fs_rm", Decision: DecisionDeny, Error: true}
	if lines[0] != want {
		t.Errorf("审计记录 = %+v,期望 %+v", lines[0], want)
	}
}

func TestReceivingMiddlewareOnlyToolsCall(t *testing.T) {
	next := &stubNext{}
	rec, handler := newAuditHandler(t, next.handle)
	ctx := withStubDecision(withStubRole(context.Background(), "viewer"), true)

	if _, err := handler(ctx, "tools/list", &mcp.ServerRequest[*mcp.ListToolsParams]{}); err != nil {
		t.Fatalf("非 tools/call 应原样放行,实际报错: %v", err)
	}
	if lines := linesOf(t, rec); len(lines) != 0 {
		t.Errorf("tools/list 不产生审计记录,实际 %d 条: %+v", len(lines), lines)
	}
}

func TestReceivingMiddlewareNoDecisionRecordsAllow(t *testing.T) {
	// RBAC 关闭、审计开启的组合:ctx 没有决策(判定中间件未安装),不存在
	// 拒绝,按 allow 记录——审计如实反映"未被判定即放行"的关闭态语义。
	next := &stubNext{}
	rec, handler := newAuditHandler(t, next.handle)
	ctx := withStubRole(context.Background(), "viewer")

	if _, err := handler(ctx, "tools/call", toolReq("calc_greet")); err != nil {
		t.Fatalf("无决策的调用应照常放行,实际报错: %v", err)
	}
	lines := linesOf(t, rec)
	if len(lines) != 1 {
		t.Fatalf("应恰一条审计记录,实际 %d 条", len(lines))
	}
	if lines[0].Decision != DecisionAllow {
		t.Errorf("无决策应按 allow 记录,实际 %q", lines[0].Decision)
	}
	if lines[0].Role != "viewer" || lines[0].Tool != "calc_greet" {
		t.Errorf("角色/工具应照常记录,实际 %+v", lines[0])
	}
}

func TestToolNameOfMalformedParams(t *testing.T) {
	// params 形状异常(非 tools/call 的 params 类型)返回空串:审计照落、
	// 工具名字段留空可查,不 panic。
	if got := toolNameOf(&mcp.ServerRequest[*mcp.ListToolsParams]{}); got != "" {
		t.Errorf("异常 params 形状应返回空串,实际 %q", got)
	}
}

func TestAppendAfterCloseDoesNotPanic(t *testing.T) {
	// 写盘失败不反噬数据面的执行口径:停机关闭后的迟到写入(理论不可达,
	// 防御分支)只记日志,不 panic、不外抛。
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	rec, err := New(Config{Path: path})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	rec.Append(Record{Role: "viewer", Tool: "calc_greet", Decision: DecisionAllow})
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读审计文件失败: %v", err)
	}
	if len(raw) != 0 {
		t.Errorf("关闭后的追加不应落盘,实际 %d 字节", len(raw))
	}
}
