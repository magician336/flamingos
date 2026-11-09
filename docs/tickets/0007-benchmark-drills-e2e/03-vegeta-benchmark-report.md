# 03:vegeta 压测与压测报告

**Spec:** [0007 运行质量实证:vegeta 压测、故障演练与基础 e2e](../../../specs/0007-benchmark-drills-e2e.md)
**What to build:** 网关相对开销获得可复现数字。顶层 `benchmark/` 包诞生(零 import internal);vegeta 以 v12 库形态引入(go.mod 唯一新直接依赖,无需安装外部二进制;Attacker + ConstantPacer + Metrics 分位数)。harness 沿 01:构建网关二进制与上游夹具、临时配置拉起,攻击进程 / 网关 / 上游夹具三进程同机回环拓扑。**烟测**(门控内随常规回归):单档短时压测,钉住 Stateless 裸 POST tools/call 无需 initialize 前提(SDK serveStateless 每请求建临时会话,v1.8.0 源码核查);前提不成立则靶体改 initialize+call 配对并在报告方法节记录;烟测无错误响应为机械判据。**全量场景显式命令触发**(默认门控跳过,常规回归时长不受影响):恒定速率三档(避免 Coordinated Omission,P99 可信)+ 一次阶梯加载(速率逐级抬升看容量拐点)+ 缓存命中态 vs 关闭态对比一档(public 规则、固定参数保证命中,spec 0006 收益的数字背书);相对开销主张 = 攻击器分别打网关端点与直打上游夹具端点的同分位(P50/P95/P99)差;压测配置形态 = policy/telemetry 段缺省(策略/审计/限流/追踪全关闭态)、cache 两形态。docs/benchmark.md 报告(工程语境,W4 README 压测表直接引用):环境交代(硬件/Go 与 SDK 版本/拓扑/回环)、方法学(速率档位/时长/样本数与代码常量一致)、分位数表、复现命令、Limitations(同机竞争、内存桩上游、无生产网络、瓶颈位置诚实标注)。压测数字本身不作硬验收门槛(环境依赖,只用于相对比较)。
**Blocked by:** 01(HTTP 假上游夹具 + e2e 骨架——harness 与夹具就绪)。
**Status:** done (2026-11-08)

- [x] go.mod 仅增 vegeta 一条直接依赖;benchmark 顶层包零 import internal(可机械核对)
- [x] 门控形态:常规 go test ./... 只跑烟测,全量场景经显式命令触发
- [x] 烟测过:Stateless 裸 POST 前提实证(或降级形态留痕于报告方法节),无错误响应
- [x] 全量场景跑通:恒定三档 + 阶梯 + 缓存对比,直连基线臂与经网关臂同分位数字齐
- [x] docs/benchmark.md:环境交代/方法学/分位表/复现命令/Limitations 齐备,复现命令与代码常量一致,零教学求职语境
- [x] `go build ./...` + `go test ./... -race` 全绿,既有测试零改动
- [x] diff 检查:新增 benchmark 包 + go.mod 一行直接依赖,internal/ 与 cmd/ 生产文件零改动

> 定档留痕:①vegeta 版本与形态——go get 实际解析 v12.13.0(库形态,`vegeta.NewAttacker` + `ConstantPacer{Freq, Per}` + `Metrics` 分位),go.mod 仅增一条直接依赖,间接闭包(tdigest/intern/easyjson/dnscache)由 go mod tidy 收敛;机械核对 `go list -deps -test ./benchmark` 闭包只含自身与 vegeta lib,零 import internal/e2e。②场景常量(报告与之一致)——恒定三档 50/200/500 rps × 15s、阶梯 50→100→200→400→800 rps × 5s/级、缓存对比 200 rps × 15s(public 规则 `tools: ['*']` ttl_ms=5000)、每臂预热 1s 不入计量、烟测 10 rps × 2s;Go 数组字面量不可为常量,速率表以包级 var 固定初始化、元素值对应单个常量。③裸 POST 前提实证通过,无需降级——烟测单例裸调用:经网关聚合名 `bench_echo` 与直连原名 `echo` 均 200 且应答字节级回显一致(serveStateless 每请求临时会话,无需 initialize;头为 Content-Type: application/json + Accept 双媒体类型,缺一即 415/400);应答形态为 200 text/event-stream 单 data 帧(网关与夹具 JSONResponse 均缺省 false,`useSSE = !c.jsonResponse`,streamable.go:1683),靶体解析按 SSE data 行拼接。④vegeta v12.13 事实——Attacker 单次使用(Attack 结束 defer Stop 关闭攻击器级 stopch,复用同一 Attacker 跑第二场会得零样本,首跑实踩后改为每臂新建),默认 transport 带 `Proxy: http.ProxyFromEnvironment`(本机 Clash 代理环境变量会劫持回环流量,攻击器显式 `vegeta.Proxy(nil)`),`ConstantPacer.Freq` 为 int。⑤全量数字(2026-11-08 本机一次运行,套件 158.14s 零错误全 200;不带 -race 跑,攻击进程数字不受竞争检测器开销污染)——相对开销:50 rps 档 P50/P95/P99 差 +1.509/+2.014/+2.055 ms,200 rps 档 +1.195/+1.495/+1.646 ms,500 rps 档 +0.968/+1.137/+1.218 ms;缓存收益:命中态 P50 −64%(1.878→0.684 ms),命中臂分位与直连基线臂几乎重合(P50 0.684 vs 0.662 ms,命中短路上游调用的旁证);阶梯 50→800 rps 吞吐紧贴排程(800 档 799.9 rps),无拐点,瓶颈在 800 rps 之上未测定。⑥偏差与注意——两臂延迟分位均随速率上升而下降(高速率下 keep-alive 连接池更热),报告中如实标注且不影响同档相减口径;vegeta 不解析 SSE 体,JSON-RPC 层错误仍为 200 不入 Errors,正确性由裸调用烟测钉住;全量命令 `FLAMINGOS_BENCHMARK=1 go test ./benchmark -run '^TestBenchmarkFullScenarios$' -v -timeout 30m`。
> 评审收口:①benchmark/harness_test.go waitForListen 的内联 10s(deadline 与错误文案两处)具名为包内常量 procWindow,与 e2e 包同名常量各包自持、值一致、注释互指(错误文案由字面量改经 %s 渲染,输出逐字节不变);②e2e/benchmark 两包 doc 注释立档自足纪律——"本套件不 import 其他测试套件(e2e/benchmark 互不依赖),harness 形状复制自 cmd 测试先例——共享需先立架构布局内共享包,复制代价已知且被接受",此前散落在票 01 留痕的自加禁令升为两包显式裁决;跨包 harness 孪生本体的结构合并留待未来立共享包时一次性做(本票只消除漂移点并立档)。
