---
id: 0004
title: "OTel 全链路追踪与 SEP-414 _meta 传播"
triage: ready-for-agent
date: 2026-11-04
source: 开发计划 §3/§4 W2 + 架构 §1/§2/§3(telemetry 预留接缝与数据流挂点)/§8-1(W2 diff 纪律)+ research/05 §3.2(SEP-414 竞品 0/6)+ research/06 §7(高风险任务排最前、时间盒纪律)+ SEP-414 规范页(modelcontextprotocol.io/seps/414-request-meta,RESOURCES.md 已存)+ go-sdk v1.8.0 模块缓存源码核对
tracker-note: 本地 tracker;W4 GitHub 公开时迁为 issue 并加同名 label
---

# Spec 0004 — OTel 全链路追踪与 SEP-414 `_meta` 传播

## Problem Statement

网关已能聚合转发 http 与 stdio 两类上游(spec 0002/0003),但对一次 tool call 的处理全程不可见:没有 span,trace 上下文走到网关即断——入站不提取(Agent 已在跟的 trace 接不上),出站不注入(网关对上游的调用在 trace 树上是空洞),而 HTTP 头通道对 stdio 上游天然不存在。W2 里程碑验收"Jaeger 呈现全链路"无法达成;检索范围内六家同类网关对 SEP-414 `_meta` 传播均为 0/6(research/05 §3.2)的空白也仍是空白。

## Solution

新建 telemetry 包承担全部可观测知识,经三处装配接入既有结构:入站在网关侧 server 的 Receiving 中间件提取 trace 上下文并开启网关请求 span——`_meta.traceparent` 优先(SEP-414 协议原生),HTTP `traceparent` 头回退;出站在每个上游 client 的 Sending 中间件向请求 `_meta` 注入 `traceparent`(全部上游,stdio 唯一可用的通道),HTTP 上游另经自定义 RoundTripper 同时注入标准头;span 经 OTLP 导出,配 Jaeger compose 供人工验收全链路。`upstream.Client` 接口与 gateway 零改动,adapter 以构造选项接收出站接线,SDK 机器类型仍不跨出所属包——架构 §8-1 的 W2 diff 纪律(新包 + 装配行)为本任务的边界检验。

## User Stories

1. As Agent 用户, I want 网关从请求 `_meta.traceparent` 提取我已有的 trace 上下文, so that 网关内 span 挂进我的 trace 树而非另起一棵(SEP-414 协议原生通道)
2. As Agent 用户, I want 网关从 HTTP `traceparent` 头提取 trace 上下文, so that 用 OTel HTTP 自动埋点的 Agent 无需了解 SEP-414 也能接上
3. As Agent 用户, I want 两个通道并存时 `_meta` 优先, so that 行为确定且以协议原生载体为权威
4. As Agent 用户, I want 不带任何 trace 上下文时网关自开根 span, so that 无埋点 Agent 的流量也有网关侧视图
5. As Agent 用户, I want `_meta` 里我自带的其他键被原样保留, so that 网关注入 traceparent 不破坏既有元数据
6. As 上游作者, I want 网关转发的请求带 `_meta.traceparent`(W3C Trace Context 值格式), so that 按 SEP-414 实现的上游把 span 挂进同一棵 trace
7. As 上游作者(HTTP), I want 网关转发的 HTTP 请求同时带标准 `traceparent` 头, so that OTel HTTP 自动埋点的上游直接接上,无需解析 `_meta`
8. As 上游作者, I want 我的上游忽略 `_meta.traceparent` 时一切照旧, so that 不支持 SEP-414 的既有上游零影响
9. As Agent 用户, I want 上游不可用(点名该上游的错误)在 span 上标记为错误并记事件, so that Jaeger 里一眼区分"传输失败"与"业务失败"
10. As Agent 用户, I want 上游业务失败(IsError)在 span 上记事件但不算协议错误, so that span 状态与"协议层成功应答"的语义一致
11. As Agent 用户, I want span 属性带方法名、聚合 tool 名、命名空间、上游名与上游类型, so that Jaeger 里能按工具与上游筛选定位
12. As Agent 用户, I want tools/call 之外的方法(initialize、tools/list 等)同样有入站 span, so that 网关侧视图覆盖全部流量而非只挑调用
13. As 网关运维, I want 配置不含 `telemetry:` 段时 tracing 完全关闭, so that 不需要可观测的部署零开销、行为与现状逐字节一致
14. As 网关运维, I want 配置 `telemetry.otlp_endpoint` 后 span 经 OTLP 自动导出, so that 接自建 Jaeger 或任何 OTLP 后端
15. As 网关运维, I want `service_name` 可配(默认 flamingos), so that Jaeger 服务列表里标识正确
16. As 网关运维, I want 一条命令拉起本地 Jaeger(compose), so that 人工验收全链路不手工拼环境
17. As 网关运维, I want OTLP 后端不可达时网关照常服务, so that 可观测后端故障不反噬数据面
18. As 网关运维, I want 优雅关闭时 tracer provider 完成导出(flush), so that 停机前在途 span 不丢
19. As 开发者, I want `upstream.Client` 接口与 gateway 包零改动, so that 传输语义与可观测知识解耦(架构 §1"最大的一刀"不被侵蚀)
20. As 开发者, I want adapter 以构造选项接收出站中间件与 RoundTripper 接线、transport 以装配参数接收入站中间件, so that telemetry 知识收在 telemetry 包,其余包只留装配行(架构 §8-1)
21. As 开发者, I want SDK 机器类型(Server/ClientSession/transport)仍不跨出所属包, so that 换 SDK 版本波及面不变(架构 D4;`mcp.Middleware` 函数类型是 SDK 官方扩展点,不算机器类型)
22. As 开发者, I want W3C traceparent 的解析与生成复用 OTel 官方 TraceContext propagator(`_meta` 经 TextMapCarrier 适配), so that 边界情形(非法 traceId、版本段、大小写)交给久经考验的实现,两通道零手写解析
23. As 开发者, I want TracerProvider 单一构造处、三形态(关闭=noop / OTLP / 测试注入), so that 装配一致且测试不需要环境
24. As 开发者, I want telemetry 包不 import transport/gateway/upstream, so that 依赖方向不破(telemetry 是被装配的叶子能力)
25. As 开发者, I want `_meta` 注入以副本修改而非原地改写, so that 不污染调用链上共享的对象(不可变纪律)
26. As 开发者, I want 入站优先级(`_meta` > HTTP 头)与 span 命名规则单一出处, so that 后续调整不散改
27. As 开发者, I want SEP-414 三键中本任务只做 traceparent 的取舍显式记录, so that 时间盒裁剪有据、tracestate/baggage 留有明确接口
28. As 测试维护者, I want CI 用 tracetest.SpanRecorder 断言 span 树, so that 全链路断言不依赖 Jaeger 进程、稳定且快
29. As 测试维护者, I want 假上游在其接收侧暴露收到的 `_meta` 与 HTTP 头, so that 出站传播以黑盒方式断言贯通
30. As 测试维护者, I want 传播纯函数表驱动直测(合法/非法 traceparent、缺 `_meta`、值非字符串、双通道并存、不覆盖既有键), so that 格式边界不靠 e2e 兜底
31. As CI, I want `go build ./...` 与 `go test ./... -race` 全绿作为机械判据, so that 验收不依赖主观判断
32. As 维护者, I want "同类网关 0/6 实现 SEP-414"的差异化事实以工程语境记录(README/设计文档,出处指向公开竞品矩阵), so that 主张有据且符合发布卫生定则

## Implementation Decisions

- **模块与边界**(架构 §1 预留接缝、§2 布局"W2 建"):
  - `telemetry` 新包,暴露:TracerProvider 装配(三形态)、入站提取中间件(供 server Receiving 挂点)、出站注入中间件(供上游 client Sending 挂点,按上游名生成)、RoundTripper 包装(HTTP 头注入)、`_meta` ↔ context 传播纯函数
  - 依赖纪律:telemetry 只依赖 OTel SDK 与 mcp 协议类型/中间件函数类型,不 import transport/gateway/upstream(Story 24)
  - transport 的入站接线与 upstream adapter 的出站接线各增一个装配参数/选项,默认值 = 关闭态(noop),既有调用方与既有测试行为不变(Story 13/20)
- **入站提取**:server 的 Receiving 中间件在请求进入时读 params `_meta.traceparent` 提取远程父上下文并开启网关请求 span,处理返回后补全(错误标注与事件);HTTP 头回退在 http.Handler 外层包装完成(transport 层标准 net/http 中间件,把头里的上下文注入 ctx);两通道并存时 `_meta` 结果优先(Story 3)
- **出站注入**:上游 client 的 Sending 中间件向出站请求 params `_meta` 注入 `traceparent`(以当前 span 生成 child 上下文;副本修改);HTTP adapter 经 `StreamableClientTransport.HTTPClient` 注入自定义 RoundTripper,同一 child 上下文同时写标准头(SEP-2028 方向);stdio adapter 只有 `_meta` 通道——这正是 SEP-414 的存在理由
- **span 拓扑**:一次 Agent 请求 = 一个网关 server span;对选中上游的转发 = 其 child client span(在出站中间件内开启,上游名经构造注入);Agent 带来的父上下文在树根之上——Jaeger 呈现 Agent → 网关 → 上游的全链路
- **span 属性**:按 OTel GenAI semconv(MCP 约定)取可用键,不足处用 `flamingos.*` 私有前缀;方法名、聚合 tool 名、命名空间、上游名、上游类型必带(Story 11)。完整 semconv 对齐属进阶,时间盒内先保结构与贯通正确
- **TracerProvider 三形态**:`telemetry:` 段缺省 → noop(零开销);`otlp_endpoint` 显式配置 → OTLP 导出(gRPC);测试 → 直接注入含 SpanRecorder 的 provider。采样固定 always-on,采样策略配置不做
- **配置段**(架构 §4.3"随模块落地时增补"):`telemetry: { otlp_endpoint, service_name(默认 flamingos) }`;校验进 config 启动时一次性完成(D8):段存在而 `otlp_endpoint` 缺失/非法即配置错误
- **W3C 格式复用**:两个通道共用官方 `propagation.TraceContext` propagator;`_meta` 的 `map[string]any` 经实现 TextMapCarrier 接口的薄适配器接入(Get/Set/Keys,值断言 string)——非法值按 propagator 语义容错忽略,不炸请求(Story 22/30)
- **错误语义标注**:上游不可用(TransportError 家族)→ span 状态 Error + 事件;上游业务失败(IsError)→ 仅记事件,不改 span 错误状态(应答在协议层是成功的)(Story 9/10)
- **Jaeger 人工验收**:`observability/` 顶层目录(非 Go 模块,不入 internal 布局):docker compose(jaeger all-in-one,OTLP 接收启用)+ 使用说明;CI 不起 Jaeger(接缝方案已确认)
- **新第三方依赖**:`go.opentelemetry.io/otel`(core/propagation/exporters OTLP 系);无其他新依赖
- **时间盒纪律**:SEP-414 部分(入站 `_meta` 提取 + 出站 `_meta` 注入)为 4h 时间盒单元;超时整体砍,降级形态 = 仅 HTTP 头标准传播(HTTP 上游全链路、stdio 上游 trace 止于网关内 span),README Limitations 如实记录——标准传播已足以支撑"W2 必成项"的基线验收

## Testing Decisions

- 好测试的标准沿用 spec 0002/0003:只从接缝断言外部行为(可观察的 span 数据、假上游收到的报文与头、JSON-RPC 往返),不断言内部调用序列
- **接缝**(用户已确认,不新增自制接缝):
  - 黑盒 e2e 沿用既有模式:go-sdk 客户端 → 网关 http.Handler(httptest)→ 假上游(go-sdk server)
  - OTel 官方注入点:TracerProvider 可注入,CI 用 SDK 自带 `tracetest.SpanRecorder` 断言 span 树
  - 假上游观察面:其接收侧中间件读 `_meta.traceparent`、httptest 包装读 HTTP `traceparent` 头——断言与网关内 span 的 trace id 贯通,是"全链路接通"的机械等价物(Jaeger 不进 CI)
  - 传播纯函数(`_meta`/header ↔ context)表驱动单测
- **telemetry 单元测试**:提取/注入表驱动——合法 traceparent、非法 traceparent(容错忽略)、缺 `_meta`、值非字符串、双通道并存优先级、注入不覆盖既有键;RoundTripper 对构造请求注入头断言
- **集成/e2e 断言集**:①Agent 带 trace 上下文(头与 `_meta` 两种)→ SpanRecorder 中 server/client span 同 trace id 且父子相连,且与 Agent 注入的同 trace(接续而非新根);②假上游收到的 `_meta.traceparent` 与 HTTP 头 traceparent 同 trace 且等于网关 client span;③无上下文时网关自开根;④`telemetry:` 缺省时零 span 且既有行为回归通过;⑤stdio 假上游路径的 `_meta` 注入(复用 spec 0003 子进程夹具模式)
- 既有先例:表驱动 + httptest + go-sdk server 假上游 + `t.TempDir`/`t.Cleanup`(spec 0002/0003);既有各包测试因装配默认关闭态而零行为变化,装配行级改动除外
- 全部测试 `-race` 运行;覆盖率 ≥80% 总目标由本任务与后续任务共同逼近

## Out of Scope

- `tracestate` 与 `baggage` 传播(SEP-414 三键中只做 traceparent;取舍记录见 Story 27)
- 响应方向(Agent ← 网关)的 `_meta` trace 回传
- OTel metrics 与 logs 两类信号
- 采样策略配置(always-on 固定)
- RBAC、审计、令牌桶限流、Origin 校验(W2 另一半,spec 0005)
- ttlMs/cacheScope 缓存(W3)、trace 录制与 eval(W4;出站中间件的"录制"挂点到时再挂)
- Jaeger 进 CI、上游自身埋点(假上游只回显观察)、OTLP 后端高可用
- e2e 一致性矩阵与压测(既有裁剪决定不变)

## Further Notes

- 依据链:架构 §1(telemetry 预留接缝)/§2(布局"W2 建"、依赖单向)/§3(数据流 W2 挂点标注:ReceivingMW=trace 提取、SendingMW=span 补全)/§8-1(W2 diff 纪律:diff 仅新包 + 装配行);research/05 §3.2(SEP-414 0/6、时间窗)/§1(agentgateway 仅 HTTP 头级,最近竞距);research/06 §7(OTel+SEP-414 高风险、10-16h、无参考代码 → 时间盒);SEP-414 规范页(键名 traceparent/tracestate/baggage、W3C 值格式、对 DNS 前缀约定的豁免;其 Related SEPs 指向 SEP-2028 `_meta`→HTTP 头转发,即本任务 HTTP 出站双通道的叙事出处)
- SDK 事实(模块缓存源码核对,v1.8.0):Server/Client 均有 `AddReceivingMiddleware`/`AddSendingMiddleware`(`Middleware = func(MethodHandler) MethodHandler`);`CallToolParams` 内嵌 `Meta`(json `_meta`),params 经 `GetMeta()/SetMeta()` 读写;`StreamableClientTransport{Endpoint, HTTPClient *http.Client}` 可注 RoundTripper;SDK 无内建 OTel——"挂钩齐备、传播自建"是本任务结构
- 与架构 §3 的对应:其"SendingMW = span 补全"在本任务落为"Receiving 中间件的出段补全 + 上游 Sending 中间件的注入"——挂点等价,实现取最薄路径;架构图标注的挂点概念不变
- 完成判据(CI 可机械验证):`go build ./...` 成功;`go test ./... -race` 全绿;e2e 断言 Agent → 网关 → 上游 trace id 贯通(双通道);`telemetry:` 缺省时既有测试零回归。人工判据:observability compose 起本地 Jaeger,Inspector 发起调用后全链路可见(截图供 W4 README 使用)
- 环境注意:Fedora 44 双 Go 并存,shell 跑 go 用 /usr/local/go/bin/go 全路径;新依赖为 OTel 系小包直拉(无需镜像);docker compose 本地可用
- 时间盒计时从出站 `_meta` 注入动工起算;入站 HTTP 头标准传播不属时间盒单元(必成项)

## 修订

| 日期 | 变更 |
|---|---|
| 2026-11-04 | 发布(ready-for-agent)。发布前已确认:W2 拆两个 spec(本件=可观测 12h,策略 5h 随后另立);测试接缝方案经用户确认(黑盒 e2e + SpanRecorder + 假上游观察面 + 传播纯函数,Jaeger 人工验收) |
| 2026-11-04 | 实现完成(集成分支 otel-tracing-meta-propagation,三票 + 双轴 code-review + 五项修复)后定稿。偏离与追记:①span 属性按 spec"child client span 上游名经构造注入"分布于两个 span——server span 带 method/聚合 tool 名/命名空间(命名空间与上游名在当前装配下恒同值,单一出处注释),client span 带 method/原始 tool 名/上游名/上游类型,Story 11 的筛选诉求在该分布下成立;②otel v1.47 semconv 包无 gen_ai 键,以字面键名对齐 `gen_ai.tool.name`、其余 `flamingos.*` 前缀(spec 授权形态);③票 02 夹具为独立 stdiometaserver 而非复用 stdioserver——复用会碰 cmd 装配测试的清单恰等断言,违反既有测试零改动红线;④`otlp_endpoint` 形式语义补充:裸 host:port 与 `http://` = 明文(本地 Jaeger 开箱即用),`https://` = TLS,URL 缺端口补 4317;⑤go.mod 将 grpc/proto/otlp 提为 direct(进程内 OTLP 接收器测试所需,均系 OTLP exporter 依赖树成员);⑥复用官方 TraceContext propagator 使 tracestate 随之顺带读写(免费副作用,非承诺面;traceparent 单键承诺不变,与 Out of Scope"只做 traceparent"的取舍边界在此收窄为"不手写处理 tracestate");⑦Story 32(0/6 差异化事实的工程语境落点)延至 W4 README(当前仓库尚无 README);⑧评审修复五项:scheme 具名常量、传播纯函数收窄为包内(包外生产调用方为零)、cmd 侧跨包等价表驱动钉死 config/telemetry 两份 endpoint 形式判定(已验证钉子非永绿)、夹具更名、startSpan 命名单一出处;⑨人工验收:Jaeger 查询 API 活体确认同 trace 三 span 树(tools/call server→client、属性齐全)且优雅停机 2s 内 flush 完成,截图留 W4 发布文档补拍。已核实补充事实:otel v1.47 的 tracetest.SpanRecorder 是 SpanProcessor(WithSpanProcessor,非 WithSyncer);HTTP 上游握手走 server/discover(SEP-2575,未 pin),stdio pin 2025-11-25 走 initialize;StreamableHTTPHandler 将请求 ctx 传入 Receiving 中间件、StreamableClientTransport 以调用 ctx 构造 POST(HTTP 头通道两端贯通的底层前提) |
