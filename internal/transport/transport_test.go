// transport 包测试:黑盒驱动 transport.New 产出的 http.Handler(spec 0002
// Testing Decisions:transport 对外是标准库 http.Handler,不新增自制接缝)。
// 聚合/路由行为用真实 gateway + 假 upstream.Client 驱动;端到端行为另见
// TestGatewayRoundTrip 的进程内双假上游全链路测试。
package transport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/magician336/flamingos/internal/gateway"
	"github.com/magician336/flamingos/internal/transport"
	"github.com/magician336/flamingos/internal/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeClient 是 upstream.Client 的进程内假实现:按预置清单/错误应答,
// 并以"回显收到的原始 tool 名"作为 CallTool 结果,供断言转发路径。
type fakeClient struct {
	tools   []*mcp.Tool // ListTools 预置返回
	listErr error       // 非 nil 时 ListTools 返回它
}

func (f *fakeClient) ListTools(context.Context) ([]*mcp.Tool, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.tools, nil
}

func (f *fakeClient) CallTool(_ context.Context, name string, _ json.RawMessage) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: name}},
	}, nil
}

func (f *fakeClient) Close() error { return nil }

// gatewayHarness 是已装配并完成 initialize 的进程内网关:测试服务真实
// 监听,单一端点为 POST /mcp;生命周期挂靠 t.Cleanup。
type gatewayHarness struct {
	ts *httptest.Server // 网关测试服务
	cs *mcp.ClientSession
}

// newGatewayHarness 以给定上游装配真实 gateway + transport,再以 go-sdk
// 客户端对网关完成 initialize。
func newGatewayHarness(t *testing.T, clients map[string]upstream.Client) *gatewayHarness {
	t.Helper()
	handler, err := transport.New(context.Background(), gateway.New(clients))
	if err != nil {
		t.Fatalf("构造 transport 失败: %v", err)
	}
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint: ts.URL + "/mcp",
			// 无状态网关不维持常驻 SSE 流(与 spike/sdkapi 同款配置)
			DisableStandaloneSSE: true,
		}, nil)
	if err != nil {
		t.Fatalf("连接网关失败: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return &gatewayHarness{ts: ts, cs: cs}
}

func TestNew(t *testing.T) {
	t.Run("上游列举失败:HandleToolsList 报错时构造即败(启动不带病,D8)", func(t *testing.T) {
		gw := gateway.New(map[string]upstream.Client{
			"calc": &fakeClient{listErr: errors.New("列举失败")},
		})

		_, err := transport.New(context.Background(), gw)
		if err == nil {
			t.Fatal("期望构造失败,实际成功")
		}
	})

	t.Run("Schema 兜底:上游 tool 缺 InputSchema 时注册空 object schema 而非 panic", func(t *testing.T) {
		h := newGatewayHarness(t, map[string]upstream.Client{
			// spike §3 实测:缺 schema 的 AddTool 直接 panic,必须兜底。
			"calc": &fakeClient{tools: []*mcp.Tool{
				{Name: "greet", Description: "无 schema 的上游 tool"},
			}},
		})

		list, err := h.cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatalf("列举 tool 失败: %v", err)
		}
		byName := toolsByName(list.Tools)
		greet := byName["calc_greet"]
		if greet == nil {
			t.Fatalf("聚合清单应含兜底注册的 calc_greet,实际 %v", toolNames(list.Tools))
		}
		schema, ok := greet.InputSchema.(map[string]any)
		if !ok {
			t.Fatalf("兜底 schema 应为线路解码后的 map,实际 %T: %+v", greet.InputSchema, greet.InputSchema)
		}
		if schema["type"] != "object" {
			t.Errorf("兜底 schema.type 应为 object,实际 %v", schema["type"])
		}
	})

	t.Run("转发走 HandleToolsCall:Agent 调聚合名,上游收到拆前缀后的原始名", func(t *testing.T) {
		h := newGatewayHarness(t, map[string]upstream.Client{
			"calc": &fakeClient{tools: []*mcp.Tool{
				{Name: "greet", InputSchema: map[string]any{"type": "object"}},
			}},
		})

		res, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "calc_greet",
			Arguments: json.RawMessage(`{"msg":"你好"}`),
		})
		if err != nil {
			t.Fatalf("调用转发失败: %v", err)
		}
		// fakeClient 回显收到的原始名:若转发没走 HandleToolsCall 拆前缀,
		// 这里会原样返回聚合名 calc_greet。
		if got := textOf(res); got != "greet" {
			t.Errorf("上游应收到拆前缀后的原始名 greet,实际 %q", got)
		}
	})

	t.Run("非法 schema 跳过:非 object schema 的 tool 不拖死注册并输出警示", func(t *testing.T) {
		var warning bytes.Buffer
		log.SetOutput(&warning)
		t.Cleanup(func() { log.SetOutput(os.Stderr) })

		h := newGatewayHarness(t, map[string]upstream.Client{
			"calc": &fakeClient{tools: []*mcp.Tool{
				{Name: "add", InputSchema: map[string]any{"type": "object"}},    // 合法:应保留
				{Name: "broken", InputSchema: map[string]any{"type": "string"}}, // 非 object:AddTool 会 panic
				{Name: "garbage", InputSchema: "不是 schema 对象"},                  // 非 map 形状
			}},
		})

		list, err := h.cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatalf("单个坏 tool 不应拖死注册,实际报错: %v", err)
		}
		if got := toolNames(list.Tools); len(got) != 1 || got[0] != "calc_add" {
			t.Errorf("清单应只保留 object schema 的 calc_add,实际 %v", got)
		}
		warn := warning.String()
		for _, want := range []string{"calc", "broken", "garbage", "警告"} {
			if !strings.Contains(warn, want) {
				t.Errorf("警示应包含 %q,实际 %q", want, warn)
			}
		}
	})
}

// TestInitializeIdentity 把 initialize 自报身份钉死为票面契约:Agent 端
// 应能识别连接的是 flamingos(spec 0002 用户故事 9)。
func TestInitializeIdentity(t *testing.T) {
	h := newGatewayHarness(t, map[string]upstream.Client{
		"calc": &fakeClient{tools: []*mcp.Tool{
			{Name: "greet", InputSchema: map[string]any{"type": "object"}},
		}},
	})

	info := h.cs.InitializeResult().ServerInfo
	if info == nil {
		t.Fatal("initialize 应答应含 serverInfo")
	}
	if info.Name != "flamingos" {
		t.Errorf("serverInfo.name = %q,期望自报 %q", info.Name, "flamingos")
	}
}

// TestEndpointSemantics 把 Stateless 端点语义钉死为票面契约:单一端点
// POST /mcp;GET/DELETE → 405 且带 Allow 头;其余路径不是端点。
func TestEndpointSemantics(t *testing.T) {
	h := newGatewayHarness(t, map[string]upstream.Client{
		"calc": &fakeClient{tools: []*mcp.Tool{
			{Name: "greet", InputSchema: map[string]any{"type": "object"}},
		}},
	})
	client := h.ts.Client()

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantAllow  string // 期望响应 Allow 头(空串表示不断言)
	}{
		{name: "GET /mcp:Stateless 不支持,405 并声明仅 POST", method: http.MethodGet, path: "/mcp", wantStatus: http.StatusMethodNotAllowed, wantAllow: "POST"},
		{name: "DELETE /mcp:Stateless 无会话可删,405 并声明仅 POST", method: http.MethodDelete, path: "/mcp", wantStatus: http.StatusMethodNotAllowed, wantAllow: "POST"},
		{name: "POST /other:单一端点,其余路径不是服务", method: http.MethodPost, path: "/other", wantStatus: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, h.ts.URL+tt.path, nil)
			if err != nil {
				t.Fatalf("构造请求失败: %v", err)
			}
			res, err := client.Do(req)
			if err != nil {
				t.Fatalf("发送请求失败: %v", err)
			}
			defer res.Body.Close()
			if res.StatusCode != tt.wantStatus {
				t.Errorf("%s %s 状态码 = %d,期望 %d", tt.method, tt.path, res.StatusCode, tt.wantStatus)
			}
			if tt.wantAllow != "" && res.Header.Get("Allow") != tt.wantAllow {
				t.Errorf("Allow 头 = %q,期望 %q", res.Header.Get("Allow"), tt.wantAllow)
			}
		})
	}
}

// toolsByName 把清单按名字索引,便于断言聚合结果。
func toolsByName(tools []*mcp.Tool) map[string]*mcp.Tool {
	byName := make(map[string]*mcp.Tool, len(tools))
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	return byName
}

// toolNames 提取清单的全部名字,用于失败信息展示。
func toolNames(tools []*mcp.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

// textOf 提取结果中第一段文本内容,便于断言转发内容。
func textOf(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return "<非文本内容>"
}

// ---- 进程内双假上游全链路集成测试(票 03 核心验收,spec 0002 Testing
// Decisions:真实 upstream adapter + 真实 gateway + 真实 transport,黑盒
// 走完 initialize → tools/list → tools/call;W1 周验收的机械等价物)----

// integrationTimeout 是集成测试里上游构造与调用的超时:远大于进程内往返,
// 避免偶发超时。
const integrationTimeout = 5 * time.Second

// fakeUpstreamTool 描述假上游注册的一个 tool。
type fakeUpstreamTool struct {
	name        string
	description string
	schema      map[string]any
	handler     mcp.ToolHandler
}

// newFakeUpstream 起一个进程内假上游(go-sdk server + Stateless Streamable
// HTTP handler + httptest,端点挂 /mcp),注册给定 tool;返回可显式关闭的
// 服务,供故障隔离场景在测试中途杀掉。生命周期不挂 t.Cleanup,由调用方
// 决定何时关(故障隔离子场景需要在存活时构造、中途关闭)。
func newFakeUpstream(t *testing.T, name string, tools ...fakeUpstreamTool) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: name, Version: "0.0.1"}, nil)
	for _, ft := range tools {
		server.AddTool(&mcp.Tool{
			Name:        ft.name,
			Description: ft.description,
			InputSchema: ft.schema,
		}, ft.handler)
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	))
	ts := httptest.NewServer(mux)
	return ts
}

// addHandler 计算 a+b:参数由原始字节解析,结果以文本返回——独立于网关的
// 业务字面量(3+4=7)由测试自带,不经网关计算。
func addHandler(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var p struct {
		A int `json:"a"`
		B int `json:"b"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &p); err != nil {
		return nil, fmt.Errorf("解析参数失败: %w", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: strconv.Itoa(p.A + p.B)}},
	}, nil
}

// echoHandler 回显原始参数字节:若转发链路重编码或误编 base64,回显必然失真
// (与 spike/sdkapi、upstream 包测试同款)。
func echoHandler(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(req.Params.Arguments)}},
	}, nil
}

// fakeUpstreamSpecs 返回两个假上游的 tool 规格:calc 注册 add,fs 注册 echo。
func fakeUpstreamSpecs() (calc, fs []fakeUpstreamTool) {
	calc = []fakeUpstreamTool{{
		name:        "add",
		description: "返回 a 与 b 的和",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"a": map[string]any{"type": "integer"},
				"b": map[string]any{"type": "integer"},
			},
			"required": []any{"a", "b"},
		},
		handler: addHandler,
	}}
	fs = []fakeUpstreamTool{{
		name:        "echo",
		description: "回显原始参数字节",
		schema:      map[string]any{"type": "object"},
		handler:     echoHandler,
	}}
	return calc, fs
}

// directCall 直连假上游调用原始 tool 名,返回结果文本——"与直连上游一致"
// 的独立参照,不经网关链路。
func directCall(t *testing.T, endpoint, tool string, args json.RawMessage) string {
	t.Helper()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "direct-client", Version: "0.0.1"}, nil).
		Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint:             endpoint,
			DisableStandaloneSSE: true,
		}, nil)
	if err != nil {
		t.Fatalf("直连上游 %s 失败: %v", endpoint, err)
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("直连调用 %s 失败: %v", tool, err)
	}
	return textOf(res)
}

func TestGatewayRoundTrip(t *testing.T) {
	calcTools, fsTools := fakeUpstreamSpecs()
	calc := newFakeUpstream(t, "calc", calcTools...)
	fs := newFakeUpstream(t, "fs", fsTools...)
	defer fs.Close()
	defer calc.Close()

	// 真实 HTTP adapter:构造时即完成对假上游的 initialize 握手。
	calcClient, err := upstream.NewHTTP(context.Background(), "calc", calc.URL+"/mcp", integrationTimeout)
	if err != nil {
		t.Fatalf("构造 calc 上游客户端失败: %v", err)
	}
	defer func() { _ = calcClient.Close() }()
	fsClient, err := upstream.NewHTTP(context.Background(), "fs", fs.URL+"/mcp", integrationTimeout)
	if err != nil {
		t.Fatalf("构造 fs 上游客户端失败: %v", err)
	}
	defer func() { _ = fsClient.Close() }()

	h := newGatewayHarness(t, map[string]upstream.Client{
		"calc": calcClient,
		"fs":   fsClient,
	})

	t.Run("聚合清单:双上游 tool 带命名空间前缀,描述与 schema 原样保留", func(t *testing.T) {
		list, err := h.cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatalf("tools/list 失败: %v", err)
		}
		if got := toolNames(list.Tools); len(got) != 2 || got[0] != "calc_add" || got[1] != "fs_echo" {
			t.Fatalf("聚合清单应为 [calc_add fs_echo],实际 %v", got)
		}
		byName := toolsByName(list.Tools)
		if d := byName["calc_add"].Description; d != "返回 a 与 b 的和" {
			t.Errorf("calc_add 描述应原样保留,实际 %q", d)
		}
		schema, ok := byName["calc_add"].InputSchema.(map[string]any)
		if !ok {
			t.Fatalf("calc_add schema 应为 map,实际 %T", byName["calc_add"].InputSchema)
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("calc_add schema.properties 应为 map,实际 %T", schema["properties"])
		}
		for _, field := range []string{"a", "b"} {
			if _, ok := props[field]; !ok {
				t.Errorf("calc_add schema.properties 应保留字段 %q,实际 %v", field, props)
			}
		}
	})

	t.Run("调用转发:经网关的调用结果与直连上游一致", func(t *testing.T) {
		addArgs := json.RawMessage(`{"a":3,"b":4}`)
		res, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "calc_add", Arguments: addArgs})
		if err != nil {
			t.Fatalf("经网关调用 calc_add 失败: %v", err)
		}
		direct := directCall(t, calc.URL+"/mcp", "add", addArgs)
		if got := textOf(res); got != direct || got != "7" {
			t.Errorf("calc_add 经网关 = %q,直连 = %q,期望一致且为独立字面量 7", got, direct)
		}
		if res.IsError {
			t.Error("calc_add 正常应答不应是 IsError")
		}

		echoArgs := json.RawMessage(`{"msg":"你好,flamingos","n":1}`)
		res, err = h.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "fs_echo", Arguments: echoArgs})
		if err != nil {
			t.Fatalf("经网关调用 fs_echo 失败: %v", err)
		}
		if got := textOf(res); got != string(echoArgs) {
			t.Errorf("fs_echo 应字节级回显参数:\n期望 %q\n实际 %q", string(echoArgs), got)
		}
	})

	t.Run("故障隔离:杀掉 calc 后指向它的调用点名不可用,另一上游照常", func(t *testing.T) {
		calc.Close() // 杀掉 calc 假上游,fs 不受影响

		_, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "calc_add",
			Arguments: json.RawMessage(`{"a":1,"b":2}`),
		})
		if err == nil {
			t.Fatal("期望指向已死上游的调用报错,实际成功")
		}
		msg := err.Error()
		for _, want := range []string{"calc", "不可用"} {
			if !strings.Contains(msg, want) {
				t.Errorf("错误应点名故障上游与不可用语义(含 %q),实际 %q", want, msg)
			}
		}

		res, err := h.cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "fs_echo",
			Arguments: json.RawMessage(`{"alive":true}`),
		})
		if err != nil {
			t.Fatalf("另一上游应照常服务,实际报错: %v", err)
		}
		if got := textOf(res); got != `{"alive":true}` {
			t.Errorf("fs_echo 应正常回显,实际 %q", got)
		}
	})
}
