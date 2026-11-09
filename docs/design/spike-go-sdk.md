# go-sdk v1.8.0 Spike 报告(悬而未决 U2 的裁决依据)

> 日期:2026-11-02 · 方式:源码核对(本地模块缓存)+ 可运行程序验证(非仅文档阅读)
> 复现:`go run ./spike/sdkapi`(spike 源码已入库,全部断言输出 SPIKE OK)
> 证据等级:下表"签名级结论"均经编译+运行验证;"行为级结论"经单次实测。

## 1. 结论速览

| # | 悬而未决/假设 | spike 结论 | 状态 |
|---|---|---|---|
| 1 | 版本 Pin | `github.com/modelcontextprotocol/go-sdk v1.8.0`(最新稳定版,2026-09-14;支持 2026-07-28 spec) | ✅ 定案 |
| 2 | U2:Server 构造/handler 注册/类型名 | 与设计文档 §4 草图一致;`ToolHandler` 即 2 返回值形状 | ✅ 定案 |
| 3 | U1:SDK 是否自带 middleware/hook | **存在**:`Server.AddReceivingMiddleware/AddSendingMiddleware`,`type Middleware func(MethodHandler) MethodHandler` | ✅ 有证据,U1 倾向 (a) |
| 4 | stdio 协议 Pin 2025-11-25 的旋钮 | **公开 API**:`client.Connect(ctx, t, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})`,实测协商结果=2025-11-25,无需任何 hack | ✅ 定案 |
| 5 | Stateless 模式端到端可行性 | `StreamableHTTPOptions{Stateless: true}` + `StreamableClientTransport{DisableStandaloneSSE: true}` 往返打通(Connect→ListTools→CallTool) | ✅ 实测通过 |
| 6 | 原始参数透传(接口草图 `json.RawMessage`) | 服务端 `CallToolRequest = ServerRequest[*CallToolParamsRaw]`,`.Params.Arguments` 即 `json.RawMessage`,零转换透传实测字节一致 | ✅ 定案 |
| 7 | `mcp/cache.go` 87 行缓存参考 | 存在,`wc -l` 恰为 87 行 | ✅ 核实 |

## 2. 验证过的 API 形状(签名级)

```go
// 服务端
mcp.NewServer(impl *Implementation, options *ServerOptions) *Server
server.AddTool(t *Tool, h ToolHandler)            // 非泛型:代理场景用这个
type ToolHandler func(context.Context, *CallToolRequest) (*CallToolResult, error)
server.Run(ctx context.Context, t Transport) error // 阻塞;一个 Server 可挂多个 transport/session

// 服务端 handler 拿到的请求
type CallToolRequest = ServerRequest[*CallToolParamsRaw]
type CallToolParamsRaw struct { Name string; Arguments json.RawMessage; ... }

// 工具与结果
type Tool struct { Meta; Name; Description; InputSchema any; ... }  // InputSchema 是 any
type CallToolResult struct { Meta; Content []Content; StructuredContent; IsError bool; ... }
type TextContent struct { Text string; Meta; Annotations }

// 客户端
mcp.NewClient(impl *Implementation, options *ClientOptions) *Client
client.Connect(ctx, t Transport, opts *ClientSessionOptions) (*ClientSession, error)
type ClientSessionOptions struct { ProtocolVersion string }  // 留空=最新(2026-07-28)
cs.ListTools(ctx, *ListToolsParams) (*ListToolsResult, error)
cs.CallTool(ctx, *CallToolParams) (*CallToolResult, error)   // CallToolParams.Arguments 是 any
cs.InitializeResult() *InitializeResult                       // .ProtocolVersion 即协商结果

// 传输
mcp.NewInMemoryTransports() (*InMemoryTransport, *InMemoryTransport)
mcp.NewStreamableHTTPHandler(getServer func(*http.Request) *Server, opts *StreamableHTTPOptions)
type StreamableHTTPOptions struct { Stateless bool; ... }     // Stateless:仅 POST,GET/DELETE→405
type StreamableClientTransport struct { Endpoint string; DisableStandaloneSSE bool; MaxRetries int; ... } // 字面量构造,无 New*

// 中间件(U1 证据)
type Middleware func(MethodHandler) MethodHandler
server.AddReceivingMiddleware(middleware ...Middleware)       // 自右向左包裹
server.AddSendingMiddleware(middleware ...Middleware)
```

协议版本机制(源码 `shared.go:40-110`):常量含 2026-07-28/2025-11-25/2025-06-18/2025-03-26/2024-11-05;**2026-07-28 时客户端先走 SEP-2575 的无状态 `server/discover`,被拒再回退传统 initialize 握手**——这解释了新旧服务端混布时的实际行为。

## 3. 行为级发现(实测,写码时必须遵守)

1. **`AddTool` 缺 `InputSchema` 直接 panic**(`AddTool "x": missing input schema`)。代理转发上游 tool 前必须兜底:上游无 schema 时替换为 `map[string]any{"type":"object"}`。实测确认。
2. **`AddTool` 对非法 tool 名只打日志不拒绝**(软失败)。命名空间前缀校验必须由 gateway 自己做,不能指望 SDK 兜底。名称规则实测兼容:非空、≤128、字符集 `[a-zA-Z0-9_-]`——`ns_原名` 放行。
3. **`CallToolParams.Arguments` 是 `any`:裸 `[]byte` 会被 encoding/json 编成 base64**。spike 首跑实测踩中(echo 回了 base64),转型 `json.RawMessage(args)` 后字节级一致。网关构造转发参数的标准写法:一律 `json.RawMessage`。
4. **`InputSchema` 线路解码后是 `map[string]any`**;`AddTool` 接受 map(校验 `type=object`)——schema 从上游到注册零转换可行。

## 4. 对设计文档的三处回写

| 位置 | 回写内容 |
|---|---|
| §7-U1 | 已有证据:SDK 自带 Receiving/Sending middleware 且形状匹配架构 §3 数据流概念 → **U1 就此定案选 (a)**,W2 直接用 SDK 机制,不造自定义 hook 链 |
| §7-U2 | 定案:Pin v1.8.0;草图类型名与实测一致,无需改动 §4 |
| §4.1 upstream | 补充实现注意:stdio adapter 用 `ClientSessionOptions{ProtocolVersion: "2025-11-25"}`;HTTP adapter 用 `DisableStandaloneSSE: true`;转发参数一律 `json.RawMessage`;注册前兜底空 schema + 自校验名称 |

## 5. 未验证边界(诚实声明)

- 仅为**单上游单 tool** 的 API 验证:聚合、命名空间、并发多上游属聚合实现任务的工作,spike 不覆盖;
- `DisableStandaloneSSE: true` 下 server 主动通知(tools/list_changed)不可达——对本项目无影响(不做订阅聚合);
- stdio 真实子进程路径(ExecTransport/Cmd)未在 spike 中跑通,属 stdio 任务时间盒范围;三个 open bug(#1332/#1209/#855)的复现与规避留待 stdio 任务首日验证;
- SEP-2575 `server/discover` 回退路径未单独验证(网络型上游非 2026-07-28 服务端时触发)。
