// evalx CLI 翻译层测试:退出码三态契约、flag 解析(header 可重复)、
// go-sdk client 适配三态与 header 注入、replay 端到端(httptest 零外网)。
// 对真网关进程的端到端契约验证归票 03 的 e2e family,本文件只覆盖 CLI
// 翻译层(票面分工)。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/magician336/flamingos/internal/evalx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// seenHeaders 是线程安全的请求头记录器(httptest 夹具收集 --header 注入)。
type seenHeaders struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *seenHeaders) record(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range []string{"x-role", "x-tenant"} {
		if v := r.Header.Get(k); v != "" {
			s.m[k] = v
		}
	}
}

func (s *seenHeaders) get(k string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[k]
}

// newTestGateway 起 httptest 夹具网关:echo(恒成功回 echoText)、biz_fail
// (IsError=true)、die(handler 报错,文案点名上游锚点);返回端点与
// 请求头记录器。
func newTestGateway(t *testing.T, echoText string) (string, *seenHeaders) {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "evalx-test-gw", Version: "0.0.0"}, nil)
	objSchema := &jsonschema.Schema{Type: "object"}
	srv.AddTool(&mcp.Tool{Name: "calc_greet", InputSchema: objSchema}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: echoText}}}, nil
	})
	srv.AddTool(&mcp.Tool{Name: "biz_fail", InputSchema: objSchema}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "除零"}}}, nil
	})
	srv.AddTool(&mcp.Tool{Name: "die", InputSchema: objSchema}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, errors.New(`上游 "calc" 不可用: 测试炸了`)
	})
	seen := &seenHeaders{m: map[string]string{}}
	// handler 构造一次全程复用:会话存储在实例内,每请求新建会使
	// notifications/initialized 找不到 initialize 建立的会话。
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.record(r)
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts.URL, seen
}

// writeRecording 写临时录制文件(单行,JSON-lines 用例)。
func writeRecording(t *testing.T, line string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "recording.jsonl")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("写录制夹具失败: %v", err)
	}
	return path
}

// successLine 构造一条成功录制行(result 与夹具网关 echo 工具的应答序列化
// 形态一致——适配层与录制端同库同类型同序列化)。
func successLine(t *testing.T, tool, text string) string {
	t.Helper()
	result, err := json.Marshal(&mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}})
	if err != nil {
		t.Fatalf("构造应答形态失败: %v", err)
	}
	return `{"v":"1","seq":1,"time":"2026-11-08T00:00:00Z","method":"tools/call","tool":"` + tool +
		`","args":{"name":"ada"},"kind":"success","result":` + string(result) + `,"latency_ms":3}`
}

func TestExitCode三态契约(t *testing.T) {
	okRep := &evalx.Report{Total: 2, Passed: 2}
	badRep := &evalx.Report{Total: 2, Passed: 1, Failures: []evalx.Failure{{Seq: 1, Grader: evalx.GraderResultEqual}}}
	tests := []struct {
		name string
		err  error
		rep  *evalx.Report
		want int
	}{
		{"全过=0", nil, okRep, exitOK},
		{"存在断言失败=1", nil, badRep, exitAssertionFailed},
		{"自身运行错误=2(文件不可读/端点不可达)", errors.New("录制文件 x 无法读取"), nil, exitRuntimeError},
		{"运行错误优先于报告状态", errors.New("端点不可达"), okRep, exitRuntimeError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCode(tc.err, tc.rep); got != tc.want {
				t.Errorf("exitCode 应为 %d,实际 %d", tc.want, got)
			}
		})
	}
}

func TestParseHeaders(t *testing.T) {
	t.Run("可重复与同名后值覆盖", func(t *testing.T) {
		h, err := parseHeaders([]string{"x-role:admin", "x-tenant:acme", "x-role:viewer"})
		if err != nil {
			t.Fatalf("合法 header 应解析成功: %v", err)
		}
		if h["x-role"] != "viewer" || h["x-tenant"] != "acme" {
			t.Errorf("解析结果不符: %v", h)
		}
	})
	t.Run("值含冒号按首个冒号拆分", func(t *testing.T) {
		h, err := parseHeaders([]string{"authorization:bearer x:y"})
		if err != nil || h["authorization"] != "bearer x:y" {
			t.Errorf("首冒号拆分应保留值内冒号,实际 %v (err=%v)", h, err)
		}
	})
	t.Run("坏格式:缺冒号即错误", func(t *testing.T) {
		if _, err := parseHeaders([]string{"x-role-admin"}); err == nil {
			t.Error("缺冒号应报错误")
		}
	})
	t.Run("坏格式:空名即错误", func(t *testing.T) {
		if _, err := parseHeaders([]string{":admin"}); err == nil {
			t.Error("空名应报错误")
		}
	})
	t.Run("空收集得零值表", func(t *testing.T) {
		h, err := parseHeaders(nil)
		if err != nil || len(h) != 0 {
			t.Errorf("空收集应为空表,实际 %v (err=%v)", h, err)
		}
	})
}

func TestParseFlags与ValidateOptions(t *testing.T) {
	t.Run("全 flags 解析", func(t *testing.T) {
		opts, jsonOut, err := parseFlags([]string{
			"-file", "rec.jsonl", "-endpoint", "http://127.0.0.1:8080/mcp",
			"-header", "x-role:admin", "-header", "x-tenant:acme", "-json",
		})
		if err != nil {
			t.Fatalf("合法 flags 应解析成功: %v", err)
		}
		if opts.file != "rec.jsonl" || opts.endpoint != "http://127.0.0.1:8080/mcp" {
			t.Errorf("file/endpoint 解析不符: %+v", opts)
		}
		if !jsonOut {
			t.Error("-json 应置位")
		}
		if opts.headers["x-role"] != "admin" || opts.headers["x-tenant"] != "acme" {
			t.Errorf("--header 应可重复解析,实际 %v", opts.headers)
		}
	})
	t.Run("缺省:表格形态", func(t *testing.T) {
		_, jsonOut, err := parseFlags([]string{"-file", "a", "-endpoint", "b"})
		if err != nil || jsonOut {
			t.Errorf("无 -json 应默认表格,err=%v", err)
		}
	})
	t.Run("validateOptions:缺 file 与缺 endpoint 各自点名", func(t *testing.T) {
		if err := validateOptions(options{endpoint: "http://x"}); err == nil || !strings.Contains(err.Error(), "-file") {
			t.Errorf("缺 file 应点名,实际 %v", err)
		}
		if err := validateOptions(options{file: "a.jsonl"}); err == nil || !strings.Contains(err.Error(), "-endpoint") {
			t.Errorf("缺 endpoint 应点名,实际 %v", err)
		}
	})
}

// TestSDKCaller适配三态:go-sdk 应答翻译为引擎吃的 JSON 形态(成功/业务
// 失败/传输错误)与 --header 注入(经 HTTP 层,服务端实测收到)。
func TestSDKCaller适配三态(t *testing.T) {
	endpoint, seen := newTestGateway(t, "hi ada")
	ctx := context.Background()
	caller, err := connectCaller(ctx, endpoint, map[string]string{"x-role": "admin"})
	if err != nil {
		t.Fatalf("连接夹具网关失败: %v", err)
	}
	t.Cleanup(func() { _ = caller.Close() })

	t.Run("成功:应答翻译为录制同形态", func(t *testing.T) {
		result, isErr, err := caller.Call(ctx, "calc_greet", json.RawMessage(`{"name":"ada"}`))
		if err != nil || isErr {
			t.Fatalf("echo 工具应成功,实际 isErr=%v err=%v", isErr, err)
		}
		var probe struct {
			Content []struct{ Text string } `json:"content"`
		}
		if json.Unmarshal(result, &probe) != nil || len(probe.Content) != 1 || probe.Content[0].Text != "hi ada" {
			t.Errorf("应答应含回显文本,实际 %s", result)
		}
	})
	t.Run("业务失败:IsError 置位透传", func(t *testing.T) {
		_, isErr, err := caller.Call(ctx, "biz_fail", nil)
		if err != nil || !isErr {
			t.Fatalf("biz_fail 应 isError 置位,实际 isErr=%v err=%v", isErr, err)
		}
	})
	t.Run("传输错误:err 非 nil 且保留上游点名锚点", func(t *testing.T) {
		_, isErr, err := caller.Call(ctx, "die", nil)
		if err == nil || isErr {
			t.Fatalf("die 应报错,实际 isErr=%v err=%v", isErr, err)
		}
		if !strings.Contains(err.Error(), `上游 "calc"`) {
			t.Errorf("错误文案应保留上游点名锚点,实际 %q", err)
		}
	})
	t.Run("请求头经 HTTP 层注入(服务端实测)", func(t *testing.T) {
		if _, _, err := caller.Call(ctx, "calc_greet", nil); err != nil {
			t.Fatalf("调用失败: %v", err)
		}
		if got := seen.get("x-role"); got != "admin" {
			t.Errorf("服务端应收到 x-role:admin,实际 %q", got)
		}
	})
}

// TestRun端到端翻译层:录制文件+夹具网关→run 的退出码三态与报告输出
// (真 SDK 会话,零外网;真网关进程契约归票 03 e2e)。
func TestRun端到端翻译层(t *testing.T) {
	endpoint, _ := newTestGateway(t, "hi ada")

	t.Run("正例:全过退出码 0,表格默认", func(t *testing.T) {
		file := writeRecording(t, successLine(t, "calc_greet", "hi ada"))
		var out, errOut bytes.Buffer
		code := run([]string{"-file", file, "-endpoint", endpoint, "-header", "x-role:admin"}, &out, &errOut)
		if code != exitOK {
			t.Fatalf("正例应退出码 0,实际 %d\n输出:\n%s%s", code, errOut.String(), out.String())
		}
		if !strings.Contains(out.String(), "evalx 回放报告") || !strings.Contains(out.String(), "全部断言通过") {
			t.Errorf("默认表格形态应输出人读报告,实际:\n%s", out.String())
		}
	})
	t.Run("负例:注入差异退出码 1,失败明细点名 grader", func(t *testing.T) {
		bad, badSeen := newTestGateway(t, "hi INJECTED")
		_ = badSeen
		file := writeRecording(t, successLine(t, "calc_greet", "hi ada"))
		var out, errOut bytes.Buffer
		code := run([]string{"-file", file, "-endpoint", bad, "-json"}, &out, &errOut)
		if code != exitAssertionFailed {
			t.Fatalf("注入差异应退出码 1,实际 %d\n输出:\n%s%s", code, errOut.String(), out.String())
		}
		var rep evalx.Report
		if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
			t.Fatalf("-json 应输出机器可读报告: %v\n%s", err, out.String())
		}
		if rep.OK() || len(rep.Failures) != 1 || rep.Failures[0].Grader != evalx.GraderResultEqual {
			t.Errorf("负例应报 result_equal 失败,实际 %+v", rep.Failures)
		}
	})
	t.Run("JSON 形态:字段齐备", func(t *testing.T) {
		file := writeRecording(t, successLine(t, "calc_greet", "hi ada"))
		var out, errOut bytes.Buffer
		code := run([]string{"-file", file, "-endpoint", endpoint, "-json"}, &out, &errOut)
		if code != exitOK {
			t.Fatalf("应退出码 0,实际 %d: %s", code, errOut.String())
		}
		var m map[string]any
		if json.Unmarshal(out.Bytes(), &m) != nil {
			t.Fatalf("JSON 形态应可解析:\n%s", out.String())
		}
		for _, key := range []string{"total", "passed", "failed", "failures"} {
			if _, has := m[key]; !has {
				t.Errorf("JSON 报告应含 %q 字段,实际 %v", key, m)
			}
		}
	})
	t.Run("文件不可读:退出码 2", func(t *testing.T) {
		var out, errOut bytes.Buffer
		code := run([]string{"-file", filepath.Join(t.TempDir(), "缺失.jsonl"), "-endpoint", endpoint}, &out, &errOut)
		if code != exitRuntimeError {
			t.Fatalf("文件不可读应退出码 2,实际 %d", code)
		}
		if !strings.Contains(errOut.String(), "缺失.jsonl") {
			t.Errorf("stderr 应点名文件,实际 %q", errOut.String())
		}
	})
	t.Run("端点不可达:退出码 2", func(t *testing.T) {
		file := writeRecording(t, successLine(t, "calc_greet", "hi ada"))
		var out, errOut bytes.Buffer
		code := run([]string{"-file", file, "-endpoint", "http://127.0.0.1:1/mcp"}, &out, &errOut)
		if code != exitRuntimeError {
			t.Fatalf("端点不可达应退出码 2,实际 %d", code)
		}
		if !strings.Contains(errOut.String(), "不可达") {
			t.Errorf("stderr 应点名端点不可达,实际 %q", errOut.String())
		}
	})
	t.Run("flags 缺失:退出码 2", func(t *testing.T) {
		var out, errOut bytes.Buffer
		if code := run([]string{}, &out, &errOut); code != exitRuntimeError {
			t.Fatalf("flags 缺失应退出码 2,实际 %d", code)
		}
	})
	t.Run("错误形态正例:录制 transport_error 回放 die 同锚点全过", func(t *testing.T) {
		file := writeRecording(t, `{"v":"1","seq":1,"time":"2026-11-08T00:00:00Z","method":"tools/call",`+
			`"tool":"die","args":null,"kind":"transport_error","result":null,`+
			`"error":"calling \"tools/call\": 上游 \"calc\" 不可用: 录制态故障","latency_ms":0}`)
		var out, errOut bytes.Buffer
		code := run([]string{"-file", file, "-endpoint", endpoint}, &out, &errOut)
		if code != exitOK {
			t.Fatalf("同锚点传输错误应全过,实际 %d\n输出:\n%s%s", code, errOut.String(), out.String())
		}
	})
}
