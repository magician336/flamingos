# 03:Origin 校验与令牌桶限流(transport 内部)+ 全装配收口

**Spec:** [0005 策略层:RBAC 判定、工具级审计、Origin 校验与令牌桶限流](../../../specs/0005-policy-rbac-audit-ratelimit.md)
**What to build:** transport 内部新增两道 HTTP 中间件:①Origin 校验——配置允许列表(支持通配端口)不匹配返回 403(MCP Streamable HTTP 规范要求,防 DNS rebinding),未配置=关闭;②令牌桶限流——全局单桶(rps+burst 可配),桶空返回 429 + Retry-After(按补充速率计算),时钟经构造注入以便确定性测试。config 增 origin 与 rate_limit 子段,cmd 装配收口;HTTP 中间件链顺序钉死:Origin → 限流 → 角色提取 → SDK handler。**降级裁决:本票超时砍限流保 Origin,Origin 属 MCP 规范要求项不可砍;降级发生须回写 spec 修订表。**
**Blocked by:** 02(工具级审计落盘——HTTP 中间件链与装配在同一处收口)。
**Status:** done (2026-11-05)

- [x] Origin 中间件:允许列表通配端口匹配纯函数表驱动;不匹配 403;未配置零行为;e2e 断言
- [x] 令牌桶:注入假时钟表驱动(连续消费/按速率恢复/burst 上限);桶空 429+Retry-After;e2e 断言
- [x] config:origin 与 rate_limit 子段校验启动即败(rps>0 等);四子段缺省全关闭回归通过
- [x] 收口:全装配 e2e(放行/拒绝/403/429/审计五行各一例);`go build ./...` + `go test ./... -race` 全绿;diff 纪律终检(新包×2 + transport 内部 + config + cmd 装配行)
