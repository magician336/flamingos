package upstream

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// httpClient 是 Client 接口的 Streamable HTTP 实现:每个上游一条长期会话,
// 构造时完成 initialize 握手(架构 §3:Agent 侧 Stateless 与 upstream 侧
// 长连接并存)。握手成功后的会话使用与关闭继承共享基座 sessionBase;
// SDK 机器类型(Client/ClientSession/Transport)不跨出本包(架构 §6 D4)。
type httpClient struct {
	sessionBase
}

// NewHTTP 构造指向 endpoint 的 HTTP 上游客户端并完成 initialize 握手:
// 握手同样受 timeout 约束,连不上的上游在构造瞬间报错而非首个请求时。
// 失败返回携带上游名与原因的 *TransportError。
//
// 可观测出站接线经 opts 注入(spec 0004 票 01):WithTelemetry 在会话上
// 挂 Sending 中间件(child client span)并向 transport 注入标准头
// RoundTripper;不传即关闭态,行为与未引入 telemetry 前逐字节一致。
func NewHTTP(ctx context.Context, name, endpoint string, timeout time.Duration, opts ...Option) (Client, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("构造上游 %q 失败: timeout 必须为正,实际 %s", name, timeout)
	}
	o := apply(opts)
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: clientImplName, Version: clientImplVersion}, nil)
	if mw := o.telemetry.SendingMiddleware(name, "http"); mw != nil { // nil 接线 → nil,关闭态零挂点
		client.AddSendingMiddleware(mw)
	}
	transport := &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		DisableStandaloneSSE: true, // 网关不维持常驻 SSE 流(spike §2 已验证)
	}
	if rt := o.telemetry.RoundTripper(nil); rt != nil { // 同一 child 上下文注入标准 traceparent 头
		transport.HTTPClient = &http.Client{Transport: rt}
	}
	session, err := client.Connect(hctx, transport, nil)
	if err != nil {
		return nil, &TransportError{Upstream: name, Cause: err}
	}
	return &httpClient{sessionBase: sessionBase{name: name, timeout: timeout, session: session}}, nil
}
