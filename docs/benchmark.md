# flamingos 压测报告(spec 0007 票 03)

网关相对开销的实测数字与复现方法。数据由顶层 `benchmark/` 包的 vegeta 压测套件在本机采集,场景参数与包内常量一致;数字依赖环境,仅用于同机同构拓扑下的相对比较,不构成生产容量主张。

## 环境

| 项 | 值 |
|---|---|
| CPU | Intel Core Ultra 9 275HX,24 逻辑核 |
| 内存 | 30.7 GiB |
| 操作系统 | Fedora 44(kernel 7.2.15-200.fc44.x86_64) |
| Go | go1.27.3 linux/amd64 |
| MCP SDK | github.com/modelcontextprotocol/go-sdk v1.8.0 |
| 压测库 | github.com/tsenart/vegeta/v12 v12.13.0(库形态,无外部二进制) |
| 拓扑 | 三进程同机回环:压测进程(= `go test` 进程,vegeta Attacker)→ 网关真实二进制(cmd/flamingos,`-config` 临时 YAML 拉起)→ HTTP 假上游夹具(internal/upstream/testdata/httpserver);全部监听 127.0.0.1 随机端口 |
| 配置形态 | `listen` + 单 http 上游段;telemetry/policy 段缺省(追踪、策略、审计、限流全关闭态);cache 两形态(缺省关闭 / public 规则 `tools: ['*']` ttl_ms=5000) |

负载靶:Stateless 裸 POST `tools/call`,echo 工具,固定 JSON-RPC 体(参数字节恒定);请求头 `Content-Type: application/json` + `Accept: application/json, text/event-stream`(go-sdk v1.8.0 serveStateless 每请求建临时会话,无需 initialize——烟测单例裸调用实证,经网关聚合名 `bench_echo` 与直连原名 `echo` 均 200 且应答字节级回显一致)。应答为 200 text/event-stream 单 data 帧(SDK 缺省形态),vegeta 只计状态码与耗时。

## 方法学

- **恒定速率**(vegeta `ConstantPacer`):攻击器按 ticker 排程发请求、不等上一应答完成,响应慢时的延迟被如实计入而非被"完成才发下一个"的节拍掩盖(Coordinated Omission),P99 可信。
- **每臂预热**:正式计量前以同速率攻击 1s(建连与代码路径热身),预热样本不入计量。
- **场景与常量**(代码出处 `benchmark/full_test.go` / `benchmark/smoke_test.go`):

| 场景 | 参数(常量) | 样本数 |
|---|---|---|
| 恒定三档 | 50 / 200 / 500 rps × 15s/档(`tier1Rate/tier2Rate/tier3Rate`,`tierDuration`) | 750 / 3000 / 7500 |
| 阶梯加载 | 50→100→200→400→800 rps × 5s/级(`staircaseRates`,`staircaseStepDur`) | 每级 250/500/1000/2000/4000 |
| 缓存对比 | 200 rps × 15s,public 规则 ttl=5000ms(`cacheCompareRate/cacheCompareDuration/cacheRuleTTLMS`) | 每臂 3000 |
| 烟测 | 10 rps × 2s(`smokeRate/smokeDuration`) | 20 |

- **攻击器参数**:`vegeta.Proxy(nil)`(显式禁代理,防本机系统代理环境变量劫持回环流量)、单请求超时 10s(`attackerTimeout`,高于上游 5s 超时)、keep-alive 开启、worker 动态扩缩(v12 默认)。
- **相对开销臂**:同一攻击器形态分别打网关端点与直打上游夹具端点,同分位(P50/P95/P99)相减(网关 − 直连)。
- 缓存命中态由构造保证:public 规则 + 固定参数字节同键,ttl 5s 远大于 5ms 请求间隔,首次 miss 后全部命中。

## 结果:恒定三档与相对开销

三档全部零错误、状态码全 200、实际吞吐与排程速率一致。

| 档位 | 臂 | 样本 | 实际吞吐 | P50 | P95 | P99 | 错误率 |
|---|---|---|---|---|---|---|---|
| 50 rps | 经网关 | 750 | 50.1 rps | 2.478 ms | 3.651 ms | 4.024 ms | 0 |
| 50 rps | 直连基线 | 750 | 50.1 rps | 0.968 ms | 1.636 ms | 1.969 ms | 0 |
| **相对开销** | | | | **+1.509 ms** | **+2.014 ms** | **+2.055 ms** | |
| 200 rps | 经网关 | 3000 | 200.0 rps | 1.857 ms | 2.866 ms | 3.395 ms | 0 |
| 200 rps | 直连基线 | 3000 | 200.0 rps | 0.662 ms | 1.371 ms | 1.749 ms | 0 |
| **相对开销** | | | | **+1.195 ms** | **+1.495 ms** | **+1.646 ms** | |
| 500 rps | 经网关 | 7500 | 500.0 rps | 1.515 ms | 2.332 ms | 2.703 ms | 0 |
| 500 rps | 直连基线 | 7500 | 500.0 rps | 0.547 ms | 1.195 ms | 1.485 ms | 0 |
| **相对开销** | | | | **+0.968 ms** | **+1.137 ms** | **+1.218 ms** | |

**结论**:关闭态全链路(路由 → 聚合 → 上游转发)的相对开销为毫秒量级,三档 P50 差 0.97–1.51 ms,P99 差 1.22–2.06 ms;档位越高相对开销越小(两臂延迟分位均随速率上升而下降,见 Limitations;同档同分位相减的口径不受此影响)。为聚合付出的延迟成本在三档梯度内为 P99 ≤ 2.1 ms。

## 结果:阶梯加载

网关臂逐级抬升(级间以同速率预热 1s 衔接,速率单调递增):

| 级 | 样本 | 实际吞吐 | P50 | P95 | P99 | 错误率 |
|---|---|---|---|---|---|---|
| 50 rps | 250 | 50.2 rps | 2.551 ms | 3.720 ms | 4.038 ms | 0 |
| 100 rps | 500 | 100.1 rps | 2.250 ms | 3.392 ms | 3.918 ms | 0 |
| 200 rps | 1000 | 200.1 rps | 1.861 ms | 3.009 ms | 3.456 ms | 0 |
| 400 rps | 2000 | 400.1 rps | 1.614 ms | 2.487 ms | 2.945 ms | 0 |
| 800 rps | 4000 | 799.9 rps | 1.533 ms | 2.270 ms | 2.574 ms | 0 |

**结论**:50→800 rps 全程实际吞吐紧贴排程速率(800 rps 档 799.9 rps),延迟分位随档位单调下降、无排队积压形态,**测试范围内未观察到容量拐点**;拐点位于本次阶梯上界(800 rps)之上,位置未测定。

## 结果:缓存命中态 vs 关闭态

200 rps × 15s,同一 echo 负载,配置差异仅 cache 段:

| 臂 | 样本 | 实际吞吐 | P50 | P95 | P99 | 错误率 |
|---|---|---|---|---|---|---|
| 缓存关闭态 | 3000 | 200.0 rps | 1.878 ms | 2.873 ms | 3.298 ms | 0 |
| 缓存命中态(public,ttl=5s) | 3000 | 200.0 rps | 0.684 ms | 1.405 ms | 1.775 ms | 0 |
| **缓存收益(关 − 命中)** | | | | **−1.194 ms(−64%)** | **−1.467 ms** | **−1.522 ms** | |

命中态分位与直连基线臂几乎重合(P50 0.684 vs 0.662 ms):命中时缓存装饰器短路上游调用,网关侧残余成本约为单次 HTTP 往返 + 缓存查找。spec 0006 缓存层的收益在本拓扑下为全分位约 −45% 至 −64%。

## 复现命令

```sh
# 常规回归形态:benchmark 包只跑烟测(裸调用前提 + 低速率短时攻击)
go test ./benchmark -run 'TestBareCallSmoke|TestSmokeAttack' -v

# 全量形态:恒定三档 + 阶梯 + 缓存对比,一次命令产出本报告全部数字
FLAMINGOS_BENCHMARK=1 go test ./benchmark -run '^TestBenchmarkFullScenarios$' -v -timeout 30m
```

go 可执行按需替换为本机路径(本仓库主工作机为 /usr/local/go/bin/go)。本次报告数字来自 2026-11-08 的一次全量运行(`-run '^TestBenchmarkFullScenarios$' -v`,未带 `-race`,攻击进程数字不受竞争检测器开销污染;套件 158.14s 跑通,零错误)。同机重跑数字应为同量级。

## Limitations

- **同机竞争**:三进程共享 CPU、内存带宽与回环口,压测进程、网关、上游互相抢占;绝对值含该竞争,只有同档相对比较有效。
- **内存桩上游**:夹具 echo 零延迟回显,上游耗时被压到极限,网关开销占比被放大;真实上游延迟(毫秒至秒级)会按比例稀释相对开销,绝对开销(毫秒量级)不随上游变化。
- **无生产网络**:回环无拥塞、无 MTU 分段、无 TLS 终止;生产网络下直连臂自身延迟增大,相对开销占比进一步缩小。
- **瓶颈位置未测定**:阶梯上界 800 rps 未到拐点,瓶颈在更高处;未做 pprof/trace 剖析(spec Out of Scope)。延迟分位随速率上升而下降的现象两臂一致(高速率下 keep-alive 连接池更热,低速率档 P99 携带更多排程边界噪声),未深究,不影响同档相减口径。
- **缓存命中为构造保证**:网关不暴露命中观测面(telemetry 关闭态),命中态收益的归因依据是命中臂分位与直连基线臂重合,加上 spec 0006 缓存包自身的单元测试。
- **vegeta 不解析应答体**:JSON-RPC 层错误(协议级失败)仍是 200,不入 Errors;烟测单例裸调用钉住应答形状正确,压测流量为纯 echo 无失败路径。
- 单次运行样本;跨时点对比需同机同构重跑(复现命令如上)。
