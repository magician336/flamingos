---
id: 0006
title: "缓存层:配置驱动的工具结果缓存引擎与 tools/list 协议 hint 产出"
triage: ready-for-agent
date: 2026-11-08
source: 开发计划 §2/§3/§4(缓存 4h,降级=缓存Scope 只留 public)+ 架构 §1(cache 预留接缝)/§2(internal/cache W3 建)/§3(W3 挂点:命名空间解析后、upstream 调用前)/§6(D2 typed 接口/D7 构造注入)+ research/06 §2(官方 87 行参考、0/6 竞品真缓存、键控+命中率、语义类比 HTTP Cache-Control)+ go-sdk v1.8.0 源码核查(mcp/cache.go 87 行参考实现;mcp/protocol.go 的 Cacheable 仅嵌入 discover 与四个 list/read 结果,CallToolResult 无此字段——计划"上游 hint 驱动调用缓存"前提不成立;mcp/server.go SetCacheable 钩子现成)+ spec 0005 修订表(W3 插缓存只跑定点)与 Out of Scope 遗留议项(deny 是否可缓存,本次裁决:结构上不可缓存)+ 会话内 AskUserQuestion 三项确认(①配置驱动调用缓存+网关自身 hint 产出;②private=缓存键含角色隔离;③本 spec 只含缓存,压测/演练/基础 e2e 归 0007)
tracker-note: 本地 tracker;W4 GitHub 公开时迁为 issue 并加同名 label
---

# Spec 0006 — 缓存层:配置驱动的工具结果缓存引擎与 tools/list 协议 hint 产出

## Problem Statement

网关已能聚合转发、全链路追踪并做策略与审计(spec 0002-0005),但每次 tools/call 都原样打到上游:相同参数的高频只读调用(查列表、读状态)重复支付上游往返延迟,突发重复流量直接传导给上游。缓存是"中间件平台工程"叙事的缺角(计划 §0.1:显式暴露缓存/限流/鉴权/OTel),六家竞品零家实现真缓存引擎(research/05,higress PR #4281 自认"wire hints only; no cache engine"),这是差异化卖点;同时计划假设的"上游 ttlMs/cacheScope hint 驱动调用缓存"经 SDK v1.8.0 源码核查不成立——协议 hint 只存在于 discover 与 list/read 结果,tools/call 线路上没有缓存语义,网关侧真缓存的触发语义需要重新裁决并在 spec 留痕。

## Solution

新增 `cache` 包实现工具结果缓存引擎,以 upstream.Client 装饰器接入(gateway/upstream/telemetry 零改动,cmd 装配层包装);配置声明缓存规则(聚合工具名通配 → ttl_ms + scope),public 结果全局共享,private 结果键含角色隔离;仅缓存成功的业务结果,拒绝与失败结构性不进缓存。同时网关自身 tools/list 应答经 SDK SetCacheable 产出协议 hint(可配 ttl_ms),以协议产出方身份落实"协议级缓存语义"。通配语法从 policy 迁出为叶包单一出处(policy/cache 两个消费方,originrule 先例)。cache 段缺省 = 关闭、行为与现状逐字节一致。

## User Stories

1. As Agent 用户, I want 相同参数的重复调用直接得到缓存结果, so that 高频只读调用不必每次等待上游往返
2. As Agent 用户, I want TTL 过期后的调用重新到达上游, so that 缓存不会返回过期数据
3. As Agent 用户, I want 不同参数的调用各自独立缓存, so that 参数不同不会错误命中
4. As Agent 用户, I want private 工具的缓存键含角色(同角色命中、跨角色隔离), so that 结果可能因调用者而异的工具不会跨角色复用
5. As Agent 用户, I want 缓存命中在请求 span 上带命中属性, so that Jaeger 排查时能分辨"快是因为缓存"
6. As Agent 侧客户端, I want 网关 tools/list 应答携带 ttlMs hint(可配), so that 我的 prompt cache 可依赖清单在该 TTL 内稳定
7. As Agent 侧客户端, I want list hint 缺省为 0(立即过期), so that 不宣告稳定性时客户端行为与协议缺省一致
8. As 网关运维, I want 用配置声明缓存规则(聚合工具名通配 → ttl_ms + scope), so that 不改代码就能按工具特性启用缓存
9. As 网关运维, I want cache 段缺省(含显式 null)=关闭、行为与现状逐字节一致, so that 不需要缓存的部署零开销零风险
10. As 网关运维, I want 规则语法非法/ttl_ms≤0/scope 取值非法启动即败(中文点名位置), so that 配置错误活不到被流量发现
11. As 网关运维, I want 多条规则按声明顺序首条命中生效, so that 优先级语义确定可预测
12. As 网关运维, I want 缓存容量有上限,超限先清过期再按最早插入逐出, so that 参数基数爆炸不会打爆内存
13. As 合规/排查人员, I want RBAC 拒绝与传输失败与业务失败(IsError)的调用不进缓存, so that 缓存里只有可复用的成功结果
14. As 合规/排查人员, I want 缓存命中的调用照样落审计行(判定 allow、无错误), so that 审计口径与结果来源无关,每次调用都有记录
15. As 合规/排查人员, I want 命中/未命中原子计数器可快照, so that 压测报告与故障演练能拿到命中率数字
16. As 开发者, I want 缓存以 upstream.Client 装饰器实现, so that gateway/upstream/telemetry 零改动,插拔只发生在 cmd 装配层
17. As 开发者, I want 工具名通配语法单一出处(自 policy 迁出的叶包), so that policy 与 cache 两个消费方共享一份语法,不重演 origin 双份实现(C1 先例)
18. As 开发者, I want 缓存键为聚合工具名 + args 的 SHA-256 截 128 位哈希, so that 不同参数几乎不可能错误命中(错误命中=把 A 的结果给 B,正确性优先)
19. As 开发者, I want 命中回调与角色提取经构造注入(cmd 闭包翻译), so that cache 包不 import telemetry/policy,cmd 是唯一相遇点(audit ctx 键先例)
20. As 开发者, I want 时钟经构造注入, so that TTL 与逐出测试确定性(不 sleep,限流假时钟先例)
21. As 开发者, I want 规则命中判定与写入判定出自同一编译产物, so that 同输入必同判定(决策桥同口径先例)
22. As 测试维护者, I want 引擎测试以计数假上游 + 假时钟表驱动, so that 只断言外部行为(上游调用次数)而非内部结构
23. As 测试维护者, I want e2e 钉死同参两次调用上游恰一次 + 两审计行 + 第二请求 span 命中属性, so that 装配正确性有黑盒判据
24. As 测试维护者, I want 关闭态回归由既有全部测试零改动自证, so that 装配纪律有机械判据
25. As CI, I want `go build ./...` 与 `go test ./... -race` 全绿为机械判据, so that 验收不依赖主观
26. As 维护者, I want 降级形态显式记录(超时砍 private 键控,private 命中即不缓存), so that 时间盒裁决有据可查
27. As 维护者, I want "为什么配置驱动而非协议 hint"以工程语境记录(v1.8.0 源码事实), so that 取舍可答辩

## Implementation Decisions

- **模块与边界**(架构 §1 cache 预留接缝、§2 仓库布局、§6 D2/D7):
  - `cache` 新包:存储引擎(mutex+map,TTL 惰性过期,容量上限+FIFO 逐出)+ `Wrap(upstream.Client, …) upstream.Client` 装饰器(项目真接缝,架构 §1);import upstream,不 import gateway/transport/policy/audit/telemetry
  - `toolpattern` 新叶包:`Parse→Pattern`+`Matches` 最小接口,自 policy 规则匹配迁出;policy 重构为消费方(等价重构,行为零变化,originrule C1 先例——origin 语法双份实现刚刚付过学费,通配语法不再犯第二次)
  - `transport` 小改:构造 ServerOptions 增加 SetCacheable hint 产出(list_ttl_ms 可配),中间件两条链零改动(C2 逐对定点单测不动)
  - `config` 增顶级 `cache:` 段(与 policy 平级,架构 §4.3 预告);cmd 装配层完成规则编译、角色提取闭包、命中回调闭包、时钟注入(cmd 是 cache 与 policy/telemetry 知识的唯一相遇点)
- **触发语义裁决(本 spec 核心决策)**:配置驱动——v1.8.0 的 CallToolResult 不含 Cacheable 字段(SEP-2549 仅覆盖 discover 与 list/read 结果),且运行期 tools/list 是启动快照(spec 0002,无上游 list 流量),协议 hint 驱动调用缓存的计划假设不成立;协议级语义以"产出方"身份落地:网关自身 tools/list 应答经 SetCacheable 统一携带 `list_ttl_ms`(缺省 0=立即过期即不宣告,cacheScope=public——清单对所有角色一致,spec 0005 既有裁决)
- **缓存键**:public = 聚合工具名 + `\0` + args 字节 SHA-256 截 128 位;private = 角色再拼在键前(角色提取经构造注入,未带头按配置默认角色,与 RBAC 判定同源);args 字节级精确匹配,不做 JSON 键序/空白规范化(如实记录的边界,规范化是未来加分项)
- **缓存范围(只存三种都不沾的结果)**:上游返回 err==nil 且 IsError==false 才写入;RBAC deny 结构性不可缓存——装饰器在 mcp 层判定与审计的内层(链最内),拒绝不下探 next、根本到不了装饰器,关闭 spec 0005 Out of Scope 遗留议项;传输失败(err≠nil)与业务失败(IsError=true)一律不存
- **规则语义**:`rules` 表按声明顺序首条命中生效(优先级=声明序);命中判定(classify:聚合工具名 → ttl/scope)编译期定档,查缓存与写缓存用同一份编译产物;ttl_ms 必须 >0(scope ∈ {public, private});同参数并发首调(缓存击穿)不做 singleflight 合并——并发下可能重复打到上游但结果正确,时间盒内不做,Limitations 记录
- **存储与逐出**:mutex+map,TTL 惰性过期(读时判定并清除);容量上限 `max_entries`(缺省 1024),put 超限时先清一轮已过期,仍超限按最早插入逐出(FIFO——LRU 留作 Limitations,当前规模够);时钟经构造注入(`Now func() time.Time`,nil 落 time.Now,限流先例)
- **可观测**:telemetry 增一个导出入口("向当前请求 span 写缓存命中属性");cmd 以闭包注入 cache 的命中回调——cache 不知 telemetry;命中率 = 包内原子计数器(hits/misses)+ 快照方法,OTel metrics 管线(MeterProvider)不做(Out of Scope)
- **配置段**(`cache:` 顶级段):`rules`(列表:tools 通配/ttl_ms/scope)、`list_ttl_ms`(缺省 0)、`max_entries`(缺省 1024);段缺省(含显式 null)= 缓存关闭 + hint 不宣告,零行为;校验进 config 启动流水线(规则语法经 toolpattern、数值边界),与既有校验问题一次性聚合,中文点名(spec 0001 惯例)
- **新第三方依赖**:零(SHA-256 标准库;vegeta 归 0007)

## Testing Decisions

- 好测试的标准沿用 spec 0002-0005:只从接缝断言外部行为(上游被调次数、审计文件内容、span 属性、HTTP/JSON-RPC 可观察面),不断言内部 map 结构与调用序列
- **接缝**:①引擎+装饰器表驱动:计数假 upstream.Client + 假时钟(首调 miss 上游 1 次/同参二调 hit 上游仍 1 次/异参 miss/假时钟拨过 TTL 后 miss/private 同角色 hit 跨角色 miss/IsError 与 err≠nil 不存/容量超限逐出顺序/计数快照);②toolpattern 语法表随迁自 policy(`*`/`ns_*`/精确/边界);③classify 纯函数表驱动(首条命中/无命中不缓存);④config 表驱动(段缺省=关闭、rules 空、ttl_ms≤0、scope 非法、max_entries≤0、多问题聚合中文点名);⑤e2e 沿既有 transport httptest + go-sdk client:同参两次调用断言上游恰一次+两审计行+第二请求 span 命中属性,deny 调用断言上游零调用;⑥关闭态回归 = 既有全部测试零改动通过
- 既有先例:表驱动 + httptest + SpanRecorder(spec 0004)+ 假时钟(spec 0005 限流)全部沿用;命中回调闭包是唯一新注入点,无新测试基建
- 全部测试 `-race`;cache 与 toolpattern 覆盖率 ≥80%

## Out of Scope

- args 的 JSON 规范化(键序/空白归一后再哈希)——字节级精确匹配
- singleflight 并发击穿合并、LRU/LFU 淘汰、分片、过期后台清扫(惰性判定)
- OTel metrics 管线(MeterProvider)——命中率以计数快照 + span 属性呈现
- 跨进程/分布式缓存、持久化(单机内存)
- 上游 list/read 结果缓存与上游 hint 消费(运行期启动快照无流量;待 SDK 未来版本 CallToolResult 携带 hint 再议)
- 缓存主动失效 API(tools/list_changed 本就不转发,spec 0002)
- 压测、故障演练、基础 e2e(0007,缓存落地后出)

## Further Notes

- 时间盒:4h(计划 §3),从存储引擎动工起算;**超时降级顺序:砍 private 键控(private 规则命中即不缓存,行为等同未配置该规则)→ 保 public 共享缓存与 hint 产出**;降级发生时本 spec 修订表追记
- 依据链:计划 §2 链图"聚合(命名空间+缓存)"、§3 缓存行(时间盒 4h/降级只留 public)、§4 W3;架构 §1 预留接缝与 §1 装饰器依据(D2 typed 接口使缓存键可用)、§2 internal/cache、§3 W3 挂点、§6 D2/D7;research/05(竞品 0/6 真缓存)、research/06 §2(87 行参考/键控+命中率/性价比第一);SDK v1.8.0:mcp/cache.go(参考形状)、mcp/protocol.go Cacheable 嵌入范围(本 spec 触发语义裁决的源码证据)、mcp/server.go SetCacheable;spec 0005 修订表"W3 插缓存只跑定点"(本 spec 不在两条中间件链上插路,链顺序定点单测零改动)与 Out of Scope 遗留议项(deny 不可缓存,本 spec 以链位置结构性关闭)
- 完成判据(CI 可机械验证):`go build ./...`;`go test ./... -race` 全绿且既有测试零改动;diff 检查:gateway/upstream/telemetry/audit 零改动,cache/toolpattern 新包 + policy 等价重构(仅匹配语法迁出)+ config 段 + cmd 装配行 + transport hint 构造参数;人工判据:同一只读工具同参调用两次,上游侧计数恰一次,审计文件两行,Jaeger 第二请求 span 可见缓存命中属性
- 设计追问要点(工程语境):"为什么配置驱动而不是协议 hint"(v1.8.0 源码事实 + 启动快照)、"为什么 SHA-256 截 128 位"(错误命中=串结果,正确性优先于哈希开销)、"为什么 FIFO 不是 LRU"(规模与 Limitations)、"deny 为什么结构上不可缓存"(装饰器在判定内层)、"private 键控防什么"(跨角色复用)

## 修订

| 日期 | 变更 |
|---|---|
| 2026-11-08 | 发布(ready-for-agent)。发布前经 AskUserQuestion 确认:①缓存触发语义=配置驱动调用缓存 + 网关自身 tools/list hint 产出(SDK v1.8.0 源码核查推翻计划"上游 hint 驱动调用缓存"前提:Cacheable 仅嵌入 discover 与 list/read 结果,CallToolResult 无此字段;运行期 tools/list 为启动快照);②cacheScope=private 取"缓存键含角色隔离"(同角色命中、跨角色隔离),计划 §3 降级形态"只留 public"有实义;③W3 拆两张 spec,本 spec 只含缓存(0007=压测+故障演练+基础 e2e,缓存落地后出) |
| 2026-11-08 | 实现完成定稿(三票合入、双轴评审两 P0 无、评审修复收口)。六处实现裁决追记:①policy 对非法通配模式由原静默折叠改为编译期 panic——票 01"等价重构行为零变化"的字面越出;该路径经 config 启动校验不可达(D8),deny 语义下静默跳过=fail-open 不可接受,采纳并留痕;②private 键=角色+NUL+聚合名+NUL+args 摘要(public 键恰含一个 NUL、private 恰含两个,两域永不重合),RoleOf 仅 private 路径咨询——public 键不含角色不咨询,避免"配 cache 未配 policy 时 public 缓存静默失效";取不到角色查/写都跳过(宁 miss 不可错 hit);③命中 span 属性 flamingos.cache.hit(hit/miss 两态都写:属性存在性=该调用经过缓存层,只写 true 会让 miss 与没查缓存在 Jaeger 无法分辨);④同键覆写只更新内容与 TTL、保留原 FIFO 位次(FIFO 与 LRU 的分界);⑤max_entries 经 yaml.Node 保留原始字面量区分"显式 0"(启动即败)与"字段缺省"(兜底 1024),rules 死规则(缺 tools)启动即败(空 allowed 同款纪律);⑥config.checkPattern 切换为 toolpattern.Parse 消费方(票 01 移交项,通配语法单一出处收口,上报文案零变化)。另:降级未发生(private 键控完整落地);架构文档 v1.4 回填缓存挂点现状。机械判据:12 包 `go test ./... -race` 全绿,既有测试零改动,cache/toolpattern 覆盖率 100%,gofmt 干净,gateway/upstream/audit 零改动 |
