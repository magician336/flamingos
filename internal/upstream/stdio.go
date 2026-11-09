package upstream

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pinnedProtocolVersion 是 stdio 握手固定的协议版本(spec 0003 用户故事
// 11):2026-07-28 版客户端握手先发 SEP-2575 无状态 server/discover,沉默
// 的老 stdio server 不应答导致握手卡死(go-sdk #1332);固定 2025-11-25
// 直走传统 initialize 规避。全仓唯一出处(spike §2 验证过的公开旋钮)。
const pinnedProtocolVersion = "2025-11-25"

// stdioClient 是 Client 接口的 stdio 子进程实现:构造即拉起子进程并完成
// initialize 握手,握手成功后的会话使用与关闭继承共享基座 sessionBase;
// SDK 机器类型(CommandTransport/ClientSession)不跨出本文件(架构 §6 D4)。
//
// 已知边界(刻意不修,范围裁决见 spec 0003 Out of Scope):上游 SDK 缺陷
// go-sdk #1209(上游发坏帧会中断整个会话)与 #855(Close 与在途调用存在
// 竞态)不在本仓修复范围;#1332 的握手卡死已由 pinnedProtocolVersion
// 规避(见上)。
type stdioClient struct {
	sessionBase
}

// NewStdio 构造 stdio 上游客户端:拉起 command 子进程并完成 initialize
// 握手,协议版本固定 pinnedProtocolVersion;子进程继承本进程的环境与
// 工作目录(简化边界,spec 0003 Implementation Decisions)。握手同样受
// timeout 约束,拉不起或握不上手的上游在构造瞬间报错而非首个请求时。
// 进程生命周期完全交给 SDK 的 CommandTransport,不自建进程管理;失败
// 返回携带上游名与原因的 *TransportError,原因点名命令以便定位配置错误。
//
// 可观测出站接线经 opts 注入:WithTelemetry 在会话上挂 Sending 中间件
// (child client span);stdio 无 HTTP 头通道,标准头 RoundTripper 不适用
// (_meta 通道由票 0004-02 增补)。不传即关闭态。
func NewStdio(ctx context.Context, name, command string, args []string, timeout time.Duration, opts ...Option) (Client, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("构造上游 %q 失败: timeout 必须为正,实际 %s", name, timeout)
	}
	o := apply(opts)
	sctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: clientImplName, Version: clientImplVersion}, nil)
	if mw := o.telemetry.SendingMiddleware(name, "stdio"); mw != nil { // nil 接线 → nil,关闭态零挂点
		client.AddSendingMiddleware(mw)
	}
	session, err := client.Connect(sctx, &mcp.CommandTransport{
		Command: exec.Command(command, args...),
	}, &mcp.ClientSessionOptions{ProtocolVersion: pinnedProtocolVersion})
	if err != nil {
		// 命令不存在、不可执行与握手失败统称"启动失败":启动 = 拉起 + 握手
		// (spec 0003 用户故事 5),一律点名命令(spec 0003 用户故事 6)。
		return nil, &TransportError{
			Upstream: name,
			Cause:    fmt.Errorf("启动 stdio 子进程 %q 失败: %w", command, err),
		}
	}
	return &stdioClient{sessionBase: sessionBase{name: name, timeout: timeout, session: session}}, nil
}
