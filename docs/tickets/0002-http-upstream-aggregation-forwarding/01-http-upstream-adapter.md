# 01:HTTP 上游 adapter 跑通(tracer bullet)

**Spec:** [0002 HTTP 上游连接、聚合与转发](../../../specs/0002-http-upstream-aggregation-forwarding.md)
**What to build:** 给定一个 HTTP MCP 上游(名称、URL、超时),构造出可长期使用的上游客户端:连得上(构造时完成 initialize 握手,失败即报)、列得出(ListTools 返回该上游原始 tool,名字未加前缀)、调得动(CallTool 以原始 tool 名 + 原始参数字节转发,结果原样返回)、错得清(连不上/超时/断开 → 携上游名与原因的 TransportError,不冒充 tool 业务错;上游业务失败 IsError 原样透传)、关得掉(Close 后再调用返回 ErrClosed)。假上游用 go-sdk server 在进程内搭建(httptest),测试不依赖网络与外部进程。
**Blocked by:** None (can start immediately).
**Status:** done (2026-11-03)

- [x] Client 接口落地:ListTools / CallTool / Close;参数 json.RawMessage;契约:构造时建连、并发安全、Close 后 ErrClosed(架构 §4.1)
- [x] HTTP adapter:Streamable HTTP 客户端传输,DisableStandaloneSSE=true;每上游一条长期会话;每次调用以该上游 timeout 设上下文期限(spike §2/§3 依据)
- [x] 转发参数字节级透传断言(裸 []byte 会被编成 base64,一律 RawMessage——spike 实测)
- [x] 错误契约两条:TransportError(携上游名+原因)/ 上游 IsError 应答原样透传不吞不改
- [x] adapter 单元测试:进程内假上游(go-sdk server + Streamable HTTP handler + httptest);覆盖握手成功往返、ListTools 原始清单、CallTool 往返、超时生效、Close 语义、连接失败报错
- [x] `go test ./... -race` 全绿
