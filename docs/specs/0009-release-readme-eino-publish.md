---
id: 0009
title: "发布就绪:Eino 示例、README 与公开仓交付"
triage: ready-for-agent
date: 2026-11-08
source: 开发计划 §1.1(核心能力第 5 位:Eino 最简示例,官方 94 行)+ §3(Eino 行 2h,超时降级=README 配置说明)+ §4 W4(README + Limitations 4h;GitHub 发布 2h)+ §9 发布与交付(GitHub 公开:Apache-2.0、CI、README 含架构图/压测表/Jaeger 截图/Limitations;发布卫生定则;历史处理"届时拍板"——本 spec 拍板)+ 架构 §2(examples/ W4 建)+ research/06 §4(eino-ext officialmcp 组件一手证据:mcp.go 588 行 + session 381 行,官方示例 94 行,Config{Cli} 直吃 ClientSession;issue #1006 为远程带鉴权路径的已知边界,对连自家网关无影响)+ spec 0007 Further Notes(README 压测表与 Limitations 章节直接引用 docs/benchmark.md 与 docs/drills.md;两份文档已按公开仓标准撰写)+ spec 0008(README evalx 用法节引用其 CLI 契约;e2e evalx 门禁 family 被 CI 引用)+ 会话 AskUserQuestion 确认(①公开历史=orphan 无祖先初始提交重建,私有仓保留完整历史;②Apache-2.0 版权行署名 magician336,公开仓名沿用 flamingos)
tracker-note: 本地 tracker;W4 GitHub 公开时迁为 issue 并加同名 label
---

# Spec 0009 — 发布就绪:Eino 示例、README 与公开仓交付

## Problem Statement

仓库当前无 README、无 LICENSE、无 CI、无公开远端:八个 spec 的功能面与全部运行证据(压测数字、演练记录、全包 -race、evalx 行为基准门禁)都存在,但外部观察者视角为零——入仓第一屏(这是什么、怎么跑、边界在哪)不存在,变更无自动门禁,许可缺失即法律上不可复用。发布是计划 §9 的硬交付;spec frontmatter 的 tracker-note("公开时迁为 issue")与计划 §9 留置的公开历史策略拍板也一并落在本 spec。本 spec 是仓库从"私有开发仓"转为"可公开复用仓"的交付面动作,零网关行为变更。

## Solution

五件交付:

1. **Eino 示例**:顶层 examples/ 目录落地(架构 §2 预告行),内部为独立 Go module。官方 eino-ext officialmcp 组件把网关聚合工具桥接为 Eino 工具,示例无 LLM key 依赖(桥接 + 直调网关聚合工具一次),连接网关端点走 Streamable HTTP transport;接 ReAct/模型的接线方式在 README 说明。
2. **README + Limitations**:第一屏定位与架构图(mermaid,GitHub 原生渲染)、quick start(最小配置 + 启动命令 + Inspector 连接指引)、能力总览表(各能力一行 + 证据文档链接)、压测与演练数字直引 docs/benchmark.md 与 docs/drills.md、Limitations 全清单(砍掉项、简化项、已知边界)、evalx 用法节、"何时需要 LLM judge"边界说明。Jaeger 全链路截图入 docs 下专用图片目录。
3. **LICENSE**:Apache-2.0,版权行 magician336。
4. **CI**:GitHub Actions 工作流——checkout、按 go.mod 版本装 Go、go build、go vet、gofmt -l 零输出检查、go test -race ./...(e2e 含 spec 0008 evalx 门禁 family 即被自动执行;benchmark 全量靠既有环境变量门控缺省不触发)。另含一个 examples 独立 module 的构建校验 job,防示例腐烂。
5. **公开仓交付**:orphan 无祖先初始提交(内容 = 当前 main 跟踪树,gitignore 私有文档天然不入);推送前列出将公开的完整文件清单经用户确认;推 GitHub 公开仓 flamingos;八张 spec 迁为 GitHub issues 并加与 triage 同名词的 label(tracker-note 兑现),本地 tracker 文件保留在仓内;发布卫生终检(全跟踪树工程语境零命中)。

## User Stories

1. As 仓库访客, I want README 第一屏说清项目定位与架构, so that 不读代码即可判断这是什么、值不值得细看
2. As 仓库访客, I want quick start(最小配置示例、一条启动命令、Inspector 连接指引), so that 五分钟内本地跑通网关
3. As 仓库访客, I want 能力总览表(聚合/追踪/策略审计限流/缓存/evalx 各一行并链到证据文档), so that 能力面与证据入口一屏可见
4. As 仓库访客, I want Limitations 章节列全砍掉项、简化项与已知边界(含单机缓存、同机压测口径、stdio 简化、协议违规会话边界、缓存开启态录制/回放误报边界、录制文件含工具结果原文的使用警示), so that 对项目边界的质疑有正面回应而非隐藏
5. As 仓库访客, I want 压测数字与故障恢复数据以 docs 报告为源引用(不复述不脱源), so that 数字可溯源到方法学、环境交代与复现命令
6. As 仓库访客, I want Jaeger 全链路截图, so that 可观测叙事有直观证据
7. As 仓库访客, I want evalx 用法节(录制配置、回放命令、退出码语义), so that 行为基准机制可自助使用
8. As Agent 框架使用者, I want examples 下把网关工具桥接为 Eino 工具的最小示例, so that 第三方框架接入网关有可复制起点
9. As Agent 框架使用者, I want 示例无 LLM key 即可运行(桥接 + 直调), so that 接入验证不依赖任何厂商账号
10. As Agent 框架使用者, I want README 说明接 ReAct/模型的接线方式与官方示例出处, so that 从直调到 agent 的路径清晰
11. As 复用者, I want Apache-2.0 LICENSE, so that 复用的法律边界明确
12. As 贡献者, I want push 即自动执行 build + vet + gofmt 检查 + 全包 -race 测试, so that 回归门禁不依赖人的记忆与自觉
13. As CI, I want benchmark 全量不进 CI(门控缺省维持), so that CI 时长与稳定性可控
14. As CI, I want evalx 门禁 family 随 e2e 在 CI 执行, so that 行为基准回归自 W4 起自动兑现
15. As 公开仓读者, I want 全跟踪树零非工程语境内容, so that 仓库内容与工程定位一致
16. As 私有仓持有者, I want 公开历史为 orphan 无祖先初始提交且私有文档不在公开树, so that 过程历史保留在私有侧而公开面干净
17. As spec 读者, I want 八张 spec 以 GitHub issues 呈现并带与 triage 同名词的 label, so that 决策记录进入平台原生视图、可被外部引用
18. As 维护者, I want 公开推送前列出将公开的完整文件清单并经确认, so that 外发内容显式过目、无误发
19. As 维护者, I want examples 为独立 module 且主模块 go.mod 零变化, so that 主依赖面不受示例生态牵动
20. As 维护者, I want README 中每个文件路径与文档链接可达且不指向私有文档, so that 第一屏无死链
21. As 评审者, I want 本 spec 零网关行为变更(改动面限于 examples/README/LICENSE/CI/docs/图片), so that 发布是交付面动作,验收可机械核对

## Implementation Decisions

- **examples/ 独立 module**:examples 下自持 go.mod(eino、eino-ext officialmcp、go-sdk 按主模块同版 pin),主模块 go.mod 零变化——eino 依赖树重,依赖面审查成本隔离在示例侧;Go 工具链不遍历嵌套 module,既有 `go build ./...` 与 `go test ./...` 机械判据不受影响
- **示例形态**:桥接 + 直调——officialmcp 的 GetTools 把网关聚合工具转为 Eino 工具接口,经该接口直接调用网关工具一次并输出结果;不含模型段(官方 94 行示例的模型段依赖具体厂商 key);README 说明接入 ReAct agent 的接线与官方示例出处。连接网关端点用 Streamable HTTP transport(官方示例为 SSE transport,差异点在 README 注明;research/06 §4 记录的 issue #1006 是"远程带鉴权 server"路径的已知边界,连自家网关不受影响)
- **README 语言与图**:中文(与仓库全部文档一致,不做双语);架构图用 mermaid 代码块(GitHub 原生渲染,无图片维护成本),Jaeger 截图与架构渲染图入 docs 下专用图片目录——现 assets/ 目录被 gitignore 收编(私有资产),公开图片不入其内
- **引用链纪律**:README 与公开文档不引用任何私有文档路径(docs/plan、docs/research 系列不入公开树);既有 spec frontmatter 的 source 行是文字出处记录(章节号)而非链接,公开树中不构成死链,保留不改
- **CI 工作流**:单 workflow 双 job——主模块(build/vet/gofmt -l 零输出/go test -race ./...)与 examples module(cd 进目录 go build ./...);Go 版本以 go.mod 声明为准;不引入额外校验工具,以首跑绿为准
- **公开历史拍板**(计划 §9 "届时拍板"落定):orphan 无祖先初始提交重建公开历史,内容 = 当前 main 跟踪树;本私有仓保留完整历史作过程证据、保持无 remote 本地状态;公开推送经显式 URL 的临时 remote 执行
- **推送确认纪律**:推送前以 git ls-files 输出将公开的完整文件清单交用户确认;orphan 树与 main 跟踪清单一致性用命令机械核对(无私有文件混入)
- **spec → issue 迁移**:八张 spec 正文为 issue body,label 沿用 triage 同名词(ready-for-agent 等按各 spec 现状);docs/specs 与 docs/tickets 文件保留在仓内不动;仓库 description 与 topics 一并设置
- **发布卫生终检**:全跟踪树按词表(教学、求职、面试、简历、课程等)grep 零命中;specs 与 tickets 的工程语境维持 2026-11-03 清洗纪律;终检结果记入票面留痕
- **架构回填义务**:关票时回填架构文档——§2 布局树 examples/ 行由"W4 建"改已落地口径(与 v1.5 对 e2e/benchmark 两行的处理同款)

## Testing Decisions

- 本 spec 零生产逻辑代码(examples 是示例,README/LICENSE/CI/docs 是仓库工程),判据全部机械化:①主模块 `go build`/`go vet`/`gofmt -l`/`go test ./... -race` 全绿在 CI 环境复现(工作流首跑绿即验收);②examples module `go build ./...` 成功;③orphan 树文件清单 = `git ls-files` 清单(机械核对,零私有文件);④卫生终检 grep 零命中;⑤README 引用的路径与文档逐一可达(脚本或清单核对)
- 示例的可运行验证:示例连 spec 0007 e2e harness 拉起的真网关执行一次直调成功(发布票内一次性验证,不入常规回归——examples 不在主模块 test 闭包内)
- 既有先例:spec 0007 的"报告文档按公开仓标准撰写"纪律(docs/benchmark.md 与 docs/drills.md 即源)、tracker-note 字段本身

## Out of Scope

- 计划 W4 的其余私人交付物(项目自述材料、个人能力证明文档、演练排练),不入仓、不立票、不出现在公开树
- 社区渠道发布(计划:可选,不作为目标)
- 网关功能与行为变更(本 spec 零生产代码改动;docs 版式调整不算行为)
- GitHub 治理配置(issue/PR 模板、分支保护、CODEOWNERS,公开后按需)
- 多语言 README、独立站点、demo 环境部署
- benchmark 全量进 CI(门控缺省维持)、压测数字的持续回归(单次运行观测口径不变)
- Git 历史重写(filter-repo 候选已被 orphan 方案否决,不再评估)

## Further Notes

- 时间盒 8h(README + Limitations 4h、Eino 示例 2h、发布 2h,计划 §4)。**超时降级顺序**:Eino 示例降为 README 配置说明(计划 §3 降级形态)→ CI 降为 build + test 两步(去 vet/gofmt 与 examples job)→ 推送与 issue 迁移单列(README/LICENSE/CI 先入库,公开动作独立执行)。降级发生时本 spec 修订表追记
- 完成判据:公开仓可达且第一屏自足(定位/quick start/能力表/Limitations);CI 首跑绿且 evalx 门禁在 CI 内生效;八张 spec 以 issues 存在;卫生终检零命中;主模块全包 -race 绿(spec 0008 后口径)且本 spec 零生产代码改动;私有仓历史与工作区不被推送动作污染
- 依据链:计划 §9(发布交付清单与发布卫生定则全文)、§4 W4、§3(Eino 行与降级);架构 §2(examples/ 行);research/06 §4(eino 组件一手证据与 issue #1006 边界);spec 0007(README 引用两份 docs 的衔接与公开仓标准撰写先例);spec 0008(evalx 门禁 family 被 CI 引用、CLI 契约被 README 引用)
- 发布卫生定则(计划 §9,2026-11-03)的执行总纲在本 spec 兑现:公开树零"学习/教学/求职策略"内容,即日纪律(新增入库文档一律工程语境)由 0008/0009 两张 spec 起持续维持

## 修订

| 日期 | 变更 |
|---|---|
| 2026-11-08 | 发布(ready-for-agent)。公开历史拍板落定:orphan 无祖先初始提交(计划 §9 留置项收口),filter-repo 候选否决;署名与仓名定稿(magician336 + flamingos);CI 双 job 形态定稿(主模块 + examples module 构建校验);推送前文件清单确认纪律与卫生终检入 Implementation Decisions;README 中文、架构图 mermaid、公开图片目录与私有 assets/ 分离 |
| 2026-11-08 | spec 0008 双轴评审收口联动:User Stories 第 4 条的 README Limitations 清单补点名两条——缓存开启态录制/回放误报边界、录制文件含工具结果原文的使用警示(spec 0008 Limitations 节为载体,README 实现时并入,保证 0008 两条边界在发布交付不丢) |
| 2026-11-09 | 双轴评审收口(票 01-03 范围):Standards 硬违规零、6 项 judgement call 处置(2 项修复:examples 包内去重——toolInfo 单一读取出处且失败文案单源、行为零变化,与 pickTool 无格式动词的 fmt.Errorf 改 errors.New;4 项留痕接受:flag 形态豁免[票面明定示例不入测试闭包]、gofmt spike 豁免与 actions 大版本 pin 有据、词表存量命中三处清洗[0005:93/0006:48/0006:88"面试"叙事命中改工程表述,其余命中为规范原文与历史留痕——0007 spec 规则原文、本 spec 词表定义与计划 §9 引用、0007 两票验收引用、票 02/04 票面原文——原样保留]、.gitignore 同类条目)、Spec 缺失 0、scope creep 2 轻微留痕接受(examples/.gitignore 防构建产物入库、examples/README.md 指路落点)、疑似不符 0;票 04 卫生终检判据前置障碍已清除(存量叙事词命中清洗,全树 grep 可全量执行,规范原文类命中按本行声明豁免) |
