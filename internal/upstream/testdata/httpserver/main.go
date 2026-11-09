// httpserver 是 HTTP 假上游的可执行夹具:最小 go-sdk server(Stateless
// Streamable HTTP handler)+ 与 stdioserver 同族的四个 tool(echo/fail/
// slow/pid),经真实 HTTP 监听与被测客户端往返——被测对象是网关与
// adapter,夹具只搭台,测的是真实 HTTP 路径而非进程内替身(spec 0007
// Implementation Decisions:可执行夹具与 stdioserver 同位同归属,kill -9
// 与断流演练有真实进程可注入)。
//
// 本文件位于 testdata 目录:go 工具链忽略该目录,不参与 go build ./...
// 的生产构建;测试入口(e2e/benchmark 的 TestMain)以 go build 一次性
// 构建为临时二进制(stdioserver 先例)。监听地址取 os.Args[1](如
// 127.0.0.1:9001)。票 02 演练的异常注入形态(panic 模式、协议外应答
// 模式)在既有 tool 表与 handler 装配点上追加,不新增目录、不改对外形态。
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// slowToolDelay 是 slow 工具的固定延迟:与 stdioserver 同值,保证断流/
// 超时场景中"在途"状态可观察;客户端取消传播到 request ctx 时提前返回,
// 缩短体面退出的等待时间。
const slowToolDelay = 3 * time.Second

// injectMode 是异常注入形态的启动参数值:argv[2] == injectMode 时夹具在
// 既有 tool 表与 handler 装配点上追加两种注入形态(panic 工具、协议外应
// 答短路器)。默认形态(无该参数)与票 01 逐字节一致——tool 表四工具,
// handler 无包装(票 01 全链判据的 8 工具聚合不破)。
const injectMode = "inject"

// outOfBandToolName 是协议外应答注入的触发工具名:裸 HTTP 层只对 tools/call
// 目标为该名的请求短路(精确匹配,不误伤 echo 等透传)。该工具同时注册进
// SDK server 的 tool 表——网关对聚合清单做启动快照注册,未注册的原始名
// 到不了夹具(网关侧 SDK server 先以 unknown tool 拒绝),短路器无从生效。
const outOfBandToolName = "rawreply"

// panicToolName 是 panic 注入的触发工具名:handler 直接 panic。
const panicToolName = "panic"

func main() {
	if len(os.Args) < 2 || len(os.Args) > 3 || (len(os.Args) == 3 && os.Args[2] != injectMode) {
		log.Fatalf("用法: httpserver <监听地址,如 127.0.0.1:9001> [%s]", injectMode)
	}
	addr := os.Args[1]
	inject := len(os.Args) == 3
	server := mcp.NewServer(&mcp.Implementation{Name: "http-fixture", Version: "0.0.1"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "echo",
		Description: "回显收到的原始参数字节",
		InputSchema: map[string]any{"type": "object"},
	}, echoHandler)
	server.AddTool(&mcp.Tool{
		Name:        "slow",
		Description: "固定延迟后应答,用于验证客户端超时与断流",
		InputSchema: map[string]any{"type": "object"},
	}, slowHandler)
	server.AddTool(&mcp.Tool{
		Name:        "fail",
		Description: "正常应答但业务失败(IsError)",
		InputSchema: map[string]any{"type": "object"},
	}, failHandler)
	server.AddTool(&mcp.Tool{
		Name:        "pid",
		Description: "应答本进程 PID,用于故障演练定位与无孤儿核对",
		InputSchema: map[string]any{"type": "object"},
	}, pidHandler)
	if inject { // 注入形态追加点:tool 表在既有四工具上追加,不新建目录
		server.AddTool(&mcp.Tool{
			Name:        outOfBandToolName,
			Description: "协议外应答触发工具(handler 不可达,短路在裸 HTTP 层)",
			InputSchema: map[string]any{"type": "object"},
		}, outOfBandStubHandler)
		server.AddTool(&mcp.Tool{
			Name:        panicToolName,
			Description: "panic 注入触发工具(handler 直接 panic,上游进程自身崩溃)",
			InputSchema: map[string]any{"type": "object"},
		}, panicHandler)
	}
	// Stateless 语义与网关侧 upstream 客户端用法对齐(DisableStandaloneSSE
	// 的对端):仅 POST,每请求临时会话,无会话头需要维持。注入形态在 SDK
	// handler 外包一层短路器(handler 装配点追加)。
	handler := http.Handler(mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	))
	if inject {
		handler = outOfBandReplyHandler{next: handler}
	}
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatal(err)
	}
}

// echoHandler 代理式 handler:线路给什么参数字节就回显什么,不反序列化、
// 不重编码(与 stdioserver 的 echo 同款,验证字节级透传)。
func echoHandler(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(req.Params.Arguments)}},
	}, nil
}

// slowHandler 固定延迟后应答;客户端因超时断开时按 ctx 取消提前返回,
// 避免拖住进程的体面退出链。
func slowHandler(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(slowToolDelay):
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "迟到的应答"}},
	}, nil
}

// failHandler 模拟"上游正常应答但业务失败":IsError=true 的应答在协议层
// 是成功响应,业务语义由调用方解读——网关必须原样透传(错误契约②)。
func failHandler(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: "上游业务失败:余额不足"}},
	}, nil
}

// pidHandler 应答自身 PID 文本:e2e 以之与拉起时记录的进程 PID 精确对照,
// 后续故障演练票用它定位并注入 kill -9。
func pidHandler(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("%d", os.Getpid())}},
	}, nil
}

// outOfBandStubHandler 是协议外应答触发工具的占位 handler:不可达——该工
// 具名的请求被外层 outOfBandReplyHandler 在 SDK handler 之前短路,注册只
// 为让 tool 表成立(网关启动快照注册需要清单含此名)。
func outOfBandStubHandler(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return nil, fmt.Errorf("%s 不可达:请求应在裸 HTTP 层被短路", outOfBandToolName)
}

// panicHandler 是 panic 注入的触发 handler:直接 panic,无任何 recover。
// 边界注释(本注入实证的对象):go-sdk v1.8.0 生产代码零 recover,且
// tools/call 在 SDK 会话读 goroutine 上执行(streamable.go servePOST 把消
// 息发进会话的 incoming 通道,handler 由 connect() 内 spawned 的 jsonrpc2
// 读 goroutine 调用)——不在 net/http 连接 goroutine 上,panic 击穿整个
// 进程;net/http 的"每连接 recover 隔离"只覆盖连接 goroutine 上的 panic,
// 两者边界由此钉住。
func panicHandler(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	panic("夹具注入的 panic:" + panicToolName)
}

// outOfBandReplyHandler 是协议外应答注入的裸 HTTP 层短路器:包在 SDK
// handler 之外,对 tools/call 目标为 outOfBandToolName 的请求直接写回
// 200 + text/html 的非 JSON-RPC 字节流(错误 Media Type 形态),SDK
// handler 对该请求不可达——探测的是网关侧 SDK 客户端对上游应答的解析路
// 径,而非 SDK 服务端行为。约束:只按 JSON-RPC 信封的 params.name 精确
// 匹配触发;请求体读取失败或解析失败一律原样放行(畸形交由 SDK 自行拒
// 绝);放行路径必须还原请求体(body 是单次流,SDK handler 还要读)。
type outOfBandReplyHandler struct{ next http.Handler }

func (h outOfBandReplyHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	if targetsTool(body, outOfBandToolName) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>协议外应答:本字节流不是 JSON-RPC</html>"))
		return
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	h.next.ServeHTTP(w, req)
}

// targetsTool 判断 JSON-RPC 请求体是否以给定原始 tool 名为 tools/call 目
// 标:轻量结构探测(方法名 + params.name 精确匹配),不依赖 SDK 内部类
// 型;解析失败返回 false(原样放行)。
func targetsTool(body []byte, name string) bool {
	var req struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if json.Unmarshal(body, &req) != nil {
		return false
	}
	return req.Method == "tools/call" && req.Params.Name == name
}
