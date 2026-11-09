// buildRecorder 装配翻译测试(spec 0008 票 01):config→recorder.Config 的
// 翻译只发生在 cmd(架构 D7)——关闭态哨兵(段缺省 → nil,零挂点)、路径
// 翻译、可写性失败上抛为启动错误,以及注入 recorder 的两路访问器闭包与
// policy/telemetry 真实咬合(经中间件以 policy 角色键 + 真实 span ctx 驱动,
// trace_id/role 落位)。沿 buildAudit 测试先例同构。
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/magician336/flamingos/internal/config"
	"github.com/magician336/flamingos/internal/policy"
	"github.com/magician336/flamingos/internal/recorder"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestBuildRecorderNilSentinel(t *testing.T) {
	t.Run("recorder 段缺省:关闭态,nil,nil", func(t *testing.T) {
		rec, err := buildRecorder(nil)
		if err != nil {
			t.Fatalf("段缺省不应报错,实际: %v", err)
		}
		if rec != nil {
			t.Fatal("段缺省应返回 nil 哨兵(关闭态零挂点),实际非 nil")
		}
	})
}

func TestBuildRecorderTranslatesPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recording.jsonl")
	rec, err := buildRecorder(&config.Recorder{Path: path})
	if err != nil {
		t.Fatalf("可写路径不应报错,实际: %v", err)
	}
	if rec == nil {
		t.Fatal("recorder 段存在应产出录制器,实际 nil")
	}
	defer func() { _ = rec.Close() }()

	// 翻译真实性钉子:经注入的角色 ctx 键与 telemetry 只读访问器驱动中间件,
	// ctx 带真实 recording span 时 trace_id 落位——cmd 闭包对 policy/
	// telemetry 的注入与 recorder 的翻译接缝真实咬合(allow/deny 挂点位置
	// 由 transport 装配测试钉死)。
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tracer := tp.Tracer("buildRecorder-test")
	spanCtx, span := tracer.Start(context.Background(), "tools/call")
	defer span.End()
	ctx := policy.WithRole(spanCtx, "viewer")

	next := func(context.Context, string, mcp.Request) (mcp.Result, error) {
		return &mcp.CallToolResult{}, nil
	}
	handler := rec.ReceivingMiddleware()(next)
	if _, err := handler(ctx, "tools/call",
		&mcp.ServerRequest[*mcp.CallToolParamsRaw]{Params: &mcp.CallToolParamsRaw{
			Name: "calc_add", Arguments: json.RawMessage(`{"a":1}`),
		}}); err != nil {
		t.Fatalf("next 应原样透传,实际报错: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读录制文件失败: %v", err)
	}
	line := strings.TrimSpace(string(raw))
	if !strings.Contains(line, `"trace_id":"`+span.SpanContext().TraceID().String()+`"`) {
		t.Errorf("录制行应含注入访问器取到的 trace_id,实际:\n%s", line)
	}
	if !strings.Contains(line, `"role":"viewer"`) {
		t.Errorf("录制行应含经 policy ctx 键取到的角色,实际:\n%s", line)
	}
}

func TestBuildRecorderFailsFastOnUnwritablePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "recording.jsonl")
	rec, err := buildRecorder(&config.Recorder{Path: path})
	if err == nil {
		_ = rec.Close()
		t.Fatal("不可写路径应报错(启动失败),实际成功")
	}
	if !strings.Contains(err.Error(), "录制") {
		t.Errorf("错误应中文点名录制文件,实际 %q", err.Error())
	}
	if rec != nil {
		t.Fatal("失败路径不应产出录制器,实际非 nil")
	}
}

// 编译期钉子:buildRecorder 的签名即装配翻译接缝(config 段指针入、录制
// 器与启动错误出)——签名漂移(如改成多段入参或吞错误)在编译期即被抓住。
var _ func(*config.Recorder) (*recorder.Recorder, error) = buildRecorder
