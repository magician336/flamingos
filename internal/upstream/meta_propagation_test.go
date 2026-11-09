// upstream 的 `_meta` 出站传播黑盒测试(票 0004-02):真实 adapter +
// WithTelemetry 后,上游接收侧观察到的 `_meta.traceparent` 与网关对
// 该上游的 client span 同 trace 同 span。stdio 上游对照真实子进程夹具
// (testdata/stdioserver 的 meta 工具即接收侧观察面)——stdio 无 HTTP
// 头,`_meta` 是唯一通道;HTTP 上游另断 `_meta` 与标准头双通道并存、
// 同一 child 上下文(spec 0004 Story 6/7)。
package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// headerCapture 捕获假上游收到的全部 traceparent 头(黑盒观察面)。
type headerCapture struct {
	mu     sync.Mutex
	values []string
}

func (h *headerCapture) add(v string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.values = append(h.values, v)
}

func (h *headerCapture) all() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.values...)
}

// newObservedFakeUpstream 起带观察面的进程内 HTTP 假上游:meta 工具回显
// 请求 `_meta`,外层中间层捕获每个入站请求的 traceparent 头——双通道
// 并存断言的两个观察面。返回地址与观察面。
func newObservedFakeUpstream(t *testing.T) (string, *headerCapture) {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-upstream", Version: "0.0.1"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "meta",
		Description: "回显收到的请求 _meta(观察面)",
		InputSchema: map[string]any{"type": "object"},
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		raw, err := json.Marshal(req.Params.Meta)
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}, nil
	})
	mux := http.NewServeMux()
	mux.Handle("/", mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	))
	capture := &headerCapture{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.add(r.Header.Get("traceparent"))
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts.URL, capture
}

// metaTraceparentOf 从 meta 工具的回显 JSON 中取 _meta.traceparent 字符串。
func metaTraceparentOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var meta map[string]any
	if err := json.Unmarshal([]byte(textOf(res)), &meta); err != nil {
		t.Fatalf("meta 工具应回显 _meta 的 JSON,实际 %q: %v", textOf(res), err)
	}
	v, ok := meta["traceparent"].(string)
	if !ok {
		t.Fatalf("回显的 _meta 应含字符串 traceparent,实际 %v", meta)
	}
	return v
}

// 夹具二进制每测试进程只构建一次:二进制复用,子进程由各测试的客户端
// 自行拉起;构建结果与错误经 once 收敛,首个使用者失败全包同错。
var (
	metaFixtureOnce sync.Once
	metaFixtureBin  string
	metaFixtureErr  error
)

// metaFixture 返回 stdiometaserver 夹具二进制路径,构建失败即 t.Fatal。
func metaFixture(t *testing.T) string {
	t.Helper()
	metaFixtureOnce.Do(func() {
		dir, err := os.MkdirTemp("", "flamingos-meta-fixture-")
		if err != nil {
			metaFixtureErr = err
			return
		}
		goBin, err := exec.LookPath("go")
		if err != nil {
			goBin = filepath.Join(runtime.GOROOT(), "bin", "go")
		}
		bin := filepath.Join(dir, "stdiometaserver")
		cmd := exec.Command(goBin, "build", "-o", bin, "./testdata/stdiometaserver")
		if out, err := cmd.CombinedOutput(); err != nil {
			metaFixtureErr = fmt.Errorf("%s: %w\n%s", cmd.String(), err, out)
			return
		}
		metaFixtureBin = bin
	})
	if metaFixtureErr != nil {
		t.Fatalf("构建 stdiometaserver 夹具失败: %v", metaFixtureErr)
	}
	return metaFixtureBin
}

func TestStdioMetaPropagation(t *testing.T) {
	t.Run("stdio 接收侧:meta 工具回显的 _meta.traceparent 与 client span 同 trace 同 span", func(t *testing.T) {
		tel, sr := newRecordingTelemetry(t)
		client, err := NewStdio(context.Background(), "fs", metaFixture(t), nil, testTimeout, WithTelemetry(tel))
		if err != nil {
			t.Fatalf("构造 stdio 客户端失败: %v", err)
		}
		defer func() { _ = client.Close() }()

		res, err := client.CallTool(context.Background(), "meta", json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("调用应照常成功,实际报错: %v", err)
		}

		span := clientSpanByName(t, sr, "tools/call")
		if got := metaTraceparentOf(t, res); got != spanContextTraceparent(span) {
			t.Errorf("stdio 上游收到的 _meta.traceparent 应与 client span 一致:\n期望 %q\n实际 %q(stdio 无 HTTP 头,_meta 是唯一通道)",
				spanContextTraceparent(span), got)
		}
	})
}

func TestHTTPMetaDualChannel(t *testing.T) {
	t.Run("HTTP 双通道并存:_meta.traceparent 与标准头同值,且与 client span 一致", func(t *testing.T) {
		url, capture := newObservedFakeUpstream(t)
		tel, sr := newRecordingTelemetry(t)
		client, err := NewHTTP(context.Background(), "calc", url, testTimeout, WithTelemetry(tel))
		if err != nil {
			t.Fatalf("构造 HTTP 客户端失败: %v", err)
		}
		defer func() { _ = client.Close() }()

		res, err := client.CallTool(context.Background(), "meta", json.RawMessage(`{}`))
		if err != nil {
			t.Fatalf("调用应照常成功,实际报错: %v", err)
		}

		span := clientSpanByName(t, sr, "tools/call")
		want := spanContextTraceparent(span)
		fromMeta := metaTraceparentOf(t, res)
		if fromMeta != want {
			t.Errorf("上游收到的 _meta.traceparent 应与 client span 一致:\n期望 %q\n实际 %q", want, fromMeta)
		}
		headerMatched := false
		for _, got := range capture.all() {
			if got == want {
				headerMatched = true
				break
			}
		}
		if !headerMatched {
			t.Errorf("上游收到的标准头应与同一 client span 一致(双通道并存):\n期望 %q\n实际捕获 %v", want, capture.all())
		}
	})
}

// spanContextTraceparent 拼出 span 上下文的 W3C traceparent 字面量
// (期望值独立出处:由录制的 span 上下文拼出,非经被测注入路径)。
func spanContextTraceparent(span sdktrace.ReadOnlySpan) string {
	sc := span.SpanContext()
	return "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-01"
}
