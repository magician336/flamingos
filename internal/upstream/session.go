package upstream

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 客户端在 initialize 握手中对上游自报的身份(与网关对 Agent 侧自报的
// 身份是两回事,后者属 transport 层);两个 adapter 的构造共用。
const (
	clientImplName    = "flamingos"
	clientImplVersion = "0.1.0"
)

// sessionBase 是 httpClient 与 stdioClient 共享的会话生命周期基座:两类
// adapter 的差异只在构造(连接方式与握手参数),握手成功后的会话使用
// 完全同形——锁内取会话锁外调用、每次调用以该上游 timeout 设上下文
// 期限、Close 幂等 + 关闭后 ErrClosed。SDK 机器类型(ClientSession)
// 不出本包(架构 §6 D4)。
type sessionBase struct {
	name    string        // 上游名称,仅用于错误文案点名
	timeout time.Duration // 该上游的调用超时,每次调用设上下文期限
	session *mcp.ClientSession

	mu     sync.Mutex
	closed bool
}

// ListTools 拉取该上游的原始 tool 清单(名字未加命名空间前缀)。
// 每次调用以该上游的 timeout 设上下文期限,挂死上游不拖死调用方。
func (b *sessionBase) ListTools(ctx context.Context) ([]*mcp.Tool, error) {
	session, err := b.currentSession()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	res, err := session.ListTools(ctx, nil)
	if err != nil {
		return nil, &TransportError{Upstream: b.name, Cause: err}
	}
	return res.Tools, nil
}

// CallTool 以原始 tool 名与原始参数字节调用上游,结果原样返回:
// 上游正常应答但业务失败(IsError=true)时同样原样返回且 err 为 nil
// (错误契约②,不吞不改);args 保持 json.RawMessage 直传 SDK,零转换
// (裸 []byte 会被编成 base64,spike 实测)。
func (b *sessionBase) CallTool(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	session, err := b.currentSession()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, &TransportError{Upstream: b.name, Cause: err}
	}
	return res, nil
}

// currentSession 在锁内取当前会话:已 Close 时返回 ErrClosed;
// 取出后在锁外调用,避免慢调用阻塞 Close。
func (b *sessionBase) currentSession() (*mcp.ClientSession, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, ErrClosed
	}
	return b.session, nil
}

// Close 关闭底层会话;幂等,重复调用返回 nil。Close 后再调用
// ListTools/CallTool 返回 ErrClosed。stdio 会话沿 SDK CommandTransport
// 内建的"关 stdin → 等待(默认 5s)→ SIGTERM → SIGKILL"分级终止链
// 体面退出子进程,Close 返回即进程已退出,不留孤儿(spec 0003 用户故事 8)。
func (b *sessionBase) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	session := b.session
	b.session = nil
	return session.Close()
}
