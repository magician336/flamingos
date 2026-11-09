# 02:工具级审计落盘(JSON-lines,allow/deny 全记录)

**Spec:** [0005 策略层:RBAC 判定、工具级审计、Origin 校验与令牌桶限流](../../../specs/0005-policy-rbac-audit-ratelimit.md)
**What to build:** 每次 tools/call 在审计文件追加一条 JSON-lines 记录(ts/role/tool/decision/error),allow 与 deny 都记录;audit 包立包(写盘器 + Receiving 审计中间件,排在 RBAC 判定之后、从 ctx 取角色与决策),文件 append 打开且启动即校验可写(失败=启动失败);config 增 audit 子段(路径),缺省 = 关闭零行为。中间层只认 ctx 里的决策键,audit 与 policy 互不 import。
**Blocked by:** 01(RBAC 判定贯通——审计需要 ctx 中的角色与决策键,装配顺序在其后)。
**Status:** done (2026-11-05)

- [x] audit 写盘器:t.TempDir 测试断言 JSON-lines 字段与追加顺序;启动不可写路径报错中文点名
- [x] 审计中间件挂 Receiving(RBAC 之后),allow/deny 两类调用各产生一条记录,e2e 断言文件内容
- [x] config:audit 子段(路径),缺省关闭零行为;`go test ./... -race` 全绿,diff 仍不碰 gateway/upstream/telemetry
