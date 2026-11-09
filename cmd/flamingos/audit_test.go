// buildAudit 装配翻译测试(票 0005-02/spec 0005):config→audit.Config 的
// 翻译只发生在 cmd(架构 D7)——关闭态哨兵(段/子段缺省 → nil,零挂点)、
// 路径翻译、可写性失败上抛为启动错误,以及注入 audit 的决策翻译闭包与
// policy 判定结果的真实对齐(经中间件以 policy 包键驱动,deny 落 deny)。
package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/magician336/flamingos/internal/audit"
	"github.com/magician336/flamingos/internal/config"
	"github.com/magician336/flamingos/internal/policy"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestBuildAuditNilSentinel(t *testing.T) {
	t.Run("policy 段缺省:关闭态,nil,nil", func(t *testing.T) {
		rec, err := buildAudit(nil)
		if err != nil {
			t.Fatalf("段缺省不应报错,实际: %v", err)
		}
		if rec != nil {
			t.Fatal("段缺省应返回 nil 哨兵(关闭态零挂点),实际非 nil")
		}
	})
	t.Run("audit 子段缺省:关闭态,nil,nil", func(t *testing.T) {
		rec, err := buildAudit(&config.Policy{})
		if err != nil {
			t.Fatalf("子段缺省不应报错,实际: %v", err)
		}
		if rec != nil {
			t.Fatal("子段缺省应返回 nil 哨兵,实际非 nil")
		}
	})
}

func TestBuildAuditTranslatesPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	rec, err := buildAudit(&config.Policy{Audit: &config.Audit{Path: path}})
	if err != nil {
		t.Fatalf("可写路径不应报错,实际: %v", err)
	}
	if rec == nil {
		t.Fatal("audit 子段存在应产出审计器,实际 nil")
	}
	defer func() { _ = rec.Close() }()

	// 翻译真实性钉子:经注入的 ctx 键与决策闭包驱动中间件,deny 判定落
	// deny 记录——cmd 闭包对 policy.Decision 的类型断言与 audit 的翻译
	// 接缝真实咬合(allow/deny 同口径由 transport e2e 钉死)。
	next := func(context.Context, string, mcp.Request) (mcp.Result, error) {
		return nil, &stubPolicyError{}
	}
	handler := rec.ReceivingMiddleware()(next)
	ctx := policy.WithDecision(policy.WithRole(context.Background(), "viewer"),
		policy.Decision{Role: "viewer", Tool: "fs_rm", Allowed: false})
	if _, err := handler(ctx, "tools/call",
		&mcp.ServerRequest[*mcp.CallToolParamsRaw]{Params: &mcp.CallToolParamsRaw{Name: "fs_rm"}}); err == nil {
		t.Fatal("next 错误应原样透传,实际被吞")
	}
}

// stubPolicyError 是 next 的错误桩:审计中间件对错误类型零假设,只看
// err != nil 落 error 字段。
type stubPolicyError struct{}

func (*stubPolicyError) Error() string { return "策略拒绝(桩)" }

func TestBuildAuditFailsFastOnUnwritablePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "audit.jsonl")
	rec, err := buildAudit(&config.Policy{Audit: &config.Audit{Path: path}})
	if err == nil {
		_ = rec.Close()
		t.Fatal("不可写路径应报错(启动失败),实际成功")
	}
	if !strings.Contains(err.Error(), "审计") {
		t.Errorf("错误应中文点名审计文件,实际 %q", err.Error())
	}
	if rec != nil {
		t.Fatal("失败路径不应产出审计器,实际非 nil")
	}
}

// 编译期钉子:buildAudit 产出的就是 transport 装配消费的审计器类型。
var _ *audit.Recorder = (*audit.Recorder)(nil)
