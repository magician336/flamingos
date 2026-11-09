// Command examples 是 flamingos 网关的 Eino 接入示例(module 根包,go
// build 产物与 go run 目标均为本目录):经 eino-ext officialmcp 组件的
// GetTools 把网关的聚合工具桥接为 Eino 工具接口(tool.BaseTool),打印桥
// 接清单,并经该接口直调其中一个工具、输出应答。全程无 LLM key 依赖——
// 接入验证不依赖任何厂商账号,从直调到 ReAct agent 的接线方式见本目录
// README 与官方示例出处(eino-ext officialmcp 的 examples/mcp.go,本文件
// 头部注释注明与它的差异点)。
//
// 运行前提:一个在跑的 flamingos 网关(最小配置与启动命令见本目录
// README.md)。网关 MCP 端点缺省 http://127.0.0.1:8080/mcp,经 -endpoint
// flag 或环境变量 FLAMINGOS_MCP_ENDPOINT 覆盖,不写死在代码里。
//
//	# 在 examples 目录(独立 module)内:
//	go run . -endpoint http://127.0.0.1:8080/mcp \
//	  -tool calc_echo -args '{"text":"hello from eino"}'
//
// 与官方示例(eino-ext components/tool/mcp/officialmcp v0.1.1 的
// examples/mcp.go)的两处差异:
//
//  1. transport:官方示例用 SSEClientTransport;此处按网关的实际对外形态换
//     StreamableClientTransport 且 DisableStandaloneSSE: true——网关是无状
//     态 Streamable HTTP,不维持常驻 SSE 流(架构 §4 adapter 实现注意⑤,
//     cmd e2e 的 agent 连接同款)。
//  2. 不含模型段:官方 94 行示例的模型段依赖具体厂商 key,本示例不接模型,
//     只做桥接 + 直调(spec 0009:接入验证不依赖厂商账号)。
//
// 已知边界:eino-ext issue #1006 记录 officialmcp 对"远程带鉴权 server"路
// 径的支持现状——鉴权、工具选择与会话清理需调用方自行组装;本示例连接的
// 是无鉴权的自家网关,不受该边界影响。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	omcp "github.com/cloudwego/eino-ext/components/tool/mcp/officialmcp"
)

const (
	// defaultEndpoint 是网关 MCP 端点的兜底缺省:本地开发形态(cmd/flamingos
	// 的缺省监听 + /mcp 路径)。仅作为注入链的末端兜底,不以常量绑定调用目标。
	defaultEndpoint = "http://127.0.0.1:8080/mcp"

	// envEndpoint 是端点注入的环境变量名:容器/CI 场景免 flag 注入。
	envEndpoint = "FLAMINGOS_MCP_ENDPOINT"
)

// endpointFlag 解析端点注入链:flag 缺省值先取环境变量,再落兜底缺省。
func endpointFlag() string {
	if v := os.Getenv(envEndpoint); v != "" {
		return v
	}
	return defaultEndpoint
}

func main() {
	endpoint := flag.String("endpoint", endpointFlag(),
		"网关 MCP 端点 URL(缺省读环境变量 "+envEndpoint+",再缺省 "+defaultEndpoint+")")
	toolName := flag.String("tool", "",
		"要直调的聚合工具名(网关聚合命名形如 <上游名>_<工具名>;缺省取清单首项)")
	argsJSON := flag.String("args", `{"text":"hello from eino"}`,
		"工具调用参数 JSON 字符串")
	flag.Parse()

	ctx := context.Background()
	sess, err := connectGateway(ctx, *endpoint)
	if err != nil {
		log.Fatalf("连接网关 %s 失败: %v", *endpoint, err)
	}
	defer sess.Close() //nolint:errcheck // 示例进程退出路径,关闭失败无处置语义

	tools, err := omcp.GetTools(ctx, &omcp.Config{Cli: sess})
	if err != nil {
		log.Fatalf("桥接网关聚合工具失败: %v", err)
	}
	if err := printTools(ctx, tools); err != nil {
		log.Fatalf("打印桥接清单失败: %v", err)
	}

	selected, err := pickTool(ctx, tools, *toolName)
	if err != nil {
		log.Fatalf("选取工具失败: %v", err)
	}
	name := mustName(ctx, selected)
	reply, err := invoke(ctx, selected, *argsJSON)
	if err != nil {
		log.Fatalf("直调工具 %s 失败: %v", name, err)
	}
	fmt.Printf("\n直调 %s 的应答:\n%s\n", name, reply)
}

// connectGateway 建立 MCP 客户端会话:Streamable HTTP transport 指向网关
// /mcp 端点。DisableStandaloneSSE: true 与网关的无状态语义对齐——每次请求
// 独立完成,不维持常驻 SSE 流(差异点的实码位置,包注释第 1 条)。
func connectGateway(ctx context.Context, endpoint string) (*mcp.ClientSession, error) {
	transport := &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		DisableStandaloneSSE: true,
	}
	cli := mcp.NewClient(&mcp.Implementation{
		Name:    "flamingos-eino-example",
		Version: "0.0.1",
	}, nil)
	return cli.Connect(ctx, transport, nil)
}

// toolInfo 读取桥接产物的元信息:包内唯一的读取出处,printTools、pickTool、
// mustName 三处共用;失败文案也仅在此出现一次,调用方原样透传。
func toolInfo(ctx context.Context, bt tool.BaseTool) (*schema.ToolInfo, error) {
	info, err := bt.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取工具元信息失败: %w", err)
	}
	return info, nil
}

// printTools 打印桥接清单:名字与描述即网关聚合快照的原样透出,调用方可
// 据此核对聚合命名(形如 <上游名>_<工具名>)与上游工具描述。
func printTools(ctx context.Context, tools []tool.BaseTool) error {
	fmt.Printf("桥接到 %d 个网关聚合工具:\n", len(tools))
	for _, bt := range tools {
		info, err := toolInfo(ctx, bt)
		if err != nil {
			return err
		}
		fmt.Printf("  - %s: %s\n", info.Name, info.Desc)
	}
	return nil
}

// pickTool 按名选取桥接产物;名为空取清单首项。找不到即显式报错,不静默
// 回落——直调对象必须是调用方明确指定的工具。
func pickTool(ctx context.Context, tools []tool.BaseTool, name string) (tool.BaseTool, error) {
	if name == "" {
		if len(tools) == 0 {
			return nil, errors.New("网关聚合清单为空,无可直调工具")
		}
		return tools[0], nil
	}
	for _, bt := range tools {
		info, err := toolInfo(ctx, bt)
		if err != nil {
			return nil, err
		}
		if info.Name == name {
			return bt, nil
		}
	}
	return nil, fmt.Errorf("网关聚合清单中无工具 %q(可用清单见上方输出)", name)
}

// mustName 读取工具名用于输出;桥接产物的元信息读取失败按进程错误处理,
// 文案由 toolInfo 统一给出,此处不再重复。
func mustName(ctx context.Context, bt tool.BaseTool) string {
	info, err := toolInfo(ctx, bt)
	if err != nil {
		log.Fatal(err)
	}
	return info.Name
}

// invoke 经 Eino 工具接口直调一次:officialmcp 的桥接产物实现
// tool.InvokableTool,InvokableRun 即一次 MCP tools/call 的等价调用——
// 入参为参数 JSON 字符串,出参为应答文本。实测(v0.1.1)出参是整份
// CallToolResult 序列化的 JSON 信封(_meta/content/resultType),上游工具
// 的原文在 content[0].text;本示例原样透出,不二次拆包。这就是"从直调到
// agent"路径上模型将看到并调用的同一接口;接 ReAct 后该调用由模型决策触发。
func invoke(ctx context.Context, bt tool.BaseTool, argsJSON string) (string, error) {
	invokable, ok := bt.(tool.InvokableTool)
	if !ok {
		return "", fmt.Errorf("桥接产物 %T 未实现 tool.InvokableTool", bt)
	}
	return invokable.InvokableRun(ctx, argsJSON)
}
