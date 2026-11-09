---
id: 0003
title: "stdio 上游适配器(简化版)"
triage: ready-for-agent
date: 2026-11-04
source: 架构设计 §4.1(upstream 接口)/§6 D4 + spike 报告 §2(协议 Pin 旋钮)/§5(未验证边界:stdio 子进程路径)+ spec 0002(接缝、错误契约与测试先例)
tracker-note: 本地 tracker;W4 GitHub 公开时迁为 issue 并加同名 label
---

# Spec 0003 — stdio 上游适配器(简化版)

## Problem Statement

配置层自始接受 stdio 型上游声明(type=stdio 的 command/args 条目,校验已要求非空 command),但装配入口对 stdio 条目一律拒绝并提示"暂未支持"——本地 stdio MCP server(如经 npx 启动的文件系统工具)无法接入网关聚合。HTTP 通道已完整,缺的是 upstream.Client 接口的第二种实现:stdio adapter。本任务为范围收窄后的简化版:单进程直连,不做进程监督、重连与池化;实现受 6h 时间盒约束,超时即砍并保留现状("启动即败 + 明确提示")作为降级。

## Solution

新增 stdio adapter 实现既有 upstream.Client 接口:构造时拉起子进程并完成 initialize 握手(协议版本固定 2025-11-25),ListTools/CallTool 以原始 tool 名与原始参数字节转发、每次调用受该上游 timeout 约束,Close 沿 SDK 内建的"关闭 stdin → 等待(默认 5s)→ SIGTERM → SIGKILL"链路体面终止子进程。装配入口的翻译层把 stdio 条目从"启动即败"改为构造 stdio 客户端;dial 按类型选择 adapter;gateway/transport 零改动——第二种传输经既有接缝接入,聚合、命名空间路由与错误契约对两种传输一视同仁。

## User Stories

1. As Agent 用户, I want 聚合清单包含 stdio 上游的 tool(带命名空间前缀), so that 本地工具与远端工具在同一清单里无差别使用
2. As Agent 用户, I want 调用 stdio 上游的 tool 时网关以原始名与原始参数字节转发, so that 参数内容不被改写
3. As Agent 用户, I want stdio 上游的业务失败应答(IsError)原样透传, so that 错误语义不被网关改写
4. As Agent 用户, I want stdio 上游死亡时收到点名该上游的"不可用"错误, so that 故障可诊断且不冒充 tool 业务错误
5. As 网关运维, I want stdio 上游在构造时完成子进程拉起与握手, so that 连接问题在启动瞬间暴露而非首个请求时
6. As 网关运维, I want 命令不存在或不可执行时启动即败,报错点名上游与命令, so that 配置错误一眼定位
7. As 网关运维, I want 每次 stdio 调用受该上游 timeout 约束, so that 挂死子进程不拖死网关请求
8. As 网关运维, I want 网关关闭时 stdio 子进程被体面终止(关 stdin → 等待 → SIGTERM → SIGKILL), so that 不留孤儿进程
9. As 网关运维, I want stdio 与 http 上游在同一配置混用, so that 聚合清单覆盖两类工具源
10. As 网关运维, I want 运行期单个 stdio 子进程死亡只影响指向它的调用, so that 故障不放大到其他上游
11. As 网关运维, I want stdio 客户端在 initialize 握手中固定协议版本 2025-11-25, so that 规避新版客户端对沉默老 stdio server 先发 server/discover 导致的握手卡死(go-sdk #1332)
12. As 开发者, I want stdio adapter 与 HTTP adapter 实现同一 Client 接口、gateway/transport 零改动, so that 传输种类增加不波及聚合与路由(架构 D4)
13. As 开发者, I want SDK 机器类型(CommandTransport/ClientSession)不跨出 adapter 文件, so that 换 SDK 版本只动一个文件
14. As 开发者, I want 协议版本 Pin 的字符串只出现一处, so that 版本策略调整不改多处
15. As 开发者, I want config 模块零改动(既有字段足够), so that 本任务不扩大配置面
16. As 测试维护者, I want 测试用真实子进程夹具(go-sdk server + StdioTransport 的最小二进制), so that 测的是真实 stdio 路径而非模拟管道
17. As 测试维护者, I want 夹具二进制一次构建、全部测试复用, so that 测试套件不因逐例 go build 而变慢
18. As CI, I want `go build ./...` 与 `go test ./... -race` 全绿作为机械判据, so that 验收不依赖主观判断
19. As 网关运维, I want 已知上游缺陷(坏帧中断会话 go-sdk #1209、Close 竞态 #855)以已知边界形式记录而非修复, so that 异常行为可归因

## Implementation Decisions

- **adapter 与 HTTP adapter 同构**:构造即握手(握手同样受该上游 timeout 约束)、锁内取会话/锁外调用、每次调用以 timeout 设上下文期限、Close 幂等、关闭后 ErrClosed——与 upstream.Client 接口注释的契约一字不差
- **传输构造**:以 exec.Command(name, args...) 构造子进程,交给 SDK 的 CommandTransport(模块缓存源码核对:v1.8.0 mcp/cmd.go,Connect 拉起进程并接管 stdin/stdout;TerminateDuration 默认 5s);adapter 不自建进程管理,Close 语义完全沿用 SDK 的分级终止链
- **协议版本 Pin**:握手经 ClientSessionOptions{ProtocolVersion: "2025-11-25"}(spike §2 验证过的公开旋钮);Pin 字符串单一出处
- **错误契约复用**:一切失败包为携带上游名与原因的 TransportError;上游业务失败(IsError)原样透传不吞不改;ErrClosed 哨兵与 HTTP adapter 共用同一份
- **参数透传纪律沿用**:转发参数一律 json.RawMessage(spike §3:裸 []byte 被编成 base64)
- **装配翻译**:cmd 翻译层把 stdio 条目投影为构造参数(名称、command、args、timeout);dial 按类型选择 NewHTTP/NewStdio;既有"暂未支持"拒绝路径删除,相关契约测试同步改写
- **环境继承**:子进程继承网关进程的环境与工作目录,不新增 env/cwd 配置字段(简化边界)
- **无新增第三方依赖**
- **时间盒纪律**:实现超时即砍;降级方案=保留现状的 stdio 拒绝路径

## Testing Decisions

- 好测试的标准沿用 spec 0002:只从接缝断言外部行为(可观察的结果与错误语义),不断言内部调用序列
- **接缝**:不新增接缝——stdio adapter 就是既有 upstream.Client 接口的第二个实现;装配层沿 cmd 既有纯函数与进程行为接缝
- **子进程夹具**:仓库内最小 stdio server(go-sdk NewServer + AddTool 若干 + StdioTransport),测试入口一次性构建为临时二进制,adapter 测试经真实子进程往返——被测对象是 adapter 与装配,夹具只搭台
- **adapter 单元测试**覆盖:握手成功且协商版本=2025-11-25、ListTools 原始清单、CallTool 以原始名与原始字节往返(断言字节级一致)、慢工具被该上游 timeout 打断、命令不存在的构造报错、Close 幂等与关闭后 ErrClosed、Close 后子进程确实退出(无孤儿)
- **装配与端到端**:混合配置(http 进程内假上游 + stdio 夹具上游)经网关 initialize → tools/list(两命名空间聚合)→ tools/call 双通道;杀掉 stdio 子进程后指向它的调用点名"不可用"、另一上游照常(故障隔离)
- **既有先例**:表驱动 + t.TempDir/t.Cleanup;假上游搭建模式见 spec 0002 各包测试

## Out of Scope

- 进程监督与自动重启(死亡的 stdio 上游在网关重启前保持"不可用")
- 重连、健康检查、上游连接池化(每 stdio 上游单进程)
- env/工作目录/启动用户等配置字段
- go-sdk 上游缺陷的规避补丁(#1209 坏帧中断会话、#855 Close 竞态)——已知边界,记录不修
- stdio 子进程的日志采集与转发
- 工具清单的运行期动态同步(沿用快照策略)

## Further Notes

- 依据链:架构 §4.1(upstream 接口)/§6 D4;spike 报告 §2(ClientSessionOptions.ProtocolVersion 实测)/§5(stdio 子进程路径未验证,本任务首次落地);spec 0002 的接缝、错误契约与测试先例
- API 形状已对模块缓存源码核对(v1.8.0):mcp/cmd.go 的 CommandTransport{Command *exec.Cmd; TerminateDuration time.Duration}(Connect 起进程接管 stdin/stdout,Close 分级终止);mcp/transport.go 的 StdioTransport{}(服务端字面量构造,读写 os.Stdin/os.Stdout)
- 完成判据(CI 可机械验证):`go build ./...` 成功;`go test ./... -race` 全绿;混合上游端到端测试演示 stdio tool 经网关聚合转发与故障隔离
- 环境注意:Fedora 44 双 Go 并存,shell 跑 go 用 /usr/local/go/bin/go 全路径;无新第三方依赖、无网络下载

## 修订

| 日期 | 变更 |
|---|---|
| 2026-11-04 | 发布(ready-for-agent);实现完成(集成分支 stdio-upstream-adapter,两票 + 双轴 code-review + 修复)后定稿。偏离与追记:①Pin 协商结果的断言经同包白读 session 的 InitializeResult——Client 接口无该接缝,不为测试扩接口("只从接缝断言"在此处的记录边界);②子进程夹具在 upstream 与 cmd 两包各构建一次(Go 测试不跨包共享构建入口,惯例背书);③评审修复:提取 sessionBase 共享基座消除两 adapter 同形重复(net -44 行,契约与既有测试零改动)、e2e 补 stdio 业务失败(IsError)透传闭环、translate/dial 双级联保持"纯函数投影 vs 副作用构造"分工并注释说明、architecture D9/§1 回写"砍 stdio 演练"的现行代价;④已知边界 #1209/#855 以代码注释与本文件 Out of Scope 双处记录;⑤upstream.go 接口注释同步双实现现状。已知记录:杀掉子进程后的清理阶段,SDK Close 的 cmd.Wait 返回 signal: killed,closeClients 按设计记日志不阻断 |
