// transport 的 Receiving 链顺序定点不变量(2026-11-05 架构评审 C2):链顺序
// 是调用者必须知道的 interface 约束,此前只活在 receivingChain 的注释与 e2e
// 反例里。本文件用真实三件套(trace/policy/audit)装配 receivingChain 的
// 产物,并在相邻中间件之间插入探针——探针依次触发的顺序就是中间件的执行
// 顺序,某个中间件不下探 next,其后的探针与 final 全部失联,顺序违约当场
// 暴露。钉住四件事:①切片顺序=执行顺序(trace→桥→审计→RBAC);②审计在
// RBAC 判定外层(deny 也落盘);③桥的预写决策与判定同口径(审计记的是
// deny 而非缺省 allow);④拒绝落 span 标错。W3 在链上插路时只跑这里。
package transport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/magician336/flamingos/internal/audit"
	"github.com/magician336/flamingos/internal/policy"
	"github.com/magician336/flamingos/internal/telemetry"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// chainFixture 装配真实三件套的接收链:返回链切片、SpanRecorder 与审计
// 文件路径(viewer 放行 calc_ 前缀,其余 fail-close)。
func chainFixture(t *testing.T) ([]mcp.Middleware, *tracetest.SpanRecorder, string) {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tel := telemetry.New(tp)

	pol := policy.New(policy.Config{
		RoleHeader:  "mcp-role",
		DefaultRole: "viewer",
		Roles:       map[string]policy.RoleRules{"viewer": {Allow: []string{"calc_*"}}},
	})
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
	return receivingChain(options{telemetry: tel, policy: pol, audit: rec}), sr, path
}

// composedWithProbes 把链切片按 SDK 语义组装(首参最外)并在相邻中间件之间
// 插探针;期望顺序 = 三个边界探针依次触发,最后到 final。
func composedWithProbes(t *testing.T, mws []mcp.Middleware, order *[]string) mcp.MethodHandler {
	t.Helper()
	if len(mws) != 4 {
		t.Fatalf("Receiving 链应有 4 个中间件(trace/桥/审计/RBAC),实际 %d", len(mws))
	}
	probe := func(name string, next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			*order = append(*order, name)
			return next(ctx, method, req)
		}
	}
	cur := mcp.MethodHandler(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		*order = append(*order, "final")
		return &mcp.CallToolResult{}, nil
	})
	boundaries := []string{"p01", "p12", "p23"} // 边界 mw[i-1]→mw[i],探针在 mw[i] 外层
	for i := len(mws) - 1; i >= 0; i-- {
		cur = mws[i](cur)
		if i > 0 {
			cur = probe(boundaries[i-1], cur)
		}
	}
	return cur
}

func TestReceivingChainOrder(t *testing.T) {
	call := func(t *testing.T, tool string) ([]string, error, *tracetest.SpanRecorder, string) {
		mws, sr, auditPath := chainFixture(t)
		var order []string
		h := composedWithProbes(t, mws, &order)
		ctx := policy.WithRole(context.Background(), "viewer")
		req := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: tool}}
		_, err := h(ctx, "tools/call", req)
		return order, err, sr, auditPath
	}

	t.Run("allow 路径:三个边界探针依次触发,到 final", func(t *testing.T) {
		order, err, _, _ := call(t, "calc_greet")
		if err != nil {
			t.Fatalf("allow 调用应成功,实际报错: %v", err)
		}
		want := []string{"p01", "p12", "p23", "final"}
		if strings.Join(order, ",") != strings.Join(want, ",") {
			t.Fatalf("链执行顺序违约:\n期望 %v\n实际 %v", want, order)
		}
	})

	t.Run("deny 路径:RBAC 不下探 final,审计仍落 deny,span 标错", func(t *testing.T) {
		order, err, sr, auditPath := call(t, "fs_echo")
		want := []string{"p01", "p12", "p23"} // p23 之后 RBAC 拒绝,final 失联
		if strings.Join(order, ",") != strings.Join(want, ",") {
			t.Fatalf("deny 执行顺序违约:\n期望 %v\n实际 %v", want, order)
		}
		var rpcErr *jsonrpc.Error
		if !errors.As(err, &rpcErr) || rpcErr.Code != -40001 {
			t.Fatalf("deny 应返回 JSON-RPC -40001,实际 %v", err)
		}
		raw, readErr := os.ReadFile(auditPath)
		if readErr != nil {
			t.Fatalf("读审计文件失败: %v", readErr)
		}
		content := string(raw)
		if !strings.Contains(content, "fs_echo") || !strings.Contains(content, "deny") {
			t.Errorf("审计应记录 deny 的 fs_echo(审计在判定外层、桥预写同口径),实际:\n%s", content)
		}
		var denySpan bool
		for _, s := range sr.Ended() {
			if s.Name() == "tools/call" && s.Status().Code == codes.Error {
				denySpan = true
			}
		}
		if !denySpan {
			t.Error("deny 调用应有网关请求 span 且标注错误")
		}
	})
}
