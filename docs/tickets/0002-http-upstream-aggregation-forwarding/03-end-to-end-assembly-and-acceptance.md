# 03:端到端跑通——transport 装配、真实启动与双假上游验收

**Spec:** [0002 HTTP 上游连接、聚合与转发](../../../specs/0002-http-upstream-aggregation-forwarding.md)
**What to build:** 写好两个 HTTP 假上游的配置,启动网关:真实监听单一端点(POST /mcp,Stateless,GET/DELETE → 405),initialize 握手以 flamingos 身份自报;一次 tools/list 看到双上游带前缀聚合清单;一次 tools/call 经网关路由转发,结果与直连上游一致;Ctrl+C/SIGTERM 优雅退出(停服务、断开全部上游);配置含 stdio 条目时启动即败并明确提示"暂未支持";杀掉一个假上游后,指向它的调用得到点名"不可用",另一上游照常。集成测试全部进程内完成,作为 W1 周验收的机械等价物。
**Blocked by:** [01 HTTP 上游 adapter 跑通](01-http-upstream-adapter.md)、[02 gateway 聚合与路由](02-gateway-aggregation-routing.md)
**Status:** done (2026-11-03)

- [x] transport:go-sdk server + Stateless Streamable HTTP handler(仅 POST,GET/DELETE → 405);对外暴露 http.Handler,监听地址构造注入
- [x] 启动时快照注册:调 HandleToolsList 取聚合清单,逐个 AddTool,转发 handler 统一走 HandleToolsCall;缺 InputSchema 兜底空 object schema(防 panic,spike 实测)
- [x] cmd 装配升级:Load → 构造全部上游客户端(HTTP;stdio 条目即败,中文提示"暂未支持(下一任务)")→ gateway → transport → 监听;config 翻译只发生在 cmd(D7);启动时输出上游装配摘要(沿用 0001 渲染)
- [x] 信号处理:SIGINT/SIGTERM 优雅关闭——停 server、逐个 Close 上游后退出
- [x] 集成测试(进程内黑盒):两个假上游(go-sdk server + httptest)+ 真实 adapter + transport;go-sdk 客户端对网关 initialize → tools/list → tools/call 完整往返;聚合清单含双上游带前缀 tool;调用结果与直连上游一致;关闭一个假上游 → 该上游调用"不可用"、另一上游正常(故障隔离)
- [x] `go build ./...` 成功;`go test ./... -race` 全绿
- [x] spec 0002 勾选完成判据、修订记录追记
