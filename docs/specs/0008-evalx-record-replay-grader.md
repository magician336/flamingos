---
id: 0008
title: "evalx 最小闭环:工具调用录制、回放与确定性 grader 门禁"
triage: ready-for-agent
date: 2026-11-08
source: 开发计划 §1.1(核心能力第 1 位:eval 内建,六家竞品全空白,research/05)+ §3(evalx 最小闭环 8h:录制+回放+确定性 grader+CLI 表格/JSON/退出码,无 LLM judge;超时降级=砍回放,保录制+grader+CLI)+ §4 W4 首行 + §5 测试策略(evalx 自举:W4 起作为回归门禁)+ 架构 §1(recorder 预留接缝)/§2(internal/recorder W4 建)/§3(W4 占位"trace 录制",实落位置以实现为准并回填)+ research/06 §3(promptfoo Deterministic/Model-assisted 官方二分法:确定性断言是业界基座而非简化妥协;断言面=响应等值/json-schema 校验/tool 调用序列/错误形态/延迟阈值;用途定位=回归门禁——上游与网关变更后回放验证行为不回归)+ spec 0007 Out of Scope(evalx 与 trace 录制器归 W4)+ 会话 AskUserQuestion 确认(①spec 粒度两张,本 spec 只含 evalx,发布就绪归 0009;②接缝方案 A:录制=mcp 层 Receiving 中间件沿 audit 先例、回放引擎本地小接口、门禁=e2e 套件新增 family 零新 harness)
tracker-note: 本地 tracker;W4 GitHub 公开时迁为 issue 并加同名 label
---

# Spec 0008 — evalx 最小闭环:工具调用录制、回放与确定性 grader 门禁

## Problem Statement

网关的功能面(聚合转发、全链路追踪、策略审计限流、结果缓存,spec 0002-0006)与运行质量证据(压测、演练、黑盒 e2e,spec 0007)都已齐,但缺"行为正确性"的回归基准:网关代码演进(重构、升级依赖、新增能力)之后,没有机制验证"同一批调用仍得到同一批结果"——既有 14 包测试都是开发者视角的断言,没有"以录制到的真实调用为基准、机器可判、退出码可门禁"的行为基准线,回归全靠人肉核对测试清单。同时这是竞品空白带(research/05:六家竞品零家内建 eval)与计划核心能力排序第一位的最后一块;业界主流 eval 工具的断言基座本就是确定性的(promptfoo 官方二分法),确定性 grader 不是简化妥协而是基座形态。计划 §5 定稿的"evalx 自举:W4 起作为回归门禁"落在本 spec。

## Solution

三件套交付:

1. **录制**:internal/recorder 落地(架构 §1 预留接缝诞生)。以 mcp 层 Receiving 中间件形态挂网关既有中间件链(audit 同形:格式知识自有、JSON-lines 落盘、构造注入 ctx 键访问器),config 新段缺省 = 关闭零挂点。只录实际执行的 tools/call(方法、聚合工具名、参数原始字节、成功/业务失败/传输错误三态、延迟、trace_id 与角色可空),deny 不入录制。
2. **回放 + grader**:internal/evalx 回放引擎 + 五种确定性 grader(结果等值/错误形态/json-schema 校验/序列匹配/延迟上限),引擎只依赖记录格式与本地小接口(SDK 机器类型禁入);cmd/evalx 第二二进制作为 CLI——连运行中的网关端点串行回放,输出表格(默认)或 JSON(--json)报告,退出码三态。
3. **门禁自举**:e2e 套件新增 evalx family(复用 spec 0007 真二进制 harness,零新基建)——正例(录制→回放→全过)、负例(上游应答注入差异→回放必报失败且退出码非 0,防 grader 永真)、CLI 契约例(连不上端点→退出码 2)。随常规 go test ./... 运行,"自举为回归门禁"自本 spec 起兑现;GitHub Actions 引用归 spec 0009(CI 工作流)。

## User Stories

1. As 网关维护者, I want 用配置声明录制(JSONL 路径必填、段缺省 = 关闭零挂点), so that 行为基准采集不改代码,不需要录制的部署零开销零风险
2. As 网关维护者, I want 每次实际执行的 tools/call 依序落 JSON-lines(方法、聚合工具名、参数原始字节、结果或错误、延迟), so that 录制文件是完整可回放的行为基准
3. As 网关维护者, I want 传输错误与业务失败在录制中可区分(错误文案与 IsError 分开记录), so that 基准能区分"上游不可用"与"上游报业务失败"两类回归
4. As 网关维护者, I want 录制条目冗余 trace_id 与角色(telemetry/策略关闭态留空), so that 录制文件可与 Jaeger 排查对查、与策略语义对账
5. As 网关维护者, I want 录制条目携带格式版本字段, so that 未来格式演进有升级锚点而非无标识漂移
6. As 网关维护者, I want 录制只含通过策略判定的调用(deny 不入录制), so that 基准表征网关的工具处理行为而非策略配置——策略回归由既有定点单测与 e2e 覆盖
7. As 网关维护者, I want recorder 段缺省时网关行为与现状逐字节一致, so that 引入本能力对既有部署零行为风险
8. As evalx 使用者, I want 一条命令对运行中的网关回放录制文件, so that 网关行为与基准是否一致由机器判定
9. As evalx 使用者, I want 结果等值断言按 JSON 语义比较(对象键序无关、数组有序), so that 无害的序列化差异不产生误报
10. As evalx 使用者, I want 业务失败与传输错误形态各自断言(传输错误文案点名上游,沿 e2e 既有断言先例), so that 失败路径的回归同被门禁
11. As evalx 使用者, I want 可选 json-schema 断言校验结果形状, so that 形状类期望不必逐字节钉死
12. As evalx 使用者, I want 序列匹配断言(回放顺序与条目覆盖 = 录制顺序), so that 路由或聚合的行为漂移(错序、漏调、多调)被门禁
13. As evalx 使用者, I want 可选的单条目延迟上限断言, so that 粗粒度性能防线与行为基准共用同一机制
14. As evalx 使用者, I want 录制文件即回放用例(单文件自足,期望可显式覆写), so that 用例管理只有一个文件,特殊期望不需要第二套格式
15. As evalx 使用者, I want 回放以录制时的角色重放(请求头透传), so that private 缓存与策略语义在回放时与录制时同口径
16. As CI 编排者, I want CLI 退出码三态(0 全过/1 存在断言失败/2 自身运行错误), so that 门禁脚本对"行为回归"与"环境错误"可区分处置
17. As CI 编排者, I want JSON 报告(条目数、通过数、失败明细含条目号与 grader 名), so that 门禁失败原因机器可读、可直接进日志
18. As CI 编排者, I want 默认表格报告(人读), so that 本地排查不需要读 JSON
19. As 回归门禁, I want e2e 内的正例(真网关录制态配置→采集→回放→全过退出码 0), so that "自举为回归门禁"每轮 go test 即兑现
20. As 回归门禁, I want e2e 内的负例(上游应答注入差异→同一录制文件回放必报失败且退出码 1), so that grader 永真(怎么跑都绿)被结构排除
21. As 回归门禁, I want e2e 内的 CLI 契约例(连不上端点→退出码 2), so that 环境错误不误判为行为回归
22. As 维护者, I want evalx 引擎不依赖 SDK 机器类型(SDK client 适配只存在于 CLI 层), so that 引擎可单测桩驱动,SDK 升级不牵动 grader
23. As 维护者, I want 零新增第三方模块(jsonschema 校验用已在依赖图内的库转正), so that 依赖面审查成本不增加
24. As 维护者, I want 缓存开启态的录制/回放语义如实记入 Limitations, so that 跨配置回放的误报有文档解释而非静默坑

## Implementation Decisions

- **模块与存放**(架构 §2 布局树回填随关票落地):
  - internal/recorder:录制中间件 + 记录格式 + JSONL sink。格式知识自有(U4 同款裁决,不为两份 JSONL 落盘抽共享包);并发落盘互斥,seq 分配与行序同锁保证一致
  - internal/evalx:记录格式消费端——回放引擎、五种 grader、报告构造。定义本地 Caller 小接口(单方法:执行一次聚合调用,入参聚合名 + 参数字节 + 请求头,出参结果 JSON/错误三态),结构化类型无 import 即满足;SDK 机器类型禁入引擎(D4 纪律延伸)
  - cmd/evalx:CLI 二进制,SDK client 适配只在此(flags:录制文件、网关端点 URL、可重复的请求头透传含角色头、报告格式);把 go-sdk 应答翻译为引擎吃的 JSON 形态
- **录制挂点**:mcp 层 Receiving 链末位(判定内层)——链序现状为 trace → 决策桥 → 审计 → RBAC 判定(spec 0005),录制器追加在判定之后,deny 路径不下探故不入录制。只处理 tools/call,其余方法原样下探零改动;transport 新增接线选项,cmd 装配沿 audit 先例翻译 config 段并注入 ctx 键访问器(trace_id 经 telemetry 访问器闭包注入,角色经 policy 访问器注入,关闭态为空)
- **记录格式**(JSON-lines,单行一次调用):`v`(格式版本)、`seq`(文件内自增)、`time`、`method`(恒 tools/call)、`tool`(聚合名)、`args`(参数原始字节)、`kind`(成功|业务失败|传输错误)、`result`(成功与业务失败的应答内容,SDK 应答类型经标准 JSON 序列化的形态)、`error`(传输错误文案)、`latency_ms`、`trace_id`(可空)、`role`(可空)。序列化形态由录制端与回放端共用,等值断言做语义级比较
- **grader 五种**(research/06 §3 断言面落地):①结果等值——JSON 语义等值(键序无关、数组有序),适用成功与业务失败形态;②错误形态——kind 与期望一致,传输错误文案含上游点名(spec 0002 透传语义);③json-schema——条目可选 schema 字段校验 result;④序列匹配——文件级:回放条目数、聚合名序列与录制一致;⑤延迟上限——条目可选阈值字段,超限即败,缺省不断言
- **golden + 覆写**:录制文件即回放用例(录到的应答就是期望);条目可选字段(如 schema、延迟上限)经显式手编覆写,无第二套用例格式
- **CLI 契约**:退出码 0(全部通过)/1(存在断言失败)/2(自身运行错误:文件不可读、端点不可达)——沿网关既有 0/1/2 语义;报告表格默认、--json 机器形态;串行顺序回放(不并发)
- **缓存交互**:录制/回放配置若开缓存,录制文件可能含命中态结果,跨配置回放可能误报——门禁场景(e2e family)配置缓存关闭;此边界记 Limitations,不加防呆机制
- **依赖**:零新增第三方模块;json-schema 校验用已在依赖图内的 google/jsonschema-go(SDK 间接依赖)转正为直接依赖,go.mod 净增一条
- **门禁自举形态**:e2e 套件新增 evalx family 三例(复用真二进制 harness 与夹具,录制态配置经临时 YAML):正例(夹具确定性应答→录制→回放→退出码 0 + JSON 报告全过)、负例(夹具注入应答差异→同文件回放→退出码 1 + 失败明细点名)、CLI 契约例(不可达端点→退出码 2)。时长控制在常规回归量级(不设环境变量门控)
- **架构回填义务**:关票时回填架构文档 v1.6——§1 模块清单补 recorder/evalx 两行、§2 布局树补 internal/recorder、internal/evalx 与 cmd/evalx 三行、§3 W4 占位"trace 录制"改实落口径(Receiving 链末位而非 SendingMW)

## Testing Decisions

- 好测试的标准沿 spec 0002-0007:只从接缝断言外部行为。本 spec 三层接缝:①录制——经 transport 装配级测试(audit/telemetry 装配测试同形,httptest 同进程)断言 JSONL 行内容与关闭态零挂点;②引擎——Caller 接口注入桩,表驱动五种 grader(等值/错误形态/schema/序列/阈值)与退出码映射,零网络;③CLI——契约测试在 e2e 内对真网关执行(真实端到端,含 JSON 报告字段与退出码)
- 既有先例:audit 中间件与 JSONL 落盘测试、benchmark 门控形态(本 family 不门控,时长可控)、e2e 真二进制 harness、错误文案点名上游断言
- 全部测试 `-race`;既有 14 包测试零改动;recorder 段缺省时网关行为逐字节不变由既有装配测试钉住

## Out of Scope

- LLM judge、多采样方差分析、影子测试(计划不做清单;引擎不预留 LLM 钩子,"何时需要 LLM judge"边界话术归 README,发布 spec 0009)
- 录制文件脱敏与加密(README 使用说明加一句警示:录制含工具结果原文)
- 并发回放、批量录制文件聚合、跨文件用例编排(串行单文件即可)
- trace 与录制文件的联合分析界面(trace_id 冗余入记录仅为人工对查)
- 录制格式跨版本迁移工具(v 字段留痕,不做迁移器)
- 策略行为基准(deny 不入录制;策略回归由 spec 0005 定点单测与 e2e 覆盖)
- 网关自动重试、singleflight、配置热加载(既有 Limitations 维持)

## Limitations

- **缓存开启态的录制/回放误报边界**(US24 落点;门禁场景缓存关闭):录制/回放配置若开缓存,录制文件可能含命中态结果,跨配置回放可能误报——门禁场景(e2e family)配置缓存关闭,不加防呆机制。此为 Implementation Decisions"缓存交互"行的内容升格为显式边界条目(spec 0008 修订表另记第二条跨配置回放边界:SEP-2575 注记不对称,门禁 family 采集面限 http 上游条目)
- **录制文件含工具结果原文的使用警示**:录制条目的 result 字段是工具应答原文,落盘文件即包含上游返回的全部内容,对不可信环境共享录制文件前须自行脱敏。脱敏与加密归 Out of Scope(README 使用说明的警示文案在 spec 0009 落地);本条是该警示的 spec 侧载体,发布交接时并入 README Limitations

## Further Notes

- 时间盒 8h(计划 §3)。**超时降级顺序**:砍回放(保录制 + grader + CLI 报告与退出码,门禁自举随之降为 grader 单测 + 录制集成测试,计划降级形态)→ 再砍 schema 与延迟两种 grader(保等值/错误形态/序列三核心)→ e2e family 只保正例。降级发生时本 spec 修订表追记
- 完成判据(机械可验):`go build ./...`;`go vet ./...`;`gofmt -l` 触碰文件零输出;`go test ./... -race` 全部包全绿(新增 internal/recorder、internal/evalx、cmd/evalx 均入回归);既有测试零改动;recorder 段缺省时既有装配测试零改动即证明关闭态零行为变化;go.mod 净增一条直接依赖(jsonschema-go 转正)
- 与 spec 0009 的衔接:本 spec 交付"门禁可被 CI 调用"的测试形态(GitHub Actions 引用与公开仓交付归 0009);README 的 evalx 用法节引用本 spec 的 CLI 契约
- 依据链:计划 §1.1(核心能力第 1 位)、§3(evalx 行:时间盒 8h 与降级形态)、§4 W4、§5(evalx 自举);架构 §1(recorder 预留接缝)、§2(internal/recorder W4 建)、§3(W4 占位);research/05(0/6 竞品)、research/06 §3(promptfoo 二分法、断言面清单、回归门禁定位);spec 0007 Out of Scope(evalx 归本 spec)、spec 0005(中间件链序与审计先例)、spec 0002(错误透传点名上游语义)、spec 0006(缓存交互边界)

## 修订

| 日期 | 变更 |
|---|---|
| 2026-11-08 | 发布(ready-for-agent)。接缝三处定稿:录制=mcp 层 Receiving 链末位(判定内层,deny 不入录制,audit 同形)、回放=引擎本地 Caller 接口(SDK 类型禁入,CLI 层适配)、门禁=e2e 新增 family 零新 harness;grader 五种与 golden+覆写定稿;负例(防 grader 永真)为门禁硬性组成;CI 工作流归 spec 0009 |
| 2026-11-08 | 实现裁决留痕(自三票定档留痕提炼;**时间盒降级未发生**——录制、回放、五种 grader、CLI 双报告与三态退出码、e2e 门禁 family 三例全量落地,无砍项):①录制段落位为配置顶层 `recorder` 段(非 policy 子段,录制是 eval 能力而非策略语义);kind 字面量取英文三态 success/business_failure/transport_error(消费端为机器可读报告,中文不进机器字段);seq 分配与整行写盘同锁,序列化失败不消耗序号、整条丢弃并记日志(录制文件 seq 无空洞优先),写盘失败沿 audit 口径只记日志不反噬数据面。②引擎与 CLI:Case 内嵌 recorder.Record(依赖单向 evalx → recorder 只取记录形态);go-sdk 应答经 json.Marshal(*mcp.CallToolResult) 翻译,与录制端同库同类型同序列化;传输错误文案经 SDK 透传原子串保留(`上游 "ns"` 点名锚点在回放文案可重现);--header 经自定义 RoundTripper 的 ctx 通道注入(SDK CallTool 无 per-call header 旋钮);jsonschema-go v0.4.3 转正为直接依赖,go.mod 净增恰一条。③e2e 门禁 family 实测边界(SEP-2575 注记不对称):录制落盘的是网关 Receiving 链内 handler 应答对象(SDK 应答注记之前),回放端线路应答在新协议下被网关注记 _meta serverInfo(go-sdk v1.8.0 annotateServerInfo)——http 上游夹具与网关新协议对话、应答已自带上游注记,网关见键已占不再注记,录制/回放两侧形态一致;stdio 上游客户端钉 2025-11-25 旧协议、上游不注记,录制形态无 _meta 而回放形态含网关注记,result_equal 必然误报——门禁 family 采集面限 http 上游条目,stdio 转发行为由既有 fullchain e2e 覆盖(缓存交互之外的第二条跨配置回放边界,以本行留痕替代 Limitations 增补);负例差异源=夹具既有 inject 形态 rawreply 的含点名 transport_error 对默认形态世界的无点名 unknown tool 错误,error_shape 检出,grader 永真被结构排除 |
| 2026-11-08 | 序列 grader 结构永真裁决收口(票 02 留痕在案的裁决升格入修订表):序列匹配现行语义 = 串行单发引擎下的**管线完整性断言**——回放执行台账与用例同源(引擎逐用例填写台账),非独立观测面;"漏调/多调被门禁"的完全可败面随引擎演进(跳条/重试/并发改动)兑现,现行可败面(管线丢行/重复行/错位)由纯函数四形态表驱动测试(对齐/错序/漏调/多调)钉住 |
| 2026-11-08 | "既有测试零改动"判据口径修正(票 03 明定接线点,以本行留痕为准,不重写历史提交):既有测试**零断言变化**;e2e_test.go 的 TestMain 构建清单三改四与孤儿核对纳入 evalxBin 为票面明定的必要接线,属接线改动而非断言改动 |
| 2026-11-08 | 双轴评审 judgement call 修复留痕(代码六项 + 文档一项,断言文案逐字保留、既有测试零断言变化):①头注入收敛为 CLI 构造期 RoundTripper 单一路径——上一行"实现裁决留痕②"所记"--header 经自定义 RoundTripper 的 ctx 通道注入"自本行起过时,connectCaller 的 headers 形参死通道退役,引擎 Caller 接口与 Replay 收窄去掉逐条 headers 形参(接口收窄仅及本 spec 新增代码);②replay 关闭会话失败文案改经注入 errOut(os.Stderr 直写归零);③cmd 装配测试恒真式编译期钉子改 buildRecorder 真实签名断言;④headerTransport 经 req.Clone 副本设头(RoundTripper 契约禁改传入请求);⑤gradeCase 按 grader 拆子函数,行为零变化;⑥transport 录制测试同一录制文件连读两次改读一次复用;⑦架构 §1 预留接缝行按已落地口径收口(架构 v1.7)与 Limitations 节落地(US24/Out of Scope 警示的 spec 侧载体) |
| 2026-11-09 | 关闭(三票 done;双轴评审硬违规零,Standards 7 项 judgement call 全处置,Spec 缺失 1+疑似不符 2 全收口;时间盒降级未发生;机械判据:16 包 go test ./... -race 全绿,断言文案逐字保留,既有测试零断言变化) |
