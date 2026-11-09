# 01:标准传播全链路(HTTP 头 traceparent 贯通 Agent→网关→上游)

**Spec:** [0004 OTel 全链路追踪与 SEP-414 `_meta` 传播](../../../specs/0004-otel-tracing-meta-propagation.md)
**What to build:** 一次 tools/call 的 trace 不再断在网关:Agent 带标准 HTTP `traceparent` 头调用网关,网关提取上下文并开启网关请求 span(全部 JSON-RPC method 覆盖,属性带 method、聚合 tool 名、命名空间、上游名、上游类型),转发 HTTP 上游时经 RoundTripper 注入标准头、开启 child client span;上游不可用(点名该上游的错误)在 span 标 Error 并记事件,上游业务失败(IsError)仅记事件不改错误状态;无上下文时网关自开根 span。telemetry 新包承担全部可观测知识(TracerProvider 的注入/noop 形态、header↔context 传播纯函数、入站/出站中间件、RoundTripper),transport 与 upstream 各以一个装配参数/选项接线且默认关闭态;CI 用 tracetest.SpanRecorder + 假上游观察面断言 Agent→网关→上游 trace id 贯通。
**Blocked by:** None (can start immediately).
**Status:** done (2026-11-04)

- [x]telemetry 包成形:TracerProvider 注入与 noop 两形态、header↔context 传播纯函数(复用官方 TraceContext propagator);包不 import transport/gateway/upstream
- [x]transport 入站接线(构造参数,默认=noop 关闭态):http.Handler 外层包装提取 HTTP `traceparent` 头;server Receiving 中间件开网关请求 span 并在出段补全——上游不可用 → span Error + 事件;IsError → 仅记事件不改错误状态
- [x]span 属性集:method、聚合 tool 名、命名空间、上游名、上游类型(GenAI semconv 可用键优先,不足处 `flamingos.*` 前缀)
- [x]upstream 出站接线(构造选项,默认关闭态):Sending 中间件开 child client span(上游名经构造注入)、HTTP 上游经自定义 RoundTripper 注入标准 `traceparent` 头;`upstream.Client` 接口与 gateway 零改动;SDK 机器类型不出所属包(架构 D4)
- [x]e2e:Agent 带 traceparent 头 → SpanRecorder 断 server/client span 同 trace 且父子相连、与 Agent 注入的同 trace(接续非新根);假上游观察面收到的头 trace id 与 client span 一致;无上下文时网关自开根
- [x]关闭态回归:不接 telemetry 时零 span,既有全部测试零改动零回归(装配默认值保证)
- [x]`go test ./... -race` 全绿;`go build ./...` 成功
