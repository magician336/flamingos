// transport 的 SEP-414 `_meta` 通道 e2e(票 0004-02):Agent 经请求
// `_meta.traceparent` 携带 trace 上下文(协议原生通道,不经 HTTP 头)→
// 网关接续而非新根;stdio 夹具上游(复用 spec 0003 子进程夹具模式)全
// 链路:Agent → 网关(server span)→ stdio 上游(client span + 夹具
// 接收侧回显的 `_meta`),断 `_meta.traceparent` 与网关 client span
// 同 trace——stdio 无 HTTP 头,`_meta` 是它唯一的 trace 通道。
package transport_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/magician336/flamingos/internal/telemetry"
	"github.com/magician336/flamingos/internal/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// traceparentLiteral 拼出 span 上下文的 W3C traceparent 字面量(Agent
// 侧与期望值的独立出处,不经被测传播路径)。
func traceparentLiteral(sc trace.SpanContext) string {
	return "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-01"
}

// TestTracingMetaInbound:Agent 在 CallToolParams 的 `_meta` 里携带
// traceparent(SEP-414 协议原生通道,全程不出现 HTTP 头),网关请求
// span 应接续该上下文而非另起新根。
func TestTracingMetaInbound(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	agent := recordedAgentSpan(t)
	fakeTools := map[string]upstream.Client{
		"calc": &fakeClient{tools: []*mcp.Tool{
			{Name: "greet", InputSchema: map[string]any{"type": "object"}},
		}},
	}
	// agentSC 传无效值:不注入头 RoundTripper,Agent 上下文只经 `_meta` 携带。
	h := tracedHarness(t, fakeTools, telemetry.New(tp), trace.SpanContext{})

	if _, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "calc_greet",
		Arguments: json.RawMessage(`{}`),
		Meta:      mcp.Meta{"traceparent": traceparentLiteral(agent)},
	}); err != nil {
		t.Fatalf("经网关调用失败: %v", err)
	}

	span := spanFinder(t, sr)("tools/call")
	if span.SpanContext().TraceID() != agent.TraceID() {
		t.Errorf("网关 span 应接续 Agent 经 _meta 携带的 trace %s(接续而非新根),实际 %s",
			agent.TraceID(), span.SpanContext().TraceID())
	}
	if span.Parent().SpanID() != agent.SpanID() {
		t.Errorf("网关 span 父应为 Agent 的 %s,实际 %s", agent.SpanID(), span.Parent().SpanID())
	}
}

// TestTracingFullLinkStdio:stdio 夹具上游全链路——Agent 经 `_meta` 携带
// 上下文,网关 server span 接续;对 stdio 上游的转发注入 `_meta.traceparent`
// (夹具的 meta 工具在接收侧回显),与网关 client span 同 trace 且为
// server span 的 child;同一 trace 贯通 Agent → 网关 → stdio 上游。
func TestTracingFullLinkStdio(t *testing.T) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tel := telemetry.New(tp)

	fsClient, err := upstream.NewStdio(context.Background(), "fs",
		buildStdioFixture(t), nil, integrationTimeout, upstream.WithTelemetry(tel))
	if err != nil {
		t.Fatalf("构造 stdio 上游客户端失败: %v", err)
	}
	t.Cleanup(func() { _ = fsClient.Close() })

	agent := recordedAgentSpan(t)
	h := tracedHarness(t, map[string]upstream.Client{"fs": fsClient}, tel, trace.SpanContext{})

	res, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "fs_meta",
		Arguments: json.RawMessage(`{}`),
		Meta:      mcp.Meta{"traceparent": traceparentLiteral(agent)},
	})
	if err != nil {
		t.Fatalf("经网关调用 stdio 上游失败: %v", err)
	}

	// 接收侧观察:夹具回显收到的 _meta,其 traceparent 应与网关 client span
	// 的值全等(trace 与 span id 都一致)。
	var echoed map[string]any
	if err := json.Unmarshal([]byte(textOf(res)), &echoed); err != nil {
		t.Fatalf("meta 工具应回显 _meta 的 JSON,实际 %q: %v", textOf(res), err)
	}
	fromMeta, ok := echoed["traceparent"].(string)
	if !ok {
		t.Fatalf("回显的 _meta 应含字符串 traceparent,实际 %v", echoed)
	}

	spans := spansInTrace(sr, agent.TraceID())
	server := findSpan(t, spans, "tools/call", trace.SpanKindServer)
	client := findSpan(t, spans, "tools/call", trace.SpanKindClient)
	if client.Parent().SpanID() != server.SpanContext().SpanID() {
		t.Errorf("client span 父应为网关 server span(%s),实际 %s",
			server.SpanContext().SpanID(), client.Parent().SpanID())
	}
	if want := traceparentLiteral(client.SpanContext()); fromMeta != want {
		t.Errorf("stdio 上游收到的 _meta.traceparent 应与网关 client span 一致:\n期望 %q\n实际 %q(stdio 无 HTTP 头,_meta 是唯一通道)",
			want, fromMeta)
	}
	if client.SpanContext().TraceID() != agent.TraceID() {
		t.Errorf("client span 应与 Agent 同 trace %s(全链路贯通),实际 %s",
			agent.TraceID(), client.SpanContext().TraceID())
	}
}

// buildStdioFixture 用 go build 把 stdiometaserver 夹具(upstream 包
// testdata,单 meta 工具的接收侧观察面)一次性构建为临时二进制(spec
// 0003 子进程夹具模式;testdata 目录被 go 工具链的模式匹配忽略,须以
// 显式 import path 构建)。go 可执行文件优先取 PATH,PATH 无 go 时回退
// 测试二进制自带的 GOROOT;临时目录随 t.Cleanup 清理。
func buildStdioFixture(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "stdiometaserver")
	goBin, err := exec.LookPath("go")
	if err != nil {
		goBin = filepath.Join(runtime.GOROOT(), "bin", "go")
	}
	root, err := moduleRoot()
	if err != nil {
		t.Fatalf("定位模块根失败: %v", err)
	}
	cmd := exec.Command(goBin, "build", "-o", bin,
		"github.com/magician336/flamingos/internal/upstream/testdata/stdiometaserver")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("构建 stdio 夹具失败: %v\n%s", err, out)
	}
	return bin
}

// moduleRoot 定位模块根目录:测试进程的 cwd 是包目录 internal/transport,
// 上溯两级即模块根。
func moduleRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(wd, "..", ".."))
}
