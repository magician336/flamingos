// 录制中间件测试(spec 0008 票 01):经 ReceivingMiddleware 的公共接缝以桩
// next 驱动,断言 JSONL 行内容逐字段成立——成功/业务失败/传输错误三态、
// 非 tools/call 透传零录制、params 形状异常记录照落、关闭态哨兵。
package recorder_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/magician336/flamingos/internal/recorder"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// testRoleKey 是单测自持的不透明 ctx 键:recorder 只认不透明键值(沿 audit
// 依赖纪律),装配层传 policy.RoleContextKey(),单测传本键即可驱动。
type testRoleKey struct{}

// testTraceKey 是 trace_id 访问器桩的标记键:取 ctx 中标记值,缺失返回空串
// ——与 telemetry 只读访问器的形态同构(装配级测试以真实访问器再钉一次)。
type testTraceKey struct{}

func traceIDStub(ctx context.Context) string {
	id, _ := ctx.Value(testTraceKey{}).(string)
	return id
}

// newTestRecorder 构造单测录制器:临时目录下的录制文件 + 自持角色键 +
// trace_id 桩访问器(验证闭包注入接缝真实可用)。
func newTestRecorder(t *testing.T) (*recorder.Recorder, string) {
	t.Helper()
	return newTestRecorderWithConfig(t, recorder.Config{
		Path:      filepath.Join(t.TempDir(), "recording.jsonl"),
		RoleKey:   testRoleKey{},
		TraceIDOf: traceIDStub,
	})
}

// newTestRecorderWithConfig 以给定配置构造录制器,生命周期挂靠 t.Cleanup。
func newTestRecorderWithConfig(t *testing.T, cfg recorder.Config) (*recorder.Recorder, string) {
	t.Helper()
	rec, err := recorder.New(cfg)
	if err != nil {
		t.Fatalf("构造录制器失败: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	return rec, cfg.Path
}

// recordLines 读回录制文件全部记录(JSON-lines 逐行反序列化)。
func recordLines(t *testing.T, path string) []recorder.Record {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读录制文件 %s 失败: %v", path, err)
	}
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

// callToolReq 构造服务端 Receiving 链上的 tools/call 请求(SDK 对 tools/call
// 恒传 *mcp.CallToolParamsRaw,与 policy/audit 同一实测结论)。
func callToolReq(name string, args json.RawMessage) *mcp.ServerRequest[*mcp.CallToolParamsRaw] {
	return &mcp.ServerRequest[*mcp.CallToolParamsRaw]{Params: &mcp.CallToolParamsRaw{Name: name, Arguments: args}}
}

// textResult 构造单段文本的应答;isError 置 IsError(业务失败形态)。
func textResult(text string, isError bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: isError}
}

// withRoleAndTrace 把角色与 trace_id 标记放进 ctx(生产形态:HTTP 提取层写
// 角色、trace Receiving 开 span 写 trace,单测以同形键值直接写入)。
func withRoleAndTrace(ctx context.Context, role, traceID string) context.Context {
	ctx = context.WithValue(ctx, testRoleKey{}, role)
	return context.WithValue(ctx, testTraceKey{}, traceID)
}

// textOf 提取结果中第一段文本内容(与 transport 测试同款辅助)。
func textOf(res mcp.Result) string {
	ctr, ok := res.(*mcp.CallToolResult)
	if !ok {
		return "<非工具应答>"
	}
	for _, c := range ctr.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return "<非文本内容>"
}

// passthroughNext 是恒返回固定成功应答的 next 桩。
func passthroughNext(context.Context, string, mcp.Request) (mcp.Result, error) {
	return textResult("桩应答", false), nil
}

func TestReceivingMiddlewareSuccess(t *testing.T) {
	rec, path := newTestRecorder(t)
	handler := rec.ReceivingMiddleware()(passthroughNext)

	res, err := handler(withRoleAndTrace(context.Background(), "viewer", "trace-abc"),
		"tools/call", callToolReq("calc_add", json.RawMessage(`{"a":3,"b":4}`)))
	if err != nil {
		t.Fatalf("中间件不应拦截调用,实际报错: %v", err)
	}
	if textOf(res) != "桩应答" {
		t.Errorf("结果应原样透传,实际 %q", textOf(res))
	}

	lines := recordLines(t, path)
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
	if got.Tool != "calc_add" {
		t.Errorf("tool = %q,期望聚合名 calc_add", got.Tool)
	}
	if string(got.Args) != `{"a":3,"b":4}` {
		t.Errorf("args = %s,期望参数原始字节逐字节保留", got.Args)
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
	if got.TraceID != "trace-abc" {
		t.Errorf("trace_id = %q,期望经注入访问器取到 trace-abc", got.TraceID)
	}
	if got.Role != "viewer" {
		t.Errorf("role = %q,期望经注入 ctx 键取到 viewer", got.Role)
	}

	// result 字段为 SDK 应答类型的标准 JSON 序列化形态:JSON 对象,且内容
	// 文本在内(不做逐字节钉死,SDK 序列化细节不属录制格式契约)。
	var obj map[string]any
	if err := json.Unmarshal(got.Result, &obj); err != nil {
		t.Fatalf("result 应为 JSON 对象,实际解析失败: %v(%s)", err, got.Result)
	}
	if !strings.Contains(string(got.Result), "桩应答") {
		t.Errorf("result 应含应答内容文本,实际 %s", got.Result)
	}
}

func TestReceivingMiddlewareKindTriStates(t *testing.T) {
	t.Run("业务失败:IsError 应答落 business_failure,result 记录、error 留空", func(t *testing.T) {
		rec, path := newTestRecorder(t)
		next := func(context.Context, string, mcp.Request) (mcp.Result, error) {
			return textResult("上游业务失败文案", true), nil
		}
		if _, err := rec.ReceivingMiddleware()(next)(withRoleAndTrace(context.Background(), "viewer", "t"),
			"tools/call", callToolReq("calc_add", json.RawMessage(`{}`))); err != nil {
			t.Fatalf("业务失败在协议层是成功应答,不应报错: %v", err)
		}
		lines := recordLines(t, path)
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
		if !strings.Contains(string(got.Result), "上游业务失败文案") {
			t.Errorf("result 应含业务失败应答内容,实际 %s", got.Result)
		}
	})

	t.Run("传输错误:err 落 transport_error,文案入 error,result 为 null", func(t *testing.T) {
		rec, path := newTestRecorder(t)
		next := func(context.Context, string, mcp.Request) (mcp.Result, error) {
			return nil, errors.New("调用上游 calc 失败: 连接被拒")
		}
		if _, err := rec.ReceivingMiddleware()(next)(withRoleAndTrace(context.Background(), "viewer", "t"),
			"tools/call", callToolReq("calc_add", json.RawMessage(`{"a":1}`))); err == nil {
			t.Fatal("next 的错误应原样透传,实际被吞")
		}
		lines := recordLines(t, path)
		if len(lines) != 1 {
			t.Fatalf("应恰落一条记录,实际 %d 条", len(lines))
		}
		got := lines[0]
		if got.Kind != recorder.KindTransportError {
			t.Errorf("kind = %q,期望 %q", got.Kind, recorder.KindTransportError)
		}
		if !strings.Contains(got.Error, "calc") {
			t.Errorf("error 应保留传输错误文案(含上游点名),实际 %q", got.Error)
		}
		if string(got.Result) != "null" {
			t.Errorf("result = %s,传输错误形态应为 JSON null", got.Result)
		}
		if string(got.Args) != `{"a":1}` {
			t.Errorf("args = %s,传输错误形态参数字节照录", got.Args)
		}
	})
}

func TestReceivingMiddlewarePassthrough(t *testing.T) {
	t.Run("非 tools/call:其余方法原样下探零录制", func(t *testing.T) {
		rec, path := newTestRecorder(t)
		handler := rec.ReceivingMiddleware()(passthroughNext)

		for _, method := range []string{"initialize", "tools/list", "prompts/list"} {
			if _, err := handler(withRoleAndTrace(context.Background(), "viewer", "t"),
				method, callToolReq("calc_add", json.RawMessage(`{}`))); err != nil {
				t.Fatalf("method %s 应照常下探,实际报错: %v", method, err)
			}
		}
		if lines := recordLines(t, path); len(lines) != 0 {
			t.Fatalf("非 tools/call 不应产生录制记录,实际 %d 条: %+v", len(lines), lines)
		}
	})

	t.Run("params 形状异常:记录照落,tool/args 留空可查", func(t *testing.T) {
		rec, path := newTestRecorder(t)
		handler := rec.ReceivingMiddleware()(passthroughNext)

		// 非 tools/call 参数形状(mcp.CallToolParams 而非 Raw 形态):提取
		// 失配时 tool/args 留空,记录不缺位(沿 audit 的观察者纪律)。
		req := &mcp.ServerRequest[*mcp.CallToolParams]{Params: &mcp.CallToolParams{Name: "calc_add"}}
		if _, err := handler(withRoleAndTrace(context.Background(), "", "t"),
			"tools/call", req); err != nil {
			t.Fatalf("形状异常不应反噬调用,实际报错: %v", err)
		}
		lines := recordLines(t, path)
		if len(lines) != 1 {
			t.Fatalf("记录应照落,实际 %d 条", len(lines))
		}
		if lines[0].Tool != "" || string(lines[0].Args) != "null" {
			t.Errorf("形状异常时 tool/args 应留空(null),实际 tool=%q args=%s", lines[0].Tool, lines[0].Args)
		}
		if lines[0].Role != "" {
			t.Errorf("ctx 无角色键时 role 应留空,实际 %q", lines[0].Role)
		}
	})
}

func TestReceivingMiddlewareNilSentinel(t *testing.T) {
	var rec *recorder.Recorder
	if mw := rec.ReceivingMiddleware(); mw != nil {
		t.Fatal("nil 哨兵应返回 nil 中间件(关闭态零挂点),实际非 nil")
	}
	rec.Append(recorder.Record{}) // 关闭态落盘为 no-op,不 panic
	if err := rec.Close(); err != nil {
		t.Errorf("nil 哨兵 Close 应为 no-op,实际报错: %v", err)
	}
}

func TestAppendSeqMatchesLineOrderUnderConcurrency(t *testing.T) {
	const calls = 64
	rec, path := newTestRecorder(t)
	handler := rec.ReceivingMiddleware()(passthroughNext)

	var wg sync.WaitGroup
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 并发共用同一写盘器(服务端并发调用的生产形态),耗时自然
			// 摊入 latency_ms。
			_, _ = handler(withRoleAndTrace(context.Background(), "viewer", "t"),
				"tools/call", callToolReq("calc_add", json.RawMessage(`{"n":1}`)))
		}()
	}
	wg.Wait()

	lines := recordLines(t, path)
	if len(lines) != calls {
		t.Fatalf("并发 %d 次调用应恰落 %d 条记录,实际 %d 条", calls, calls, len(lines))
	}
	seen := make(map[int64]bool, calls)
	for i, line := range lines {
		if line.Seq != int64(i+1) {
			t.Fatalf("第 %d 行 seq = %d,seq 分配必须与文件行序一致(期望 %d)", i+1, line.Seq, i+1)
		}
		if seen[line.Seq] {
			t.Fatalf("seq %d 重复", line.Seq)
		}
		seen[line.Seq] = true
	}
}

func TestNewFailsFastOnUnwritablePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "recording.jsonl")
	rec, err := recorder.New(recorder.Config{Path: path})
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
