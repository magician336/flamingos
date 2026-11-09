// 判据②:协议版本组合握手(spec 0007 用户故事 19)。SDK v1.8.0
// supportedProtocolVersions 五版为声明支持面,本套件钉三个代表组合加一例
// 伪造的未支持版本(协商语义实测),不做 5×N 矩阵(spec 0007
// Implementation Decisions)。各例共享同一真实网关拓扑、各自独立握手——
// 协议版本是会话属性而非进程属性。
package e2e

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestProtocolVersionHandshake 复核"客户端指定协议版本"的协商在真实网关
// 二进制上的行为:SDK 客户端的公开旋钮是 ClientSessionOptions.ProtocolVersion
// (v1.8.0 mcp/client.go:250)——带该旋钮的黑盒连接即版本判据的全部手段,
// 无需裸 HTTP 注入。注意 SDK 语义:客户端声明 2026-07-28 走 SEP-2575 的
// server/discover 路径(SDK 服务端支持,协商结果仍为 2026-07-28);更老
// 两版走 legacy initialize,服务端按"支持即回显"协商;伪造的未支持版本
// 亦走 legacy initialize,服务端按"不支持即回最新支持版"降级(见对应
// 子测试内的实测记录)。
func TestProtocolVersionHandshake(t *testing.T) {
	fixtureAddr := freeAddr(t)
	startHTTPFixture(t, fixtureAddr)
	gwAddr := freeAddr(t)
	endpoint := startGateway(t, gwAddr, configYAML(gwAddr,
		httpUpstreamYAML("calc", "http://"+fixtureAddr+"/mcp"),
		stdioUpstreamYAML("fs", stdioFixtureBin),
	))

	t.Run("2026-07-28:最新版握手并全链调用", func(t *testing.T) {
		agent := connectAgent(t, endpoint, "2026-07-28")
		if got := agent.InitializeResult().ProtocolVersion; got != "2026-07-28" {
			t.Fatalf("最新版握手应协商为 2026-07-28,实际 %q", got)
		}
		list, err := agent.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatalf("最新版 tools/list 失败: %v", err)
		}
		if len(list.Tools) != 8 {
			t.Fatalf("最新版全链 tools/list 应聚合两上游 8 工具,实际 %d", len(list.Tools))
		}
		args := json.RawMessage(`{"v":"2026-07-28"}`)
		res, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "calc_echo", Arguments: args,
		})
		if err != nil {
			t.Fatalf("最新版 tools/call 失败: %v", err)
		}
		if got := textOf(res); got != string(args) {
			t.Errorf("最新版 tools/call 往返应一致,发送 %q 实际 %q", string(args), got)
		}
	})

	t.Run("2025-11-25:上一代版握手并混合上游链路调用", func(t *testing.T) {
		agent := connectAgent(t, endpoint, "2025-11-25")
		if got := agent.InitializeResult().ProtocolVersion; got != "2025-11-25" {
			t.Fatalf("握手应协商为 2025-11-25,实际 %q", got)
		}
		// 混合上游链路:http 上游默认协商,stdio 上游协议 Pin 2025-11-25
		// (upstream 包 pinnedProtocolVersion,防老 server 握手卡死)——
		// 两通道各走一通调用,链路在上一代协议下可用。
		args := json.RawMessage(`{"v":"2025-11-25"}`)
		res, err := agent.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "calc_echo", Arguments: args,
		})
		if err != nil {
			t.Fatalf("2025-11-25 链路调 http 上游失败: %v", err)
		}
		if got := textOf(res); got != string(args) {
			t.Errorf("2025-11-25 链路 calc_echo 往返应一致,实际 %q", got)
		}
		res, err = agent.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "fs_echo", Arguments: args,
		})
		if err != nil {
			t.Fatalf("2025-11-25 链路调 stdio 上游失败: %v", err)
		}
		if got := textOf(res); got != string(args) {
			t.Errorf("2025-11-25 链路 fs_echo 往返应一致,实际 %q", got)
		}
	})

	t.Run("2025-06-18:更老版握手成功", func(t *testing.T) {
		agent := connectAgent(t, endpoint, "2025-06-18")
		if got := agent.InitializeResult().ProtocolVersion; got != "2025-06-18" {
			t.Fatalf("握手应协商为 2025-06-18,实际 %q", got)
		}
	})

	t.Run("1999-01-01:伪造的未支持版本按协商语义降级", func(t *testing.T) {
		// 伪造的未支持版本实测(spec Implementation Decisions 钉定的"伪造
		// 版本行为实测记录"):客户端报服务端不支持的自造版本时握手不报
		// 错,按协商语义静默降级。SDK v1.8.0 源码路径与本例真网关实测一
		// 致:mcp/client.go:314 请求版经 ClientSessionOptions.ProtocolVersion
		// 生效,字符串比较 "1999-01-01" < "2026-07-28" 故不经 SEP-2575
		// server/discover、直接走 legacy initialize;mcp/shared.go:76
		// negotiatedVersion 对不在支持面的请求版回"服务端支持面中小于
		// 2026-07-28 的最新版"(MCP 规范:不支持时 MUST 回另一支持版,
		// SHOULD 为最新支持版)= 2025-11-25;mcp/client.go:395 校验应答
		// 版在 SDK 五版支持面内,会话成立。
		agent := connectAgent(t, endpoint, "1999-01-01")
		if got := agent.InitializeResult().ProtocolVersion; got != "2025-11-25" {
			t.Fatalf("伪造的未支持版本应降级为服务端最新支持版 2025-11-25,实际 %q", got)
		}
	})
}
