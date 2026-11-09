# 01:RBAC 判定贯通(角色头 → ctx → policy 匹配 → deny -40001)

**Spec:** [0005 策略层:RBAC 判定、工具级审计、Origin 校验与令牌桶限流](../../../specs/0005-policy-rbac-audit-ratelimit.md)
**What to build:** Agent 在 HTTP 头声明角色(头名可配,缺省 mcp-role;未带头落默认角色),配置按角色声明聚合工具名通配规则(allow/deny);tools/call 在 mcp 层 Receiving 中间件按 deny 优先、fail-close 判定——放行照旧转发,拒绝返回 JSON-RPC 错误 -40001(信息点名角色与工具)且 span 标注错误;initialize/tools/list 等其他方法不判。policy 包立包(匹配纯函数 + 判定中间件,决策经 ctx 传递),config 增 policy 段(rbac 子段),cmd 装配;段缺省 = 关闭态全放行,行为与现状逐字节一致。
**Blocked by:** None (can start immediately).
**Status:** done (2026-11-05)

- [x] policy 匹配纯函数:`角色×工具名→决策`,deny 命中即拒、未命中 allow 即拒、未知角色拒(fail-close);通配三形态(`*`/`ns_*`/精确)表驱动穷举
- [x] 角色提取中间件进 transport(HTTP 头→ctx,专用 key 类型);policy 判定中间件挂 Receiving(在 spec 0004 trace 中间件之后),SDK 源码核对 AddReceivingMiddleware 顺序语义后钉进 e2e
- [x] 拒绝返回 JSON-RPC 错误 -40001(中文点名角色与工具),e2e 断言 allow 往返成功、deny 收 -40001、span 上错误可见(SpanRecorder)
- [x] config:policy 段 + rbac 子段(头名/默认角色/规则表),校验启动即败(中文点名位置,通配语法正反例表驱动)
- [x] diff 纪律:gateway/upstream/telemetry/audit 零改动;关闭态既有测试零改动通过;`go test ./... -race` 全绿
