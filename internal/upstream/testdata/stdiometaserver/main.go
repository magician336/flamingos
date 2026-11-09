// stdiometaserver 是票 0004-02 的 stdio `_meta` 传播子进程夹具:最小
// go-sdk stdio server,单个 meta 工具把收到的请求 `_meta` 原样序列化
// 回显——stdio 上游没有 HTTP 头,`_meta` 是其唯一 trace 通道,接收侧
// 回显即网关注入 `_meta.traceparent` 的全部证据(spec 0003 子进程夹具
// 模式的单工具变体;不复用 stdioserver 是为了不动其工具清单快照——
// cmd 包装配测试对它做恰等于断言,既有测试零改动)。
//
// 本文件位于 testdata 目录:go 工具链忽略该目录,不参与 go build ./...
// 的生产构建;消费者(upstream/transport 的 _meta 传播测试)用 go build
// 以显式 import path 一次性构建为临时二进制。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	server := mcp.NewServer(&mcp.Implementation{Name: "meta-stdio-fixture", Version: "0.0.1"}, nil)
	server.AddTool(&mcp.Tool{
		Name:        "meta",
		Description: "回显收到的请求 _meta(JSON),trace 传播断言的接收侧观察面",
		InputSchema: map[string]any{"type": "object"},
	}, metaHandler)
	// 阻塞至 stdin 关闭(客户端 Close)即正常退出(与 stdioserver 同款)。
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}

// metaHandler 把收到的请求 `_meta` 原样序列化回显;`_meta` 缺失时回显
// JSON null,同样是有信息的观察结果。
func metaHandler(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	raw, err := json.Marshal(req.Params.Meta)
	if err != nil {
		return nil, fmt.Errorf("序列化 _meta 失败: %w", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}},
	}, nil
}
