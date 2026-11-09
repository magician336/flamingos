---
id: 0005
title: "策略层:RBAC 判定、工具级审计、Origin 校验与令牌桶限流"
triage: ready-for-agent
date: 2026-11-05
source: 开发计划 §2/§3/§4(RBAC+审计+限流 5h,自研 ~200 行,降级=砍限流保 RBAC+审计)+ 架构 §1/§2(transport 中间件链与 ReceivingMW 策略挂点)/§6(D1 分包纪律)/§8-1(W2 diff 纪律)/开放问题 U3(本次裁决:Origin/限流进 transport 内部)+ research/02 §安全(Streamable HTTP 要求校验 Origin 403;OAuth 资源服务器语义)+ research/06 §6(RBAC0:principal×tool 通配→allow/deny;不引 cerbos/CEL 的取舍证据)+ MCP 2026-07-28 规范(Streamable HTTP Origin 校验要求;应用自定义错误码应分配在 -32768..-32000 之外)+ 会话内 AskUserQuestion 两项确认(角色来自 HTTP 头;Origin/限流进 transport)
tracker-note: 本地 tracker;W4 GitHub 公开时迁为 issue 并加同名 label
---

# Spec 0005 — 策略层:RBAC 判定、工具级审计、Origin 校验与令牌桶限流

## Problem Statement

网关已能聚合转发并对每次调用做全链路追踪(spec 0002/0003/0004),但对"谁可以调用什么"没有任何控制:任何连到端点的客户端都能调用全部上游工具;调用了什么、被没被放行,没有任何记录可查;浏览器型客户端直连端点存在 MCP 规范明确要求防御的 Origin 伪造(DNS rebinding)风险;突发流量没有任何保护,上游会被打挂。网关作为"策略执行点"的核心卖点(research/02:tool 级权限与审计是 agent 攻防新议题;计划 §0.1:显式暴露"中间件平台工程")当前为零。

## Solution

在不碰 gateway/upstream/telemetry 任何既有代码的前提下,新增两包 + transport 内部中间件链,四层防护一次装配:

- **RBAC(新包 policy)**:Agent 以 HTTP 头声明角色(会话确认;头名可配,缺省 `mcp-role`;未带头落默认角色),配置声明 RBAC0 规则(角色 × 聚合工具名通配 → allow/deny);tools/call 在 mcp 层 Receiving 中间件判定,拒绝返回应用自定义 JSON-RPC 错误 -40001(规范建议应用自定义码分配在 -32768..-32000 之外)。自研规则匹配 ~百行,不引 cerbos(需独立部署)/CEL(第四门新技术)——取舍本身即 research/06 §6 的既定答案。
- **工具级审计(新包 audit)**:每次 tools/call 一条 JSON-lines 记录落盘(时间、角色、聚合工具名、判定 allow/deny、结果错误与否),文件 append、启动即校验可写;段缺省=关闭。
- **Origin 校验(transport 内部,裁决 U3)**:配置 allowed 列表按 MCP Streamable HTTP 规范要求校验 Origin 头,不匹配 403;未配置=关闭(放行)。
- **令牌桶限流(transport 内部,裁决 U3)**:全局单桶(rps+burst 可配),桶空返回 429;时钟经构造注入以便确定性测试;超时降级首个被砍项(计划 §3)。

四能力各自子段缺省=关闭、零开销,沿用 spec 0004 的三形态装配纪律;新第三方依赖为零。

## User Stories

1. As Agent 用户, I want 在请求头声明角色(mcp-role), so that 网关按我声明的角色做工具级放行判定
2. As Agent 用户, I want 不带头时按默认角色判定, so that 最简客户端零配置可用
3. As Agent 用户, I want 被拒调用收到明确错误(code -40001,信息点名角色与工具), so that 能立即定位"是策略拒绝而非工具不存在"
4. As Agent 用户, I want 被拒调用同样有完整追踪 span 且标注错误, so that 被拒流量在 Jaeger 里可见可排查
5. As Agent 用户, I want 未被策略覆盖的工具默认拒绝(deny 优先于 allow), so that 新增上游工具不会静默开放
6. As Agent 用户, I want 限流触发时收到 429 与 Retry-After, so that 客户端能退避重试而不是无限重试
7. As 浏览器型客户端用户, I want 非允许 Origin 的请求被 403, so that 我的页面不会被 DNS rebinding 攻击滥用
8. As 合规/排查人员, I want 每次 tools/call 一条 JSON-lines 审计记录(时间/角色/工具/判定/结果), so that 事后能回答"谁在何时调了什么、放没放行"
9. As 合规/排查人员, I want 审计文件 append 且启动即校验可写, so that 磁盘/权限问题在启动瞬间暴露而非静默丢记录
10. As 网关运维, I want 用配置声明规则(角色×通配 allow/deny), so that 不改代码就能调整放行策略
11. As 网关运维, I want 四个能力子段各自缺省=关闭、行为与现状逐字节一致, so that 不需要策略的部署零开销零风险
12. As 网关运维, I want 规则语法错误/参数非法启动即败(中文点名位置), so that 配置错误活不到被流量发现
13. As 网关运维, I want 令牌桶参数(rps/burst)可配, so that 按上游承受能力定阈值
14. As 网关运维, I want Origin 允许列表可配(含通配端口), so that 本地开发与生产各自收紧
15. As 网关运维, I want 限流与认证解耦(全局单桶), so that 5h 时间盒内先拿到最薄但真实的保护
16. As 开发者, I want RBAC 判定为纯函数(角色+工具名→决策), so that 规则引擎可以表驱动穷举而无需网络
17. As 开发者, I want diff 纪律兑现(仅 policy/audit 两新包 + transport 内部中间件 + config 段 + cmd 装配行), so that gateway/upstream/telemetry 零改动(架构 §8-1,加 RBAC 那天的验收判据)
18. As 开发者, I want 决策结果经 ctx 在中间件间传递, so that RBAC 与审计与 span 三方对同一次判定口径一致
19. As 开发者, I want 不引 cerbos/CEL 的事实与理由以工程语境记录, so that 取舍可答辩(独立部署/新技术成本 vs RBAC0 需求)
20. As 测试维护者, I want 令牌桶时钟经构造注入, so that 限流测试确定性(不 sleep)
21. As 测试维护者, I want 审计写入 t.TempDir, so that 测试零残留
22. As 测试维护者, I want 关闭态零行为回归由既有测试零改动自证, so that 装配纪律有机械判据
23. As CI, I want `go build ./...` 与 `go test ./... -race` 全绿为机械判据, so that 验收不依赖主观
24. As 维护者, I want 降级形态显式记录(超时砍限流;Origin 属 MCP 规范要求优先保), so that 时间盒裁决有据可查

## Implementation Decisions

- **模块与边界**(架构 §1 预留接缝、§6 D1、U3 本次裁决):
  - `policy` 新包:规则匹配纯函数(角色, 聚合工具名 → 决策)+ Receiving 判定中间件;规则来源经构造注入;不 import transport/gateway/upstream/audit
  - `audit` 新包:JSON-lines 写盘器 + Receiving 审计中间件;不 import 上述各包
  - `transport` 内部:Origin 校验中间件与令牌桶限流中间件(HTTP 层机制,不立独立包——U3 裁决;含"角色头提取"小中间件,把声明的角色放进 ctx 供 mcp 层消费)
  - 装配链(HTTP 层):Origin 校验 → 令牌桶限流 → 角色提取 → SDK handler;mcp 层(spec 0004 已装的 trace Receiving 之后):RBAC 判定 → 审计落盘。安装顺序=洋葱外层在前,由 e2e 钉住
  - 依赖纪律:policy/audit 互不依赖、均不 import 既有业务包;决策结果以 ctx 键传递(专用 key 类型)
- **principal(角色)来源**(会话确认):HTTP 头声明,头名可配(缺省 `mcp-role`),未带头取配置的 default_role;网关无认证是如实记录的边界(OAuth/RFC 9728/8707 资源服务器语义属 Out of Scope),自报角色 = 策略层的诚实起点
- **RBAC0 规则语义**(research/06 §6):配置按角色列 allow/deny 通配表(聚合工具名,`*` 全量 / `ns_*` 前缀 / 精确);判定 = 同一角色下 **deny 命中即拒;未命中 allow 即拒;否则放行**(fail-close,Story 5);tools/call 以外的 method(initialize/tools/list/discover)不判放行;`tools/list` 不做未授权工具过滤(已知边界,Out of Scope 记录)
- **拒绝语义**:tools/call 被拒 → JSON-RPC error,code **-40001**(应用自定义;MCP 规范建议新码分配在 -32768..-32000 之外),message 中文点名角色与工具名;span 按既有错误语义标注 Error(spec 0004 completeSpan 零改动即覆盖);限流 → HTTP 429 + Retry-After;Origin 不匹配 → 403(MCP Streamable HTTP 规范要求)
- **审计记录**:JSON-lines,字段 `ts/role/tool/decision/error`;tools/call 级粒度(mcp 层落盘——计划 §2 原画在 HTTP 中间件链,修订为 mcp 层以拿到工具名与判定,修订表记录);文件 append 打开,启动即校验可写(失败=启动失败,Story 9);段缺省=关闭
- **令牌桶**:全局单桶,`rps`+`burst` 两参数;time 来源经构造注入(测试注入假时钟);桶空 → 429 + Retry-After(按 refill 算)
- **配置段**:`policy:` 一段四子段(rbac 规则/audit 路径/origin 列表/rate_limit 参数),子段缺省=各能力关闭、段缺省=全关闭(逐字节一致,Story 11);校验进 config 启动流水线(通配语法、rps>0、路径可写由 audit 包构造期校验),中文点名错误(spec 0001 错误话语权惯例)
- **架构 U3 裁决记录**:Origin/限流进 transport 内部(纯 HTTP 机制不服务其他层,避免几十行一包的过度切分);policy/audit 依架构 §24 预留接缝立包——四能力的知识各自单一出处,其余包零改动
- **新第三方依赖**:零(golang.org/x/time 不引,令牌桶自研即卖点)

## Testing Decisions

- 好测试的标准沿用 spec 0002-0004:只从接缝断言外部行为(决策结果、HTTP 状态码、JSON-RPC 错误、审计文件内容),不断言内部调用序列
- **接缝**:①policy 匹配纯函数表驱动穷举(通配边界:`*`/`ns_*`/精确/deny 优先/fail-close/未知角色);②令牌桶注入假时钟表驱动(连续消费/恢复速率/burst 上限);③audit 写 `t.TempDir` 断言 JSON-lines 字段与顺序;④config 表驱动(规则语法正反例、参数边界、子段缺省=关闭);⑤e2e 沿既有 transport httptest + go-sdk client(带/不带角色头)断言 allow 往返、deny -40001、429、403、审计行落盘;⑥关闭态回归 = 既有全部测试零改动通过
- 既有先例:表驱动 + httptest + SpanRecorder(spec 0004)全部沿用;时间注入是本 spec 唯一新接缝
- 全部测试 `-race`;新包覆盖率 ≥80%

## Out of Scope

- 认证与身份验证(OAuth 资源服务器、JWT、API key——角色为自报声明,如实记录)
- RBAC1+(角色继承/约束)、CEL/cerbos 策略引擎
- tools/list 的未授权工具过滤(列表全量返回,调用才拦——已知边界)
- per-IP / per-role / per-upstream 限流维度(全局单桶)
- 审计轮转/压缩/远端 sink(本地单文件 append)
- Origin 校验的浏览器外语义(SSE 重连等已由 SDK 层处理)
- 缓存与策略的交互(W3 cache 到时再议 deny 结果是否可缓存——默认不可缓存)

## Further Notes

- 时间盒:5h(计划 §3),从 policy 匹配纯函数动工起算;**超时降级顺序:砍令牌桶限流 → 保 RBAC+审计 → Origin 必保**(MCP 规范要求项);降级发生时本 spec 修订表追记
- 依据链:计划 §2 中间件链图(审计原位 HTTP 层,本 spec 修订为 mcp 层,理由:工具级字段)→ 修订表;架构 §62-64 数据流标注、§188 D1、§205 U3(本 spec 关闭该项)、§210 W2 diff 纪律;research/02 §185(Origin 403 规范要求)、research/06 §6(RBAC0 模型与不引库论证)
- 完成判据(CI 可机械验证):`go build ./...`;`go test ./... -race` 全绿且既有测试零改动;diff 检查:gateway/upstream/telemetry 三包零改动,policy/audit 新包 + transport 内部 + config 段 + cmd 装配行;人工判据:带角色头调用被拒/放行各一例,审计文件可见两条对应记录
- 设计追问要点(工程语境):"为什么不用 cerbos/CEL"(独立部署成本/新技术栈 vs RBAC0 三种匹配一条 deny 优先规则)、"fail-close 为什么是默认"、"审计为什么在 mcp 层不在 HTTP 层"

## 修订

| 日期 | 变更 |
|---|---|
| 2026-11-05 | 发布(ready-for-agent)。发布前经 AskUserQuestion 确认:①RBAC 角色来自 HTTP 头声明(默认角色兜底);②U3 裁决=Origin/限流进 transport 内部,policy/audit 立包。审计落点由计划 §2 的 HTTP 中间件链修订为 mcp 层 Receiving(工具级字段需要),修订表留痕 |
| 2026-11-05 | 实现完成定稿(三票合入、评审修复收口)。三处实现裁决追记:①审计中间件装在 RBAC 判定**外层**——上文"mcp 层 RBAC 判定 → 审计落盘"按安装顺序字面(外层在前)会把 audit 推到判定内层,而 deny 不下探 next、内层看不到拒绝;实现取唯一自洽读法(审计先下探、待决策产生再落盘,allow/deny 都恰好记一条),e2e 以 deny 落盘记录反钉;②HTTP 层 403/429 不产生 span、不落审计——Origin 校验与限流是准入机制,装在可观测与审计之外(链最外层),攻击与过载流量不入 trace;③Story 19(不引 cerbos/CEL 的工程语境记录)落点=policy 包注释与本行,W4 README 里程碑再外化 |
| 2026-11-05 | 架构评审深化(C1/C2,improve-codebase-architecture 选定执行):①origin 条目语法收进新叶包 originrule(Parse→Pattern+Matches)——原 config/transport 双份实现与"跨包一致性靠 e2e 反例"退役,一份语法两个消费方;config 的 checkOriginEntry 退化为位置前缀包装,transport 编译期消费 Pattern;唯一行为变化=无端口 IPv6 条目的方括号规范化(原实现该形态永不匹配,无测试覆盖的潜在不一致,已修复并有专例);config/transport 的 origin 语法测试表迁入 originrule,消费方只留管线测试。②两条中间件链顺序升为逐对定点单测(receivingChain 探针法钉 trace→桥→审计→RBAC 与 deny 路径可见性;HTTP 准入对钉 Origin→限流不消耗令牌、429 不产生 span)——W3 插缓存只跑定点,不再全量 e2e 兜底。机器判据:go build + go test ./... -race 全绿,既有测试零改动(gofmt 归一除外) |
