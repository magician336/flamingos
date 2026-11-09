// stdioserver 是 stdio adapter 测试的子进程夹具:最小 go-sdk stdio server
// (NewServer + 四个 tool + StdioTransport),经 stdin/stdout 以换行分隔
// JSON 与被测客户端往返——被测对象是 adapter,夹具只搭台,测的是真实
// stdio 路径而非模拟管道(spec 0003 Testing Decisions)。
//
// 本文件位于 testdata 目录:go 工具链忽略该目录,不参与 go build ./...
// 的生产构建;测试入口(stdio_test.go 的 TestMain)用 go build 一次性
// 构建为临时二进制,全部测试复用,避免逐例 go build 拖慢套件
// (spec 0003 用户故事 17)。
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// slowToolDelay 是 slow 工具的固定延迟:远超超时用例给客户端的期限,
// 保证客户端超时必然先于应答触发;客户端取消传播到 request ctx 时
// 提前返回,缩短子进程体面退出的等待时间。
const slowToolDelay = 3 * time.Second

func main() {
	server := mcp.NewServer(&mcp.Implementation{Name: "stdio-fixture", Version: "0.0.1"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "echo",
		Description: "回显收到的原始参数字节",
		InputSchema: map[string]any{"type": "object"},
	}, echoHandler)
	server.AddTool(&mcp.Tool{
		Name:        "slow",
		Description: "固定延迟后应答,用于验证客户端超时",
		InputSchema: map[string]any{"type": "object"},
	}, slowHandler)
	server.AddTool(&mcp.Tool{
		Name:        "fail",
		Description: "正常应答但业务失败(IsError)",
		InputSchema: map[string]any{"type": "object"},
	}, failHandler)
	server.AddTool(&mcp.Tool{
		Name:        "pid",
		Description: "应答子进程自身 PID,用于验证进程退出与故障隔离",
		InputSchema: map[string]any{"type": "object"},
	}, pidHandler)
	// 阻塞至 stdin 关闭(客户端 Close)即正常退出;SDK 自身 stdio 测试
	// 夹具同款(mcp/cmd_test.go),stdin EOF 时 Run 返回 nil、退出码 0。
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}

// echoHandler 代理式 handler:线路给什么参数字节就回显什么,不反序列化、
// 不重编码(与进程内假上游的 echo 同款,验证字节级透传)。
func echoHandler(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(req.Params.Arguments)}},
	}, nil
}

// slowHandler 固定延迟后应答;客户端因超时断开时按 ctx 取消提前返回,
// 避免拖住子进程的体面退出链。
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

// pidHandler 应答自身 PID 文本:adapter 测试用它核对 Close 后子进程确实
// 退出(无孤儿);后续装配票的故障隔离测试用它定位并杀掉子进程。
func pidHandler(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("%d", os.Getpid())}},
	}, nil
}
