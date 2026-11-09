---
id: 0002
title: "HTTP 上游连接、聚合与转发"
triage: ready-for-agent
date: 2026-11-03
source: 架构设计 §3/§4.1/§4.2/§9 + spike 报告(docs/design/spike-go-sdk.md §2/§3)
tracker-note: 本地 tracker;W4 GitHub 公开时迁为 issue 并加同名 label
---

# Spec 0002 — HTTP 上游连接、聚合与转发

## Problem Statement

网关目前只会"读配置、打印计划":装配入口输出一份装配摘要就退出,没有任何真实服务在监听;配置里声明的 HTTP 上游也没有人去连接。Agent 客户端(Inspector/Eino)无处可连,每个 Agent 用户仍要自己逐个连接、逐个配置上游 MCP server——网关的核心价值(一个端点聚合 N 个上游)至今不存在。里程碑验收("两个假上游经网关被 Inspector 调用成功")无法达成。

## Solution

打通架构 §3 的完整数据流:启动时与全部 HTTP 上游完成 initialize 握手并拉取各自 tool 清单,聚合加命名空间前缀后注册到网关侧 MCP server;网关以 Streamable HTTP(Stateless)对外监听单一端点。Agent 一次 initialize 即可使用,一次 tools/list 看到全部上游 tool 的聚合清单(calc_add、fs_read 这样的前缀名),一次 tools/call 经网关按第一个下划线拆出命名空间、路由到正确上游、以原始 tool 名与原始参数字节转发,结果原样返回。传输细节封在 upstream 的 HTTP adapter,命名空间聚合与路由在 gateway,Streamable HTTP 细节在 transport,装配与翻译在 cmd——依赖方向 cmd → transport → gateway → upstream 不变。

## User Stories

1. As Agent 用户, I want 向网关端点发起 initialize 握手并成功协商, so that Inspector/Eino 可以把网关当作一个普通 MCP server 连接
2. As Agent 用户, I want 一次 tools/list 拿到全部上游 tool 的聚合清单, so that 不必逐个连接、逐个配置上游
3. As Agent 用户, I want 聚合清单里每个 tool 名带命名空间前缀(如 calc_add), so that 多上游同名 tool 不冲突且来源一眼可读
4. As Agent 用户, I want 调用 calc_add 时网关转发到 calc 上游的 add 并原样返回结果, so that 聚合转发对 Agent 零感知(协议终止式代理)
5. As Agent 用户, I want tool 的描述与输入 schema 原样保留, so that Agent 端提示词与参数校验不失真
6. As Agent 用户, I want 上游正常应答但业务失败(IsError)时结果原样透传, so that 错误语义不被网关改写或吞掉
7. As Agent 用户, I want 上游连不上、超时或中途断开时收到点名该上游的"不可用"错误, so that 故障可诊断、且不冒充 tool 本身的业务错误
8. As Agent 用户, I want 调用参数按原始字节透传, so that 参数内容不被网关改写(如被误编码成 base64)
9. As Agent 用户, I want 网关在 initialize 应答中自报身份, so that Agent 端能识别连接的是 flamingos
10. As 网关运维, I want 启动时与全部上游完成握手并拉取清单, so that 连接问题在启动瞬间暴露而非首个请求时
11. As 网关运维, I want 任一上游握手或列举失败时启动即败并指出是哪个上游, so that 网关不带病上线(设计 D8)
12. As 网关运维, I want 每次上游调用受该上游的 timeout 配置约束, so that 挂死上游不拖死网关请求
13. As 网关运维, I want 运行期单个上游故障只影响指向它的调用, so that 故障不放大到其他上游的列出与调用
14. As 网关运维, I want 网关对 Agent 侧无会话状态(Stateless), so that 网关重启不欠 Agent 状态、未来水平扩展不受限
15. As 网关运维, I want Ctrl+C/SIGTERM 时优雅关闭:停止接收新请求、断开全部上游, so that 不留孤儿连接
16. As 网关运维, I want 配置里出现 stdio 上游时启动被拒并明确提示"暂未支持", so that 不会在运行期撞进未实现分支
17. As 开发者, I want upstream.Client 是唯一新接缝、gateway 测试塞假实现即可运行, so that 聚合与路由的测试不需要网络与真实上游
18. As 开发者, I want SDK 机器类型(server/session/transport)不跨出 adapter(设计 D4), so that 未来换 SDK 版本或加传输类型只动一个文件
19. As 开发者, I want 命名空间解析按第一个下划线拆分且规则单一出处(设计 D5), so that 解析永无歧义、config 的 R1 字符集校验与路由规则互相咬合
20. As 开发者, I want 聚合 ListTools 并发扇出全部上游, so that 上游数量增多时启动延迟不线性叠加
21. As 开发者, I want 上游 tool 缺 schema 时兜底空 object schema 再注册, so that SDK 注册不 panic(spike 实测)
22. As 测试维护者, I want 假上游用 go-sdk server 在进程内搭建, so that 测试快速、稳定、无外部依赖,测的是网关而不是环境
23. As CI, I want `go build ./...` 与 `go test ./... -race` 全绿作为本任务完成的机械判据, so that 验收不依赖主观判断
24. As 维护者, I want 转发链路的每个决策都能对应 D2/D3/D4/D5/D8 与 spike 实测证据, so that 后续修改转发行为时有据可依、取舍可追溯

## Implementation Decisions

- **模块与边界**(架构 §1/§2,依赖单向 cmd → transport → gateway → upstream):
  - `upstream`:`Client` 接口(ListTools/CallTool/Close)+ HTTP adapter;接口形状照架构 §4.1——CallTool 收原始 tool 名、参数为 json.RawMessage;契约:构造时建立底层连接、实现并发安全、Close 后再调用返回 ErrClosed
  - `gateway`:`New(clients map[string]Client)` 依赖注入;`HandleToolsList` 并发扇出全部上游 → 合并 → 加 `命名空间_` 前缀;`HandleToolsCall` 按第一个下划线拆前缀 → 查表选 upstream → 以原始名调用;名字无下划线或前缀无匹配 → 明确报错
  - `transport`:构造 go-sdk server + Streamable HTTP handler(Stateless),对外暴露 http.Handler;启动时调用 `HandleToolsList` 取聚合清单,逐个 `AddTool` 注册,注册的转发 handler 统一走 `HandleToolsCall`——工具注册表是启动时的一次性快照
  - `cmd`:装配入口从"打印摘要"升级为真实启动:Load → 构造全部上游客户端 → 装配 transport → 监听 → 信号处理 → 优雅关闭(停 server、逐个 Close 上游);config.Upstream 到 upstream 构造参数的翻译只发生在 cmd(架构 §5)
- **stdio 条目的本任务行为**:配置校验(spec 0001)仍接受 stdio 声明,但装配阶段遇到 stdio 条目即启动失败并明确中文提示"stdio 上游暂未支持(下一任务落地)"——即败而非运行期撞未实现分支
- **错误契约(接口的一部分,调用方必须知道)**:①连接失败/超时/断开 → 包装为携带上游名与原因的 TransportError,对 Agent 表达为点名上游的"不可用"错误,不冒充 tool 业务错误;②上游正常应答但业务失败(IsError)→ 原样透传,不吞不改(架构 §4.1 错误契约两条)
- **HTTP adapter 细节**(spike §2 已验证的 API 形状):go-sdk client + Streamable HTTP 客户端传输,DisableStandaloneSSE=true;每个上游一条长期会话(Agent 侧 Stateless 与 upstream 侧长连接并存,架构 §3);每次调用以该上游配置的 timeout 设置上下文期限
- **参数透传纪律**:转发参数一律 json.RawMessage——spike 实测裸 []byte 会被 encoding/json 编成 base64,字节级不一致
- **注册兜底两条**(spike §3 行为级发现):上游 tool 缺 InputSchema → 兜底 `{"type":"object"}` 再注册(缺 schema 注册直接 panic);聚合后的完整名自校验(非空、≤128、字符集)——SDK 对非法名只打日志不拒绝,网关必须自守;非法 tool 跳过并输出启动警示,单个坏 tool 不拖死整个网关启动
- **Agent 端点**:POST /mcp;Stateless 语义(仅 POST,GET/DELETE → 405,2026-07-28 spec);initialize 应答以 flamingos 身份自报
- **聚合时机**:启动时一次性快照注册;运行期不重列上游、不转发 tools/list_changed(订阅多路复用聚合不在范围)
- **扇出失败策略**:`HandleToolsList` 任一上游失败 → 整体失败 → 启动即败(D8);重试、重连、健康检查均不做
- **无新增第三方依赖**:go-sdk v1.8.0 与 yaml.v3 已在 go.mod;扇出用标准库或已在依赖树的 golang.org/x/sync

## Testing Decisions

- **好测试的标准**:只测外部行为——从接缝(假 Client、http.Handler、JSON-RPC 往返)断言可观察的结果与错误语义,不断言内部调用序列或实现细节
- **接缝**(与 spec 0001 的"唯一接缝 config.Load"同级表述):本任务唯一新接缝 = `upstream.Client`(架构 §1"真接缝":两个 adapter 天然存在;§5 原文背书"gateway 测试构造假 map[string]upstream.Client 即可运行,全仓库无一测试需要配置文件");transport 对外是标准库 http.Handler 类型,黑盒可测,不新增自制接缝
- **gateway 单元测试**:表驱动 + 假 Client 实现,覆盖:多上游清单合并、前缀拼接、按第一个下划线拆分(原名含下划线也不歧义)、以原始名转发、无下划线/前缀无匹配的报错、TransportError 到"上游不可用"的映射、IsError 结果透传、Close 后 ErrClosed、并发扇出正确合并
- **全链路集成测试(进程内黑盒)**:两个假上游 = go-sdk server(分别注册 add/echo 类 tool)+ Streamable HTTP handler + httptest;网关侧装配真实 HTTP adapter + transport;用 go-sdk 客户端对网关走 initialize → tools/list → tools/call。断言:聚合清单含双上游带前缀 tool;调用往返结果与直连上游一致;关闭一个假上游后,指向它的调用得到点名"不可用"、另一上游仍正常(故障隔离)
- **upstream adapter 单元测试**:httptest 假上游 + 真实 adapter,覆盖 ListTools/CallTool 往返、超时生效、Close 语义
- **既有先例**:spec 0001 建立的表驱动 + 临时夹具模式沿用;假上游用 SDK 搭建属测试代码,被测对象是网关自身,不算引入 SDK 依赖面
- 全部测试 `-race` 运行;覆盖率 ≥80% 总目标由本任务与后续任务共同逼近

## Out of Scope

- stdio adapter(下一任务,时间盒 6h,超时即砍;本任务内 stdio 条目=启动即败并提示)
- 运行期工具动态同步(重列上游、转发 tools/list_changed)
- 重试、重连、健康检查、上游连接池化(每上游单会话)
- Origin 校验、审计、令牌桶限流(W2 transport 中间件链)
- OTel 传播与 SEP-414 `_meta`(W2)
- ttlMs/cacheScope 缓存(W3)、eval 录制(W4)
- standalone SSE GET 流与服务端主动通知(不做订阅聚合的既定裁剪)
- Inspector 手工验证:作为手动验证补充,不进 CI 机械判据(CI 用进程内等价物覆盖)

## Further Notes

- 依据链:架构 §3(数据流)/§4.1(upstream 接口)/§4.2(gateway 与命名空间 R1-R3)/§9(里程碑映射)/§6 D2-D5、D8、D9;spike §2(签名级 API 形状)/§3(行为级发现 1-4)
- 里程碑验收"两个假上游经网关被 Inspector 调用成功":本任务的进程内双假上游集成测试是其机械等价物;Inspector 连真实进程作为手动验证补充
- spike 未验证边界的落点:多上游并发聚合、命名空间合并属本任务首次落地(spike 仅单上游单 tool),集成测试必须覆盖;`DisableStandaloneSSE: true` 下服务端主动通知不可达对本项目无影响(已裁剪)
- 环境注意:Fedora 44 双 Go 冲突,shell 跑 go 用 /usr/local/go/bin/go 全路径;无新第三方依赖、无大文件下载
- 完成判据(CI 可机械验证):`go build ./...` 成功;`go test ./... -race` 全绿;集成测试演示双假上游经网关 initialize/list/call 完整往返与单上游故障隔离

## 修订

| 日期 | 变更 |
|---|---|
| 2026-11-03 | 实现完成(集成分支 http-upstream-aggregation-forwarding,三票 + 双轴 code-review + 清理)后定稿,偏离记录:①接口形状按 SDK 线路真实形状修订——ListTools 返回 `[]*mcp.Tool`(架构草图 `[]mcp.Tool`,D4 零转换原则);②构造握手同样受该上游 timeout 约束(挂死上游在启动瞬间报错,Story 10/11);③非 object schema 与缺 schema 同等处理——兜底或跳过并警示(实测 AddTool 对 type≠object 亦 panic,"单个坏 tool 不拖死启动"纪律延伸);④net.Listen 与 /mcp 端点挂载收在 cmd,transport 只暴露 http.Handler(架构 §5 装配示意据此修订);⑤退出码新增 2=启动失败(0/1 字面值不动,契约测试同步);⑥tool 名自校验字符集含点号(核 SDK v1.8.0 源码 `mcp/tool.go`,spike 记录为不完整子集);⑦gateway 额外拦截"原始名为空"(ns_ 字面合法但拆分后不可路由的规则组合缺口)。已知记录:同一上游重复 listing 同名 tool 会被 AddTool 静默覆盖(当前无危害,备查);上游重启后长会话不复连属 Out of Scope(重连不做) |
