---
id: 0007
title: "运行质量实证:vegeta 压测、故障演练与基础 e2e"
triage: ready-for-agent
date: 2026-11-08
source: 开发计划 §3(压测 6h:vegeta 恒定多档+1 次阶梯,核心主张"相对开销",恒定速率避免 Coordinated Omission;故障演练 4h:上游 kill -9/断流重发/panic recovery;基础 e2e 4h:支持的版本组合+故障演练,5×N 一致性矩阵已砍)+ §4 W3 周验收(压测数字出炉/恢复时间数据入 docs)+ §5 测试策略(报告必含环境交代与 Limitations)+ §9(README 引用压测表)+ 架构 §2(e2e/ 与 benchmark/ 顶层目录 W3 建,黑盒测试在 internal 外)§8-3 + spec 0006 Out of Scope(压测/故障演练/基础 e2e 归本 spec)+ spec 0003(协议 Pin 2025-11-25、stdio 可执行夹具归属先例)+ go-sdk v1.8.0 源码核查(serveStateless 每请求建临时会话,裸 POST tools/call 无需先 initialize;supportedProtocolVersions 五版;生产代码零 recover——panic 行为待演练实证)+ vegeta v12 模块核查(纯 Go 库形态可用,无需外部二进制)
tracker-note: 本地 tracker;W4 GitHub 公开时迁为 issue 并加同名 label
---

# Spec 0007 — 运行质量实证:vegeta 压测、故障演练与基础 e2e

## Problem Statement

网关的功能面(聚合转发、全链路追踪、策略审计限流、结果缓存,spec 0002-0006)已经齐了,但"运行质量"三问没有任何证据:①引入网关本身值多少延迟(相对开销数字不存在,计划成功标准"压测数字出炉"未达成);②上游进程骤死、连接断流、异常应答注入时网关的行为与恢复时间没有任何实测记录,现有测试只覆盖 stdio 子进程死亡一例;③既有最高测试接缝是 cmd 装配级(httptest 同进程),真实交付形态(二进制 + 配置文件 + 监听端口 + 信号处理)没有黑盒判据,"支持的协议版本组合"的声明没有测试背书。三块都压在本 spec:它们是 README 性能表与 Limitations 章节的输入,计划 §9 发布交付物的前置。

## Solution

三块实证交付,生产代码默认零改动:

1. **vegeta 压测**:vegeta 以 Go 库形态引入(不依赖外部二进制安装),场景代码落顶层 benchmark/ 目录(架构 §2 预告行落地)。拓扑为三进程回环:压测进程(vegeta 攻击器)→ 网关真实二进制(临时配置文件拉起)→ 假上游夹具进程;基线臂用同一夹具直连,相对开销 = 同分位延迟差。恒定速率三档 + 一次阶梯加载,外加缓存命中态与关闭态对比一档(spec 0006 缓存收益的数字背书)。报告入 docs(环境交代、方法学、分位数表、复现命令、Limitations)。
2. **故障演练**:三个场景 family——上游进程 kill -9(HTTP 与 stdio 两种传输)、断流重发(在途调用被断流后客户端重发)、异常注入(上游 panic 与协议外应答,实证网关存活边界)。演练自动化为 e2e 套件的一部分,恢复时间数据(注入到服务恢复的实测窗口)写入 docs 记录。
3. **基础 e2e**:顶层 e2e/ 目录的黑盒套件,零 import 内部包,只经外部可观察面(MCP JSON-RPC、进程退出码、stdout、进程表)断言:真实二进制全链路、支持的协议版本组合握手、退出码契约复核、演练场景。

e2e/ 与 benchmark/ 是架构 §2 布局树已预告的两个顶层目录,本 spec 是它们的诞生 spec(纪律 D10:目录随模块诞生)。压测默认门控跳过,常规 go test 时长与稳定性不受影响。

## User Stories

1. As 网关运维, I want 网关相对开销数字(经网关与直连同一上游的同分位延迟差), so that "为聚合付出多少延迟"有量化答案而非拍脑袋
2. As 网关运维, I want 恒定速率多档压测(至少三档), so that 不同负载水平下的延迟形态可见,且 P99 不受 Coordinated Omission 污染
3. As 网关运维, I want 一次阶梯加载(速率逐级抬升), so that 网关的容量拐点存在性有观察记录
4. As 网关运维, I want 缓存命中态与关闭态的吞吐对比一档, so that 缓存层的实际收益有自己的数字
5. As 报告读者, I want 报告交代完整环境(硬件、Go 与 SDK 版本、进程拓扑、回环口), so that 数字有边界,不被误读为生产容量主张
6. As 报告读者, I want Limitations 章节诚实标注瓶颈位置与方法边界(同机竞争、内存桩上游、无生产网络), so that 对数字可信度的质疑有正面回应
7. As 报告读者, I want 报告含复现命令与全部场景参数(速率档位、时长、样本数), so that 第三方可以独立重跑验证
8. As 维护者, I want vegeta 以库形态引入而非要求安装外部二进制, so that 复现只需要仓库工具链与 go 命令
9. As 维护者, I want 一条显式命令跑完全部压测并产出报告, so that 数字可重取、跨时点可对比
10. As CI, I want 压测默认门控跳过(常规 go test ./... 不触发长负载), so that 回归时长与稳定性不受影响
11. As 网关运维, I want HTTP 上游进程 kill -9 的完整行为记录(在途调用、后续调用、恢复), so that 上游骤死的影响面有实证而非推测
12. As 网关运维, I want stdio 上游子进程 kill -9 的行为记录与恢复路径(重启网关), so that 两种传输的故障语义差异可见并可写入运行说明
13. As Agent 用户, I want 上游故障期间的调用得到点名具体上游的明确错误, so that Agent 能把故障归因到上游而非网关自身
14. As Agent 用户, I want 在途调用被断流后,上游恢复时重发同一调用即成功, so that 客户端重试是可靠的恢复策略
15. As 网关运维, I want 上游异常注入(panic、协议外应答)后网关进程存活且后续调用照常, so that 上游的异常不放大为网关不可用
16. As 维护者, I want 每个演练场景的恢复时间数据(注入时刻到服务恢复的实测窗口)入 docs, so that 可用性叙述有数字支撑
17. As 维护者, I want 演练暴露网关崩溃路径时在时间盒内做最小修复并补定点测试, so that 韧性缺口不悬空(修订表追记留痕)
18. As Agent 用户, I want 真实二进制 + 真实配置文件 + 真实监听端口的端到端验收(initialize/tools/list/tools/call 全链), so that 交付形态本身有黑盒判据,装配级测试之外多一层
19. As Agent 用户, I want 支持的协议版本组合有黑盒握手判据(最新版、上一代版、混合上游链路), so that "支持哪些版本"的声明有测试背书
20. As 网关运维, I want 退出码契约(0 成功/1 配置错误/2 启动失败)在真二进制上复核, so that 编排系统判活脚本依赖的语义有黑盒钉子
21. As 测试维护者, I want e2e 与 benchmark 零 import 内部包, so that 套件只可能断言外部行为,内部重构不牵动它们
22. As 测试维护者, I want HTTP 假上游以可执行夹具落地(与 stdio 夹具同位同归属), so that kill -9 与断流演练有真实进程可注入
23. As CI, I want e2e 套件进入常规 go test ./..., so that 回归门禁覆盖交付形态而不只是装配形态
24. As 维护者, I want 本 spec 默认形态生产代码零改动(diff 纪律), so that 产出是证据而非行为变更,验收可机械核对
25. As 维护者, I want 压测报告与演练记录只含工程语境(无教学与求职叙事), so that 仓库内容纪律自本 spec 起维持,W4 公开前零额外清洗
26. As CI, I want go build ./... 与 go test ./... -race 全部包全绿为机械判据, so that 验收不依赖主观

## Implementation Decisions

- **三块交付与存放**(架构 §2 布局树预告行落地):
  - `e2e/` 顶层包:黑盒端到端套件(基础 e2e + 演练自动化);零 import internal 包,网关与上游一律以 `go build` 出二进制、临时 YAML 配置拉起(配置内容以字符串模板写入临时文件),Agent 侧用 go-sdk 客户端连真实端口
  - `benchmark/` 顶层包:vegeta 场景代码;同样零 import internal 包;环境变量门控(缺省 skip,显式环境变量 + go test 命令触发全量),烟测子集随常规回归门控内运行
  - `docs/benchmark.md` 压测报告、`docs/drills.md` 演练记录:恢复时间数据与压测数字的唯一沉淀处,W4 README 引用;两份文档只含工程语境(计划 §9 发布卫生定则)
- **新可执行夹具**:`upstream` 包 testdata 下新增 HTTP 假上游夹具(内部包外唯一动 upstream 包的点,且只在 testdata),工具族 echo(回显参数字节,零延迟,压测负载)/ fail(业务失败 IsError)/ slow(固定延迟,断流与超时场景)/ pid(自报 PID,供 kill -9 定位),另含两种异常注入形态——panic 模式(处理器 panic,SDK 生产代码无 recover,上游进程自身崩溃,与 kill -9 语义并案实证)与协议外应答模式(裸 HTTP 层对特定工具名返回非 JSON-RPC 应答或错误 Media Type,探测网关解析路径)。归属沿 stdioserver 先例:夹具归 upstream 包所有,e2e/benchmark 只经构建产物使用
- **vegeta 引入**:go-sdk 之外唯一新第三方依赖(vegeta v12 模块路径,库形态:Attacker + ConstantPacer + Metrics 分位数,无需 vegeta 二进制);go.mod 单一出处,场景参数(速率档位、每档时长、请求体模板)为 benchmark 包内常量,报告列明
- **压测方法学**(计划 §3/§5 定稿):恒定速率(避免 Coordinated Omission)至少三档 + 一次阶梯(速率逐级抬升看拐点);相对开销主张 = 攻击器分别打网关端点与直打上游夹具端点,同分位(P50/P95/P99)相减;三进程同机回环拓扑如实写进报告环境节。缓存场景:public 规则命中态 vs 缓存关闭态各一档(缓存键含 args,靶体固定参数保证命中)
- **压测配置形态**:policy/telemetry 段缺省(策略、审计、限流、追踪全关闭态),cache 两形态(关/开 public 规则);vegeta 靶为 Stateless 裸 POST tools/call(SDK serveStateless 每请求建临时会话,无需 initialize——v1.8.0 源码核查;攻击前烟测一例裸调用成功钉住此前提,前提不成立则靶体改为 initialize+call 配对并在报告方法节记录)
- **演练场景定义**(计划 §3 三 family,记录格式统一):每场景一条记录 = 场景名、前置拓扑与配置、注入手段(命令级可复现)、预期、实际(含关键错误文案)、恢复时间数据(轮询采样,注入时刻到首次成功调用的窗口)、结论与 Limitations 归档项。三个 family:
  - **上游 kill -9**:HTTP 夹具与 stdio 夹具各一例,空载与在途(slow 工具进行中)两种时机;断言网关进程存活、故障上游调用点名不可用、健康上游不受影响、恢复路径(HTTP:上游进程重启后调用恢复;stdio:子进程无再拉起机制,网关重启后恢复——spec 0003 简化策略的实证)。SDK 客户端会话对上游重启的容忍度是 SDK 行为事实,演练实测:若上游重启后会话不可自愈需网关重启,如实记录为 Limitations 与运行说明条目
  - **断流重发**:slow 工具在途时杀上游,在途调用失败后,上游恢复,同一 Agent 会话重发同一调用成功;记录"注入到重发成功"窗口。网关侧不加自动重试机制(演练记录的是客户端重发语义,非网关新能力)
  - **异常注入**:panic 模式(上游处理器 panic → 上游进程崩溃,按 kill -9 语义并案)与协议外应答(畸形 JSON-RPC/错误 Media Type 流向网关解析路径);预期该调用得到明确错误、网关存活、后续调用照常,实测记录 net/http 每连接 panic 隔离与 SDK 解析层报错的真实边界
- **应急修复条款**:演练若暴露网关进程级崩溃路径(超出 net/http 单连接隔离),本 spec 时间盒内以最小修复收口(候选:transport 层 recover 中间件)+ 定点单测,并在修订表追记;预期不发生,发生即留痕
- **协议版本组合**(计划"我支持的版本组合"):声明支持面 = SDK v1.8.0 supportedProtocolVersions 五版;e2e 钉三个组合:最新版(2026-07-28)握手 + 全链调用、上一代版(2025-11-25)握手且混合上游链路(http 默认协商 + stdio Pin 2025-11-25)调用成功、更老版(2025-06-18)握手成功;伪造的未支持版本行为实测记录(SDK 协商语义),不做 5×N 矩阵扫描(计划已砍)
- **退出码契约复核**:真二进制上复核三态(0 成功/1 配置错误/2 启动失败),字面值沿 main 包既有契约测试,不新增语义
- **生产代码零改动为默认形态**:diff 检查 = 新增 e2e/、benchmark/、testdata 夹具、docs 两份、go.mod 一行依赖;internal/ 与 cmd/ 生产文件零改动(应急修复条款触发除外);已有 12 包测试零改动

## Testing Decisions

- 好测试的标准沿用 spec 0002-0006:只从接缝断言外部行为。本 spec 的接缝升至最高一层——**真实进程边界**:网关二进制的 stdout/退出码/监听端口、MCP JSON-RPC 应答与错误文案(点名上游语义)、进程表扫描(无孤儿,fixturePids 先例)、vegeta metrics 分位数、恢复时间轮询采样。不断言网关内部结构,零 import internal 包使这一纪律由编译器守门
- **e2e 套件用例**(常规回归,门控外):版本组合三例握手与全链调用、混合上游真进程调用、退出码三态复核、kill -9 与断流重发与异常注入的演练自动化、夹具进程无孤儿核对
- **benchmark 用例**:烟测(单档短时长压测,验证攻击器-网关-夹具三进程 harness 正确性与裸 POST 前提,门控内随常规回归跑);全量场景(三档 + 阶梯 + 缓存对比)显式命令触发;压测数字本身不作硬验收门槛(环境依赖,同机数字只用于相对比较),机械判据 = 套件可跑、报告含复现命令且参数与代码常量一致、烟测无错误响应
- **既有先例**:cmd 混合上游 e2e(真实装配路径 + stdio 夹具 kill -9 轮询判据)、stdio 可执行夹具归属 upstream testdata、TestMain go build 构建模式、错误文案点名上游断言——全部沿用;唯一新基建是 HTTP 夹具与 vegeta 攻击器 harness
- 全部测试 `-race`;e2e 套件时长控制在常规回归可接受量级(演练轮询窗口设上限)

## Out of Scope

- 5×N 协议版本一致性矩阵、跨 SDK 版本组合扫描(计划已砍,本 spec 只钉支持组合的代表例)
- 生产网络/分布式/满配容量主张;OS 与内核参数调优;基准实验室级环境控制
- pprof/trace 级瓶颈剖析(报告只按计划要求诚实标注瓶颈位置,不开剖析面)
- OTel metrics 管线、evalx、trace 录制器(W4)
- 网关侧自动重试、熔断、上游健康探测(演练记录客户端重发语义即可,网关不加机制)
- singleflight 并发击穿合并(spec 0006 Limitations 维持)
- 配置热加载、多网关实例、连接池化(spec 0003 简化策略不变)

## Further Notes

- 时间盒:14h(压测 6h + 演练 4h + 基础 e2e 4h,计划 §3/§4)。**超时降级顺序**:压测砍阶梯与缓存对比场景、只保恒定三档(计划降级形态)→ 演练自动化降为半自动(脚本 + 人工注入,docs 记录照出)→ 基础 e2e 只保版本握手与故障演练子集、退出码复核可砍;降级发生时本 spec 修订表追记
- 完成判据(CI 可机械验证):`go build ./...`;`go test ./... -race` 全部包全绿(新增 e2e、benchmark 两包)且既有测试零改动;diff 检查:internal/ 与 cmd/ 生产文件零改动(夹具只在 testdata),新增顶层 e2e/ 与 benchmark/ 零 import internal,go.mod 仅增 vegeta 一条直接依赖。人工判据:压测报告含相对开销数字、环境交代、Limitations 与复现命令;演练记录含三 family 全场景与恢复时间数据;同一命令重跑数字同量级
- 报告与记录的 W4 衔接:README 的压测表与 Limitations 章节直接引用 docs/benchmark.md 与 docs/drills.md;两份文档现即按公开仓标准撰写(工程语境),公开前零清洗
- 依据链:计划 §3 压测/演练/基础 e2e 三行(时间盒与降级形态)、§4 W3 周验收、§5 测试策略、§9 发布交付;架构 §2 布局树 e2e//benchmark/ 行、§8-3(砍 stdio 演练的现行口径);spec 0006 Out of Scope(三块归本 spec)、spec 0003(协议 Pin、夹具先例、单进程无池化——stdio 恢复路径=网关重启的依据)、spec 0005(HTTP 中间件链——压测关闭态的配置依据);research/06(裁剪证据:相对开销主张不需满配);go-sdk v1.8.0 源码核查(serveStateless 临时会话/supportedProtocolVersions/生产代码零 recover——异常注入场景的实证起点)
- 报告与记录的证据定位:压测方法学(恒定速率避免 Coordinated Omission、相对开销主张不需满配)、演练记录(两种传输的故障语义差异、恢复时间数据)、版本组合判据——三者构成仓库内自足的运行质量数据源,支撑 README 性能表与 Limitations 章节,不依赖仓库外文档

## 修订

| 日期 | 变更 |
|---|---|
| 2026-11-08 | 发布(ready-for-agent)。测试接缝定为最高一层(真实进程边界:e2e/ 与 benchmark/ 零 import internal,黑盒断言),沿架构 §2 已预告的顶层目录,不新增生产接缝;vegeta 以 v12 库形态引入(唯一新第三方依赖);压测默认门控不进常规回归;演练三 family 与记录格式按计划 §3 定稿;应急修复条款(演练暴露崩溃路径时时间盒内最小修复)为唯一预设的生产代码改动通道 |
| 2026-11-08 | 实现完成定稿(三票合入、双轴评审硬违规零、收口提交)。四处实现裁决追记:①协议版本组合实为四例(评审收口补第四例)——伪造未支持版本("1999-01-01")实测语义=握手成功、静默降级至 2025-11-25 不报错(SDK negotiatedVersion 回支持面中次新版,MCP 规范一致),钉进测试注释;②SDK 会话容忍度分界实证:传输层失败(EOF/RST/refused)不关闭会话、HTTP 上游重启后毫秒级自愈;协议违规(非法 Media Type)永久关闭会话、同一上游恢复须网关重启——"后续调用照常"按此收窄,记 Limitations 与运行说明条目(stdio 无再拉起=spec 0003 简化策略实证);③e2e/benchmark 两包 doc 注释立自足纪律(互不 import、harness 形状复制自 cmd 先例、共享需先立架构布局内共享包),10s 进程窗口两包同名常量消漂移;④vegeta v12.13.0 三事实钉子(Attacker 单次使用/默认 transport 带 ProxyFromEnvironment 劫持回环须显式 Proxy(nil)/应答恒为 SSE data 帧)。另:降级未发生(恒定三档+阶梯+缓存对比全量落地;演练七场景全自动化;应急修复条款未触发——七场景网关进程均存活)。机械判据:14 包 `go test ./... -race` 全绿,既有测试零改动,internal/ 与 cmd/ 生产文件零改动(仅 testdata 夹具),e2e/benchmark 零 import internal 经 go list 机械核对;全量压测一次运行零错误全 200(相对开销 P99 +1.2~2.1ms,缓存命中 P50 −64%,阶梯 50→800 rps 无拐点,瓶颈在 800 以上未测定如实标注);架构文档 v1.5 回填布局树两行 |
