# 02:SEP-414 `_meta` 传播(时间盒单元 4h)

**Spec:** [0004 OTel 全链路追踪与 SEP-414 `_meta` 传播](../../../specs/0004-otel-tracing-meta-propagation.md)
**What to build:** 协议原生 trace 通道接通:入站 Agent 可在请求 `_meta.traceparent` 携带 trace 上下文(与 HTTP 头双通道并存时 `_meta` 优先;Agent 自带的其他 `_meta` 键原样保留,副本修改不原地改写);出站网关向全部上游的转发请求注入 `_meta.traceparent`(W3C 值格式)——stdio 上游没有 HTTP 头,这是它唯一的 trace 通道,也是本票的存在理由;HTTP 上游呈 `_meta` + 标准头双通道并存。`_meta` 的 `map[string]any` 经 TextMapCarrier 薄适配接入官方 TraceContext propagator,两通道零手写 W3C 解析。e2e 补 stdio 子进程夹具路径:上游接收侧观察到的 `_meta.traceparent` 与网关 client span 同 trace。
**Blocked by:** 01 (标准传播全链路——复用 telemetry 骨架、出站中间件挂点与假上游观察面断言设施).
**Status:** done (2026-11-04)

- [x] `_meta`(map[string]any)的 TextMapCarrier 薄适配;入站/出站复用同一官方 TraceContext propagator
- [x] 入站:`_meta.traceparent` 提取接续;优先级 `_meta` > HTTP 头单一出处;非法/非字符串值容错忽略不炸请求;既有 `_meta` 键不覆盖(副本修改)
- [x] 出站:全部上游的出站请求注入 `_meta.traceparent`;HTTP 上游双通道并存(与票 01 的标准头同 child 上下文)
- [x] e2e:Agent 经 `_meta` 携带上下文 → 接续非新根;stdio 夹具上游(复用 spec 0003 子进程夹具模式)接收侧断 `_meta.traceparent` 与网关 client span 同 trace
- [x] 传播纯函数表驱动:合法/非法 traceparent、缺 `_meta`、值非字符串、双通道并存优先级、注入不覆盖既有键
- [x] `go test ./... -race` 全绿;`go build ./...` 成功
- [x] 时间盒纪律:4h 内未达上述标准即整票回退,保票 01 的标准头传播为降级形态,README Limitations 如实记录(spec 0004 Further Notes 的时间盒计时口径)——实际未触发回退;夹具形态偏离(独立 stdiometaserver 而非复用 stdioserver)记录于 spec 0004 修订表
