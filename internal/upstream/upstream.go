// Package upstream 提供"一个可调用的上游 MCP server"的抽象:Client 接口
// 与其 HTTP adapter 实现。gateway 只依赖接口;传输差异(连接方式、生命周期、
// SDK 机器类型)全部封在 adapter 内部,不跨出本包(架构 §1/§6 D4)。
package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ErrClosed 表示客户端已 Close,此后任何调用都返回它;调用方用 errors.Is 判别。
var ErrClosed = errors.New("上游客户端已关闭")

// Client 是"一个可调用的上游 MCP server"(架构 §4.1,项目最重要的一份接口)。
// 实现:HTTP adapter(Streamable HTTP)与 stdio adapter(子进程,协议版本
// 固定 2025-11-25);公共会话逻辑在包内 sessionBase,两个 adapter 只含传输差异。
//
// 契约(接口的一部分,调用方必须知道):
//   - 构造函数建立底层连接并完成 initialize 握手,失败即报;
//   - 实现并发安全;
//   - Close 后再调用任何方法返回 ErrClosed。
type Client interface {
	// ListTools 返回该上游全部 tool 的原始描述——名字未加命名空间前缀,
	// 描述与输入 schema 原样保留,由 gateway 负责加前缀聚合。
	// (元素为指针:SDK 线路形状即 []*Tool,零转换透传,设计 D4。)
	ListTools(ctx context.Context) ([]*mcp.Tool, error)

	// CallTool 用"原始 tool 名"调用;args 为上游期望的原始参数字节。
	// args 一律 json.RawMessage 透传(裸 []byte 会被 encoding/json 编成
	// base64,spike 实测);上游正常应答但业务失败(IsError=true)时结果
	// 原样返回、err 为 nil,不吞不改(错误契约②)。
	CallTool(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error)

	// Close 释放底层连接;幂等,重复调用返回 nil。
	Close() error
}

// TransportError 表示上游在传输层不可达:连不上、超时或中途断开(错误契约①)。
// 调用方(gateway)据此向 Agent 报"上游不可用",绝不冒充 tool 本身的业务错误
// ——业务失败走 IsError 原样透传,不会出现在这里。
type TransportError struct {
	// Upstream 是上游名称(即命名空间),用于点名故障上游。
	Upstream string
	// Cause 是传输失败的底层原因(网络错误、超时、断开等)。
	Cause error
}

// Error 实现 error:中文文案点名上游与根因,统一"传输失败"措辞。
// 超时不在此单列文案:spec 只要求 Agent 侧(gateway 的"不可用"错误)
// 单列超时,此处的超时判别由 Unwrap 后 errors.Is(ctx.Err()) 承担。
func (e *TransportError) Error() string {
	return fmt.Sprintf("上游 %q 传输失败: %v", e.Upstream, e.Cause)
}

// Unwrap 暴露底层原因,使 errors.Is/As 可穿透判别(如超时、连接拒绝)。
func (e *TransportError) Unwrap() error {
	return e.Cause
}
