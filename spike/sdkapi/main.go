// spike:以可运行代码验证 go-sdk v1.8.0 的 API 形状(docs/design/architecture.md §7-U2)。
// 验证点:
//  ① Server 构造 + AddTool(untyped handler,代理式 schema 原样注册)
//  ② InMemory 往返:ListTools / CallTool
//  ③ ClientSessionOptions.ProtocolVersion 版本 Pin(stdio 2025-11-25 策略)
//  ④ HTTP Stateless 往返:StreamableHTTPHandler + StreamableClientTransport
//  ⑤ CallToolParamsRaw.Arguments 原始参数透传(接口草图 json.RawMessage 的依据)
//  ⑥ AddTool 缺 InputSchema 时 panic(代理兜底依据)
//
// 运行:go run ./spike/sdkapi
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// echoHandler 代理式 handler:req.Params.Arguments 是 json.RawMessage,
// 上游线路给什么就持有什么——不反序列化、不重编码。
func echoHandler(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(req.Params.Arguments)}},
	}, nil
}

// addUpstreamTools 模拟"把上游 tool 原样注册进网关 server":
// InputSchema 用 map(线路解码后的真实形状),不做任何转换。
func addUpstreamTools(s *mcp.Server) {
	s.AddTool(&mcp.Tool{
		Name:        "echo",
		Description: "回显原始参数",
		InputSchema: map[string]any{"type": "object"},
	}, echoHandler)
}

func main() {
	if err := run(); err != nil {
		log.Fatal("SPIKE FAIL:", err)
	}
}

func run() error {
	ctx := context.Background()

	// ① 服务端构造 + 代理式注册
	server := mcp.NewServer(&mcp.Implementation{Name: "spike-upstream", Version: "0.0.1"}, nil)
	addUpstreamTools(server)

	// ② InMemory 往返
	client := mcp.NewClient(&mcp.Implementation{Name: "spike-client", Version: "0.0.1"}, nil)
	ct, st := mcp.NewInMemoryTransports()
	go func() { _ = server.Run(ctx, st) }() // Run 阻塞直至 transport 关闭;错误仅影响清理,忽略

	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		return fmt.Errorf("inmem connect: %w", err)
	}
	defer cs.Close()

	lt, err := cs.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("inmem list: %w", err)
	}
	fmt.Printf("[inmem]   tools = %v\n", toolNames(lt))

	args, _ := json.Marshal(map[string]any{"msg": "你好网关"})
	// 注意:Arguments 是 any,裸 []byte 会被 encoding/json 编成 base64;
	// 必须转成 json.RawMessage 才能原样嵌入(网关转发 args 的标准写法)。
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: json.RawMessage(args)})
	if err != nil {
		return fmt.Errorf("inmem call: %w", err)
	}
	fmt.Printf("[inmem]   echo → %s\n", textOf(res))

	// ③ 版本 Pin(stdio 2025-11-25 策略;传输层无关,内存 transport 即可验证)
	ct2, st2 := mcp.NewInMemoryTransports()
	go func() { _ = server.Run(ctx, st2) }()
	cs2, err := client.Connect(ctx, ct2, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		return fmt.Errorf("pin connect: %w", err)
	}
	defer cs2.Close()
	fmt.Printf("[pin]     协商版本 = %s(期望 2025-11-25)\n", cs2.InitializeResult().ProtocolVersion)

	// ④ HTTP Stateless 往返——网关的 HTTP 传输路径今天打通
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true})
	ts := httptest.NewServer(handler)
	defer ts.Close()

	hclient := mcp.NewClient(&mcp.Implementation{Name: "spike-http", Version: "0.0.1"}, nil)
	hcs, err := hclient.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             ts.URL,
		DisableStandaloneSSE: true, // 无状态网关不维持常驻 SSE 流
	}, nil)
	if err != nil {
		return fmt.Errorf("http stateless connect: %w", err)
	}
	defer hcs.Close()

	hlt, err := hcs.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("http stateless list: %w", err)
	}
	fmt.Printf("[http]    Stateless tools = %v\n", toolNames(hlt))

	hres, err := hcs.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: json.RawMessage(args)})
	if err != nil {
		return fmt.Errorf("http stateless call: %w", err)
	}
	fmt.Printf("[http]    Stateless echo → %s\n", textOf(hres))

	// ⑥ 验证缺 schema 的 AddTool 行为(代理兜底的依据)
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("[panic]   无 schema AddTool 确认 panic: %v\n", trunc(fmt.Sprint(r)))
			}
		}()
		bad := mcp.NewServer(&mcp.Implementation{Name: "bad", Version: "0"}, nil)
		bad.AddTool(&mcp.Tool{Name: "noschema"}, echoHandler)
		fmt.Println("[panic]   未触发——兜底结论需修正")
	}()

	fmt.Println("SPIKE OK:①②③④⑤⑥ 全部验证完毕")
	return nil
}

func toolNames(lt *mcp.ListToolsResult) []string {
	names := make([]string, 0, len(lt.Tools))
	for _, t := range lt.Tools {
		names = append(names, t.Name)
	}
	return names
}

func textOf(r *mcp.CallToolResult) string {
	for _, c := range r.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return "<non-text>"
}

func trunc(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}
