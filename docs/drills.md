# 故障演练记录

spec 0007 票 02 的交付物:网关在三族故障注入(上游进程 kill -9、断流重发、异常注入)下的行为与恢复时间实测记录。演练自动化为 e2e 套件的一部分(`e2e/drills_test.go`,判据与断言的代码出处),本文件沉淀记录与数据;W4 README 的 Limitations 章节可引用本文件。

## 环境与测量口径

- 硬件与系统:Intel Core Ultra 9 275HX,24 逻辑核,Fedora 44(kernel 7.2.15-200.fc44.x86_64),全部回环口 127.0.0.1。
- 软件版本:go1.27.3,go-sdk `github.com/modelcontextprotocol/go-sdk/mcp` v1.8.0,网关构建自本仓库 68dbaee 基线。
- 拓扑:三进程回环——测试进程内 go-sdk 客户端(Agent 角色)→ 网关真实二进制(临时 YAML 拉起,`listen: 127.0.0.1:<空闲端口>`)→ 假上游夹具进程。每例两个上游:`calc`(HTTP 夹具,独立进程)与 `fs`(stdio 夹具,网关子进程)。配置为最小合法形态,telemetry/policy/cache 全部缺省(关闭态),上游 timeout 统一 5s。
- 测量口径:恢复窗口 = 注入时刻(注入动作执行前取 `time.Now()`)到首次成功调用应答返回,测试内 `time.Since` 采样、20ms 轮询步长、单窗口上限 10s;全部数字为单次套件运行(`go test ./e2e -race`)的观测值,同机回环、无网络抖动、夹具为内存桩,只用于同机相对比较,不构成生产容量或生产可用性主张。
- 断言判据:错误文案点名故障上游并含"不可用"语义(网关错误契约①的 Agent 侧形态);网关存活以 SIG 0 探测;进程死亡以 `cmd.Wait` 返回或新 PID 对照实证。

## 场景记录

### 1. kill -9 HTTP 上游(空载)

- 前置拓扑:calc(HTTP 夹具进程)+ fs(stdio 夹具子进程),网关已监听,Agent 已握手;`calc_pid` 应答与夹具拉起 PID 精确对照(注入目标确认)。
- 注入手段:`kill -9 <夹具 PID>`(PID 经 calc_pid 工具应答取得)。
- 预期:故障生效后 calc 调用点名不可用;网关存活;fs 照常;夹具同端口重启后调用恢复。
- 实际:故障生效错误文案(轮询首次观察到的形态,Agent 侧所见)——`calling "tools/call": 上游 "calc" 不可用: calling "tools/call": sending "tools/call": rejected by transport: Post "http://127.0.0.1:<port>/mcp": connect: connection refused`。多次运行还观测到同链路的 `read tcp ...: connection reset by peer` 与 `EOF` 变体(连接池中既有连接被击穿时的形态,三种同为传输层错误,网关统一映射为点名不可用)。网关存活,fs_echo 正常回显。
- 恢复窗口:36.3ms(注入 → 夹具同端口重启后首次成功调用;含夹具进程拉起与监听)。恢复后 calc_pid 应答为新 PID,同一 Agent 会话、同一网关进程未动。
- 结论:HTTP 上游骤死是单上游局部故障;网关到上游的 SDK 客户端会话容忍上游重启,不因一次传输层失败失效,恢复不需要网关重启。

### 2. kill -9 HTTP 上游(在途,slow 3s 进行中)

- 前置拓扑:同场景 1;`calc_slow` 调用在途(slow 固定延迟 3s,注入时已进行约 300ms)。
- 注入手段:`kill -9 <夹具 PID>`。
- 预期:在途调用以 err 收场并点名 calc;网关存活;fs 照常;夹具重启后恢复。
- 实际:在途调用错误文案——`calling "tools/call": 上游 "calc" 不可用: calling "tools/call": sending "tools/call": rejected by transport: Post "http://127.0.0.1:<port>/mcp": EOF`。断流传播即时(窗口上限 20s,实测远低于它),在途调用不以 5s 上游超时收场而以断流错误收场。网关存活,fs_echo 正常。
- 恢复窗口:35.8ms(注入 → 夹具重启后首次成功调用)。
- 结论:在途断流与空载骤死同语义,恢复路径一致;在途调用不受上游超时兜底拖延,断流即报。

### 3. kill -9 stdio 上游(空载)

- 前置拓扑:同场景 1;`fs_pid` 应答取得网关拉起的 stdio 子进程 PID。
- 注入手段:`kill -9 <子进程 PID>`。
- 预期:fs 调用点名不可用;网关存活;calc 照常;stdio 无再拉起机制(spec 0003 简化策略),恢复路径 = 网关重启。
- 实际:故障生效错误文案——`calling "tools/call": 上游 "fs" 不可用: calling "tools/call": EOF`。有限窗口内三次重试全部报错,子进程无再拉起(与 spec 0003"无自动重拉"简化策略一致)。网关存活,calc_echo 正常回显。
- 恢复窗口:376.1ms(注入 → SIGTERM 旧网关优雅退出(码 0)→ 同端口同配置拉起新网关 → 新 Agent 握手 → 首次成功调用)。恢复后 fs_pid 应答为新子进程 PID。
- 结论:stdio 上游骤死同样不放大为网关故障,但恢复必须经网关重启(单进程无池化、无健康探测、无再拉起,均为 spec 0003/0007 明示的简化边界)。运行含义:编排方对 stdio 上游故障的自动恢复手段是重启网关进程。

### 4. kill -9 stdio 上游(在途,fs_slow 3s 进行中)

- 前置拓扑:同场景 3;`fs_slow` 在途(约 300ms 时注入)。
- 注入手段:`kill -9 <子进程 PID>`。
- 预期:在途调用以 err 收场并点名 fs;网关存活;calc 照常;恢复 = 网关重启。
- 实际:在途调用错误文案——`calling "tools/call": 上游 "fs" 不可用: calling "tools/call": EOF`。网关存活,calc_echo 正常。
- 恢复窗口:64.9ms(注入 → 网关重启 → 首次成功调用;同场景 3 口径)。
- 结论:与空载同语义。同口径下两次网关重启窗口相差一个量级(场景 3 为 376.1ms,本例 64.9ms),差异来自进程调度与 SDK stdio 子进程拉起+握手的抖动,同机量级为百毫秒级。

### 5. 断流重发(HTTP,同一 Agent 会话)

- 前置拓扑:同场景 1;`calc_slow` 在途(约 300ms 时注入)。
- 注入手段:`kill -9 <夹具 PID>`;随后夹具同端口重启(上游恢复),Agent 会话自始至终未重连(同一 `ClientSession` 实例),直接重发同一调用(`calc_slow`,空参数)。
- 预期:在途调用失败 → 上游恢复 → 同一会话重发成功。
- 实际:在途调用报点名 calc 不可用(同场景 2 文案);重发成功,应答为 slow 固定文案"迟到的应答"。网关侧全程无自动重试(spec 0007 Out of Scope),记录的是客户端重发语义。
- 恢复窗口:3.03s(注入 → 重发成功)。口径拆解:其中 3.01s 为重发调用自身含 slow 工具的 3s 固定延迟,注入 → 重发发起为 24.7ms。纯"故障恢复 + 客户端重发"的净窗口为毫秒级。
- 结论:HTTP 上游断流后,"等上游恢复 + 同会话重发"对 Agent 是可靠的恢复策略;网关不需要(也没有)为重发提供任何新机制。

### 6. 异常注入:panic(并案 kill -9 语义)

- 前置拓扑:同场景 1,但 HTTP 夹具以注入形态启动(argv 追加 `inject` 参数,tool 表追加 `panic` 与 `rawreply` 两个注入工具,默认形态不变)。
- 注入手段:经网关调用 `calc_panic`,夹具 handler 直接 panic,无 recover。
- 预期(实证对象):go-sdk v1.8.0 生产代码零 recover,且 tools/call 在 SDK 会话读 goroutine 上执行(streamable.go servePOST 把消息发进会话 incoming 通道,handler 由 connect() 内 spawned 的 jsonrpc2 读 goroutine 调用)——panic 不在 net/http 连接 goroutine 上,"每连接 recover 隔离"不适用,夹具进程自身崩溃;网关存活。
- 实际:夹具进程自身崩溃退出,退出码 2(Go panic);`Wait` 返回即"自身崩溃而非被外部杀死"的直接证据(net/http 隔离若生效,进程不会退出)。注入调用错误文案——`calling "tools/call": 上游 "calc" 不可用: calling "tools/call": sending "tools/call": rejected by transport: Post "http://127.0.0.1:<port>/mcp": EOF`。网关存活,fs_echo 正常。
- 恢复窗口:43.9ms(注入 → 夹具重启后首次成功调用,同场景 1 恢复路径)。
- 结论:panic 与 kill -9 并案成立(进程级骤死、同恢复路径);上游 panic 未放大为网关故障。边界钉死:net/http 的每连接 recover 只覆盖连接 goroutine 上的 panic,SDK 会话 goroutine 上的 panic 击穿进程——这一事实同时适用于上游进程(本例)与任何在会话 goroutine 上跑 handler 的 SDK 服务端。

### 7. 异常注入:协议外应答

- 前置拓扑:同场景 6(注入形态夹具)。夹具在 SDK handler 外包一层裸 HTTP 短路器:对 tools/call 目标为 `rawreply` 的请求,SDK handler 不可达,直接写回 200 + `text/html` 的非 JSON-RPC 字节流(错误 Media Type 形态)。
- 注入手段:经网关调用 `calc_rawreply`。
- 预期:该调用得到明确错误(err 或 IsError 之一,按实测钉定);网关存活;后续调用照常。
- 实际:
  - 错误形态 = **err**(非 IsError):`calling "tools/call": 上游 "calc" 不可用: calling "tools/call": sending "tools/call": unsupported content type "text/html"`。SDK 客户端在解析路径拒绝非法 Media Type,请求以传输层错误收场,业务失败通道(IsError)不经过。
  - 会话事实(超出预期的实测发现):SDK 把非法 Media Type 当作致命协议错误,**整条客户端会话就此关闭**——网关"每上游一条长会话"构造下,该上游后续调用持续报错(探针实测 3s 内 10 次重试全部 `上游 "calc" 不可用: ... client is closing: EOF`,无自愈);另一上游照常;网关存活。
  - 恢复路径 = 网关重启(与 stdio 同款第二恢复路径)。
- 恢复窗口:381.9ms(注入 → 网关重启 → 首次成功调用)。
- 结论:协议外应答不击穿网关进程,但会废掉网关到该上游的单条会话(比 kill -9 更"毒":上游重启即可恢复的 kill -9,在这里必须网关重启)。"后续调用照常"的实测边界 = 另一上游照常,同一上游需要网关重启——与票面预期的偏差如实记入 Limitations。

## Limitations 与运行说明

1. **stdio 上游无再拉起机制**(spec 0003 简化策略实证):stdio 子进程死亡后该上游持续不可用,恢复路径 = 重启网关进程(同端口同配置)。编排方运行说明:对 stdio 上游故障配置进程级看护时,看护对象是网关而非上游子进程。
2. **HTTP 上游骤死后会话可自愈**(实测):夹具进程 kill -9 后同端口重启,网关会话不重连即恢复(场景 1/2/6,窗口毫秒级)。运行含义:HTTP 上游可以独立于网关重启。
3. **协议外应答废掉单条上游会话**(实测,超出票面预期):非法 Media Type 使 SDK 客户端会话永久关闭,该上游后续调用持续报错直到网关重启(场景 7)。运行含义:上游行为异常(返回非 JSON-RPC 应答)比上游骤死更难恢复;该行为出自 go-sdk v1.8.0 客户端,升级 SDK 需复核。
4. **SDK 会话容忍度小结**(实测):传输层连接失败(EOF/RST/refused)不关闭会话,下次调用即走新连接;协议层违规(非法 Media Type)关闭会话,不可自愈。两者的分界是本票钉住的 SDK 行为事实。
5. **断流重发是客户端语义**:网关无自动重试、无熔断、无健康探测(spec 0007 Out of Scope);场景 5 实证同一 Agent 会话直接重发可行,重发窗口的净值为毫秒级(不计 slow 工具自身延迟)。
6. 测量边界:同机回环、内存桩上游、单次运行观测;数字只支撑同机相对比较与量级判断,不构成生产可用性主张。

## 应急修复条款

spec 0007 预设:演练若暴露网关进程级崩溃路径(整个进程退出,超出 net/http 单连接隔离),时间盒内最小修复(候选:transport 层 recover 中间件)+ 定点单测 + spec 修订表追记。

**本票全部七个场景中网关进程均存活(SIG 0 探测通过),未发现任何网关进程级崩溃路径,应急修复条款未触发。** internal/ 与 cmd/ 生产文件零改动,diff 检查仅含夹具(testdata)、e2e 演练测试与本文件。
